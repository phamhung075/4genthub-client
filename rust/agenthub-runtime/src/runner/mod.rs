//! `agenthub-runtime runner <room>/<seat>` — the tmux pane's foreground process.
//!
//! It spawns the seat's agent in RPC mode, owns the only channel to it, and proxies the pane:
//!
//! ```text
//!   tmux pane ──► runner ──stdin/stdout (newline JSON)──► omp --mode rpc
//!                   ▲
//!                   └── rooms/<room>/run/<seat>.sock   (the daemon's mailbox: compact, …)
//! ```
//!
//! Two measured facts shape this port (references are `pi-runner.js`, **P**, and
//! `pi-runner-protocol.js`, **PR**, in `the rig CLI package/daemon/dist/adapters`):
//!
//! * the reference child's channel is a pair of **pipes** on fd 0/1/2, not a socketpair
//!   (`P:818-822`); the design calls it a socketpair. Ownership is what matters — nobody else holds
//!   this channel — so this module uses pipes and says so rather than inventing a socketpair to match
//!   a sentence.
//! * compaction is **observed**, never issued, by the reference (`P:227-238`; no `{"type":"compact"}`
//!   anywhere in the package). The frame below is this runtime's own, and the daemon reaches it over the
//!   seat's mailbox socket instead of stealing a descriptor with `pidfd_getfd`, which needed
//!   `CAP_SYS_PTRACE` and is retired with this module.

mod claude;

use crate::types::{AgentRuntime, RoomSpec, SeatSpec};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::collections::VecDeque;
use std::fs;
use std::io::{BufRead, BufReader, Write};
use crate::frame::{self, SpillTarget};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use parking_lot::Mutex;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{self, Sender};
use std::sync::{Arc, OnceLock};
use std::thread;
use std::time::Duration;
use tracing::{error, info, warn};

/// The pane's input cap, and the rejection wording, are the reference's (`P:34-36`) — an operator who
/// sees this line should see the same one they see today.
const MAX_PANE_INPUT_BYTES: usize = 1024 * 1024;
const PANE_INPUT_REJECTED: &str = "[omp-runner] input rejected: maximum 1048576 UTF-8 bytes; send a smaller message. Ctrl-C clears unfinished input.";
/// How the daemon's frames and the child's RPC errors reach the pane, in the reference's own voice
/// (`P:355-375`).
const PANE_PREFIX: &str = "[omp-runner]";

/// `<room>/<seat>`, as the daemon and the error messages spell a seat.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RunnerTarget {
    pub room: String,
    pub seat: String,
}

impl RunnerTarget {
    pub fn parse(spec: &str) -> Result<Self, String> {
        let (room, seat) = spec.split_once('/').ok_or_else(|| {
            format!("runner target must be <room>/<seat>; got '{spec}'")
        })?;
        if room.trim().is_empty() || seat.trim().is_empty() {
            return Err(format!(
                "runner target must be <room>/<seat> with both non-empty; got '{spec}'"
            ));
        }
        Ok(Self {
            room: room.to_string(),
            seat: seat.to_string(),
        })
    }

    /// The mailbox the daemon writes frames into: inside the room, one file per seat.
    pub fn mailbox(&self, state_dir: &Path) -> PathBuf {
        state_dir
            .join("rooms")
            .join(&self.room)
            .join("run")
            .join(format!("{}.sock", self.seat))
    }
}

/// Where the seat's agent session lives: `<state dir>/rooms/<room>/seats/<seat>`, the directory
/// `RoomSupervisor::start_seat` already computes and exports as `PI_CODING_AGENT_DIR`.
fn seat_dir(state_dir: &Path, target: &RunnerTarget) -> PathBuf {
    state_dir
        .join("rooms")
        .join(&target.room)
        .join("seats")
        .join(&target.seat)
}

/// The child's argv, per `PR:220-242`: the RPC mode, the seat's own session directory, the approval
/// mode for this runtime, and the model when the seat has one.
fn child_argv(runtime: &AgentRuntime, seat_dir: &Path, seat: &SeatSpec) -> Result<Vec<String>, String> {
    match runtime {
        AgentRuntime::Omp => {
            let mut argv = vec![
                "--mode".to_string(),
                "rpc".to_string(),
                "--session-dir".to_string(),
                seat_dir.join("sessions").to_string_lossy().to_string(),
            ];
            // `--approval-mode` is the OMP spelling (`PR:224-225`); the seat's documented permission policy
            // decides it here, and an unknown word is refused rather than guessed (see `approval_mode`).
            argv.push("--approval-mode".to_string());
            argv.push(approval_mode(seat.permission_policy.as_deref())?.to_string());
            if let Some(model) = seat.model.as_deref().filter(|m| !m.trim().is_empty()) {
                argv.push("--model".to_string());
                argv.push(model.to_string());
            }
            Ok(argv)
        }
        AgentRuntime::ClaudeCode => {
            let mut argv: Vec<String> = [
                "-p",
                "--input-format",
                "stream-json",
                "--output-format",
                "stream-json",
                "--verbose",
                "--include-partial-messages",
                "--permission-mode",
            ]
            .map(String::from)
            .to_vec();
            // The same two postures as omp's: unattended, or the default mode, whose approvals a headless
            // session cannot answer and so denies.
            argv.push(
                match approval_mode(seat.permission_policy.as_deref())? {
                    "yolo" => "bypassPermissions",
                    _ => "default",
                }
                .to_string(),
            );
            if let Some(model) = seat.model.as_deref().filter(|m| !m.trim().is_empty()) {
                argv.push("--model".to_string());
                argv.push(model.to_string());
            }
            Ok(argv)
        }
        other => Err(format!(
            "runner has no native rpc path for runtime '{}'; seat '{}' needs one of: omp, claude-code",
            other.as_str(),
            seat.seat_key
        )),
    }
}

/// The executable a runtime is launched as: the seat's runtime name is not always the program's.
fn child_program(runtime: &AgentRuntime, seat: &SeatSpec) -> String {
    match runtime {
        AgentRuntime::ClaudeCode => "claude".to_string(),
        _ => seat.runtime.clone(),
    }
}

/// The runner process: spawn, handshake, then serve the pane and the mailbox until the child exits.
pub fn run(state_dir: &Path, target_spec: &str) -> Result<(), String> {
    let target = RunnerTarget::parse(target_spec)?;
    let room_dir = state_dir.join("rooms").join(&target.room);
    let spec_file = room_dir.join("spec.json");
    let spec_text = fs::read_to_string(&spec_file).map_err(|e| {
        format!(
            "cannot read room spec '{}': {e} (the room must be started first)",
            spec_file.display()
        )
    })?;
    let spec: RoomSpec =
        serde_json::from_str(&spec_text).map_err(|e| format!("cannot parse room spec: {e}"))?;
    let seat = spec
        .seats
        .iter()
        .find(|s| s.seat_key == target.seat)
        .ok_or_else(|| format!("seat '{}' is not in room '{}'", target.seat, target.room))?
        .clone();

    let runtime = AgentRuntime::from_str_loose(&seat.runtime);
    let seat_dir = seat_dir(state_dir, &target);
    // ONE PROCESS IS ONE SEAT'S RUNNER, so its log is decided here, once, before anything can be mirrored
    // into the pane - including the refusal to launch at all, which is the first line a bad config writes.
    let _ = SEAT_LOG.set(seat_log_path(state_dir, &target.room, &target.seat));
    // A refusal to launch is a *config* outcome, and the caller must be able to tell it from a crash: the
    // published death carries the refusal's own text, so a mistyped policy word names the word and a blank
    // one says it is empty, instead of both reading as "died before readiness" (the lead's request). The room's
    // own directory for the seat is the same kind of config, so it leaves by the same door.
    let refuse = |reason: String| -> Result<(), String> {
        let path = state_file_path(state_dir, &target.room, &target.seat);
        // The seat's directory may not exist yet (nothing has launched), and a refusal that cannot be
        // published is exactly the gap this closes: measured, the first version left no state file at all.
        if let Some(parent) = path.parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let _ = write_state(&path, "agent_exited", &reason, 0);
        mirror(&reason);
        Ok(())
    };
    let argv = match child_argv(&runtime, &seat_dir, &seat) {
        Ok(argv) => argv,
        Err(e) => return refuse(format!("refusing to launch: {e}")),
    };
    // Where the agent itself runs. The supervisor launched this pane in the same directory, and the runner asks
    // the room's spec again rather than trusting a shell it may not have been started by: the resolution is one
    // function, used by both, so a seat started by hand lands where the room says.
    let cwd = match crate::supervisor::seat_scope(&seat, &spec, &room_dir) {
        Ok(scope) => scope[0].clone(),
        Err(e) => return refuse(format!("refusing to launch: {e}")),
    };
    fs::create_dir_all(&seat_dir).map_err(|e| format!("cannot create seat dir: {e}"))?;

    // The supervisor already put AGENTHUB_* and PI_CODING_AGENT_DIR into this process's environment when
    // it launched the pane, so the child inherits the seat's identity rather than a second copy of it.
    let program = child_program(&runtime, &seat);
    let mut launch = Command::new(&program);
    launch
        .args(&argv)
        .current_dir(&cwd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit()); // the pane: the human sees omp's own errors
    // Default deny, before anything is spawned: the agent must not inherit the live rig's addressing, which
    // the daemon's own environment would otherwise hand it (`crate::scrub_env` states the measurement).
    crate::scrub_env(&mut launch);
    let mut child = launch
        .spawn()
        .map_err(|e| format!("cannot spawn '{program}': {e}"))?;
    info!(
        "runner for {}/{} spawned {} {:?} (pid {:?})",
        target.room,
        target.seat,
        program,
        argv,
        child.id()
    );

    let child_stdin = child
        .stdin
        .take()
        .ok_or_else(|| "child stdin was not piped".to_string())?;
    let child_stdout = child
        .stdout
        .take()
        .ok_or_else(|| "child stdout was not piped".to_string())?;

    // One writer owns the child's stdin; everything else posts frames to it over this channel.
    let (to_child, from_threads) = mpsc::channel::<String>();
    let writer_state_path = state_file_path(state_dir, &target.room, &target.seat);
    // The agent's dialect. omp is the runner's own protocol and passes through; Claude Code is translated
    // both ways (`claude.rs`), and `reader_tx` carries the frames only the translator can answer with.
    let bridge: Option<Arc<claude::Bridge>> =
        matches!(runtime, AgentRuntime::ClaudeCode).then(|| Arc::new(claude::Bridge::default()));
    let (reader_tx, reader_rx) = mpsc::channel::<String>();
    let writer_bridge = bridge.clone();
    let writer_reader_tx = reader_tx.clone();
    let writer = thread::spawn(move || {
        let mut stdin = child_stdin;
        let state_path = writer_state_path;
        for frame in from_threads {
            let frame = match &writer_bridge {
                None => frame,
                Some(bridge) => {
                    let outbound = match serde_json::from_str::<serde_json::Value>(frame.trim()) {
                        Ok(value) => bridge.from_runner(&value),
                        Err(_) => continue,
                    };
                    for reply in outbound.to_reader {
                        let _ = writer_reader_tx.send(reply.to_string());
                    }
                    outbound.to_agent.iter().map(|f| format!("{f}\n")).collect::<String>()
                }
            };
            if frame.is_empty() {
                continue;
            }
            if let Err(e) = stdin.write_all(frame.as_bytes()).and_then(|_| stdin.flush()) {
                // The child's end of the channel is gone. That is a fact the runner must *publish*: a
                // sealed pipe is not the same event as the child exiting, and a daemon that only sees its
                // own mailbox connection close cannot tell them apart (`SPEC` §3).
                warn!("runner: cannot write to the child: {e}");
                let _ = write_state(&state_path, "agent_socket_closed", "the child's channel is gone", 0);
                break;
            }
        }
    });

    // The RPC reader: responses answer a pending id, events drive the pane and the seat's state, and a
    // turn that dies retryably is resumed from here (never from the sight of an error line alone).
    let state_path = state_file_path(state_dir, &target.room, &target.seat);
    let pending: Arc<Mutex<Option<PendingPrompt>>> = Arc::new(Mutex::new(None));
    // The text a prompt refused mid-turn left behind, waiting for the seat to be free again. It sits beside
    // the pending slot because the mailbox (which relays the daemon's prompts) writes that slot and the
    // reader (which sees the refusal) owns both.
    //
    // A QUEUE, not a slot (reviewer, 2026-10-15, `GATE-c0ffbe8-refused-send`): two prompts refused inside
    // one turn both have to survive, and a single slot silently overwrote the first with the second - the
    // same lost-message class this hold exists to remove. Order is the order they were refused, and one
    // text is delivered per turn end, so a delivery never races the turn it opens.
    let held: Arc<Mutex<VecDeque<HeldText>>> = Arc::new(Mutex::new(VecDeque::new()));

    let mailbox = target.mailbox(state_dir);
    // The mailbox relays prompts, so it is given the same pending slot the pane's own input uses: a relayed
    // prompt whose response this runner did not recognise was how a refusal fell through to "nothing to
    // resume" while the text was gone.
    spawn_mailbox(
        mailbox.clone(),
        to_child.clone(),
        state_dir.to_path_buf(),
        target.clone(),
        Arc::clone(&pending),
    )?;
    // Two reports of the *same* failure for the same prompt are one failure: the reviewer measured two
    // copies of the message in flight and `resume_attempts` reaching 8 while the send bound held at 4. The
    // key is the pending id plus the error text, so a retry that fails again is a *new* failure and still
    // counts — the ladder is not weakened, only the double count is removed.
    let failures_seen: Arc<Mutex<std::collections::HashSet<String>>> =
        Arc::new(Mutex::new(std::collections::HashSet::new()));
    let ready = {
        let (ready_tx, ready_rx) = mpsc::channel::<Result<(), String>>();
        let pending_reader = Arc::clone(&pending);
        let held_reader = Arc::clone(&held);
        let failures_reader = Arc::clone(&failures_seen);
        let to_child_reader = to_child.clone();
        let state_path = state_path.clone();
        // The hand-back the held bound reaches needs the two things any relay needs: where the state lives,
        // and which seat this runner is. Cloned once here rather than read from the environment later.
        let state_dir_reader = state_dir.to_path_buf();
        let target_reader = target.clone();
        thread::spawn(move || {
            let lines = agent_lines(child_stdout, bridge, reader_tx, reader_rx);
            let mut announced_ready = false;
            let mut turn_active = false;
            // Whether the agent itself has a retry in flight (`auto_retry_start` seen, no `auto_retry_end`
            // yet). This is a LOCAL fact, not an inference about omp's frame order: the reviewer forced the
            // sequence `turn_start -> auto_retry_start -> turn_end -> failed response` and the ladder fired
            // 1/3, 2/3, 3/3 on top of the agent's own retry. Cleared on its end, so a failure arriving
            // while the agent is still retrying never stacks ours onto it.
            let mut agent_retrying = false;
            let mut attempts: u32 = 0;
            for line in lines {
                let line = match line {
                    Ok(l) => l,
                    Err(e) => {
                        error!("runner: cannot read the child's stdout: {e}");
                        break;
                    }
                };
                let trimmed = line.trim();
                if trimmed.is_empty() {
                    continue;
                }
                let record: serde_json::Value = match serde_json::from_str(trimmed) {
                    Ok(v) => v,
                    Err(_) => {
                        // Not a frame: the child is writing something human-readable; show it.
                        mirror(trimmed);
                        continue;
                    }
                };
                match record.get("type").and_then(|t| t.as_str()) {
                    // The transport handshake gates everything (`P:875-879`): until `ready`, omp is not
                    // listening for requests.
                    Some("ready") => {
                        announced_ready = true;
                        let _ = ready_tx.send(Ok(()));
                    }
                    Some("response") => {
                        let id = record.get("id").and_then(|v| v.as_str()).unwrap_or("?");
                        let failed = record.get("success").and_then(|v| v.as_bool()) == Some(false);
                        let error_text = record
                            .get("error")
                            .and_then(|v| v.as_str())
                            .map(|s| s.to_string());
                        if id == "runner-get-state" {
                            if failed {
                                let _ = ready_tx.send(Err(format!(
                                    "the agent answered get_state with an error: {}",
                                    error_text.as_deref().unwrap_or("no error given")
                                )));
                            } else {
                                let _ = ready_tx.send(Ok(()));
                                let _ = write_state(&state_path, "idle", "", 0);
                            }
                        }
                        let is_our_prompt = {
                            let guard = pending_reader.lock();
                            guard.as_ref().is_some_and(|p| p.id == id)
                        };
                        if let Some(err) = &error_text {
                            mirror(&format!("ERROR rpc: {err}"));
                        }
                        if failed && is_compact_reply(id) {
                            mirror("compact refused; no turn is affected, the seat keeps its state");
                        } else if failed {
                            if let Some(err) = error_text {
                                let has_pending = pending_reader.lock().is_some();
                                if !is_our_prompt && has_pending && !turn_active {
                                    // The measured correlation gap: omp answered an error without an id this
                                    // runner recognises. Say so, then treat the pending prompt as the turn.
                                    mirror("rpc error without a matching id; treating the pending prompt as its turn");
                                }
                                // `agent_retrying` folds into the same rule as "the turn is still running":
                                // while the agent has its own retry in flight, this ladder must not start
                                // one of its own. That makes the property local — a fact this runner
                                // observed — instead of an inference about the order omp sends its frames.
                                match decide_resume(is_our_prompt, turn_active || agent_retrying, has_pending, &err, attempts) {
                                    Resume::Nothing(reason) => {
                                        mirror(&format!("rpc error, nothing to resume: {reason}"));
                                    }
                                    Resume::Retry {
                                        attempt,
                                        delay_secs,
                                        label,
                                    } => {
                                        let key = format!(
                                            "{}-{err}",
                                            pending_reader
                                                .lock()
                                                .as_ref()
                                                .map(|p| p.id.clone())
                                                .unwrap_or_else(|| id.to_string())
                                        );
                                        if !failures_reader.lock().insert(key) {
                                            mirror(&format!(
                                                "failure already counted (attempt {attempt}/{MAX_RESUME_ATTEMPTS}); not sending a second copy: {label}"
                                            ));
                                        } else {
                                        attempts = attempt;
                                        mirror(&format!(
                                            "retry {attempt}/{MAX_RESUME_ATTEMPTS} in {delay_secs}s: {label} :: {err}"
                                        ));
                                        let _ = write_state(&state_path, "retrying", label, attempts);
                                        let resume_prompt =
                                            pending_reader.lock().as_ref().cloned();
                                        if let Some(prompt) = resume_prompt {
                                            let sender = to_child_reader.clone();
                                            let pending_retry = Arc::clone(&pending_reader);
                                            thread::spawn(move || {
                                                thread::sleep(Duration::from_secs(delay_secs));
                                                let id = format!(
                                                    "runner-resume-{}-{attempt}",
                                                    std::process::id()
                                                );
                                                // The origin and the held count ride along: this is the same
                                                // text, so it is the same sender to answer to and the same
                                                // bound to compare against.
                                                *pending_retry.lock() = Some(PendingPrompt {
                                                    id: id.clone(),
                                                    text: prompt.text.clone(),
                                                    origin: prompt.origin,
                                                    held_attempts: prompt.held_attempts,
                                                });
                                                let _ = send(
                                                    &sender,
                                                    &serde_json::json!({
                                                        "id": id,
                                                        "type": "prompt",
                                                        "message": prompt.text
                                                    }),
                                                );
                                            });
                                        }
                                        }
                                    }
                                    Resume::Blocked(reason) => {
                                        mirror(&format!("gave up: {reason} :: {err}"));
                                        attempts += 1;
                                        // A terminal outcome ends this prompt's life here. Leaving it pending
                                        // is how a *later* retryable error with a foreign id gets mistaken for
                                        // this prompt's turn and schedules a re-send after the seat already
                                        // published `blocked` - the durable record flipping blocked ->
                                        // retrying (found by the reviewer reading this arm).
                                        *pending_reader.lock() = None;
                                        let _ =
                                            write_state(&state_path, "blocked", reason, attempts);
                                    }
                                    Resume::Hold(reason) => {
                                        // The agent refused this prompt because its own turn holds it: the
                                        // text is not dropped (that was the defect) and it is not re-sent
                                        // into the turn that refused it. It is held for the next turn,
                                        // carrying the origin it will be answered at, and the state file says
                                        // so while it waits - which is what a reader (the daemon's `status`,
                                        // an operator's `ps`) sees instead of `idle`, the word that made the
                                        // refusal invisible.
                                        let prompt = pending_reader.lock().take();
                                        match prompt {
                                            Some(prompt) => {
                                                let refused_id = id.to_string();
                                                let origin = prompt.origin.clone();
                                                let entry = HeldText {
                                                    refused_id: refused_id.clone(),
                                                    text: prompt.text.clone(),
                                                    origin: origin.clone(),
                                                    // A text that was already held keeps the offers it has
                                                    // spent: the bound is the TEXT's, and a requeue is not a
                                                    // fresh start.
                                                    attempts: prompt.held_attempts,
                                                };
                                                let waiting = hold_text(&held_reader, entry);
                                                let sentence = publish_hold(
                                                    &state_path,
                                                    waiting,
                                                    attempts,
                                                    &origin,
                                                );
                                                match waiting {
                                                    Ok(depth) => {
                                                        mirror(&format!(
                                                            "held for the next turn: {reason} :: {err} :: the text is {}",
                                                            origin.label()
                                                        ));
                                                        if depth > 1 {
                                                            // The second refusal must be visible as a queue, not
                                                            // as a replacement: the text in front of it is still
                                                            // waiting and will be delivered first.
                                                            mirror(&format!(
                                                                "the text {refused_id} from {} waits behind {} held text(s); they are delivered in the order they were refused",
                                                                origin.label(),
                                                                depth - 1
                                                            ));
                                                        }
                                                    }
                                                    // A full queue drops NOTHING silently: the text goes back
                                                    // to the session that sent it, with the reason and the
                                                    // depth the bound was reached at.
                                                    Err(depth) => {
                                                        let full = format!(
                                                            "the runner's held queue is full ({depth} of {MAX_HELD})"
                                                        );
                                                        match return_to_sender(
                                                            &state_dir_reader,
                                                            &origin,
                                                            &target_reader,
                                                            &refused_id,
                                                            &prompt.text,
                                                            &full,
                                                        ) {
                                                            Ok(outcome) => mirror(&format!(
                                                                "refused, handed back to its sender: {sentence} ({outcome})"
                                                            )),
                                                            Err(e) => mirror(&format!(
                                                                "refused, NOT held and NOT handed back: {sentence} ({e})"
                                                            )),
                                                        }
                                                    }
                                                }
                                            }
                                            None => mirror(&format!(
                                                "rpc error, nothing to hold: {reason}"
                                            )),
                                        }
                                    }
                                }
                            }
                        } else if is_our_prompt {
                            attempts = 0;
                            *pending_reader.lock() = None;
                            let _ = write_state(&state_path, "idle", "", 0);
                        }
                    }
                    other => {
                        let kind = other.unwrap_or("?");
                        match kind {
                            "turn_start" => {
                                turn_active = true;
                                // A turn just started, whoever relayed the prompt that opened it (the pane's
                                // input, the daemon's mailbox, a resume): the seat is WORKING, and a state
                                // file that reads `idle` here is the lie the refused send was blamed on - a
                                // sender told the text was taken while the seat was mid-turn and busy.
                                let _ = write_state(&state_path, "working", "", attempts);
                            }
                            "turn_end" => {
                                turn_active = false;
                                // The seat is free again: a text this runner held for the next turn is
                                // delivered now rather than kept waiting for a turn that has ended.
                                deliver_held(
                                    &held_reader,
                                    &pending_reader,
                                    &to_child_reader,
                                    &state_path,
                                    &state_dir_reader,
                                    &target_reader,
                                    attempts,
                                );
                            }
                            // omp's own bounded provider retry (`P:180-250`): log it, and do NOT stack a
                            // second retry on top — if it succeeds the prompt response is a success.
                            "auto_retry_start" => {
                                agent_retrying = true;
                                mirror("provider retry in progress (omp's own)");
                                let _ = write_state(&state_path, "retrying", "omp auto-retry", attempts);
                            }
                            "auto_retry_end" => {
                                agent_retrying = false;
                                mirror("provider retry finished");
                            }
                            // A real prompt, and the one place `needs_input` belongs. The pane cannot
                            // answer a dialog (its input is a prompt), so it is cancelled the way the
                            // reference cancels what it cannot present (`P:434-438`).
                            "extension_ui_request" => {
                                let _ = write_state(&state_path, "needs_input", "a dialog this pane cannot answer", attempts);
                                mirror("a dialog arrived; this pane cannot answer it, cancelling it for the agent");
                                if let Some(id) = record.get("id").and_then(|v| v.as_str()) {
                                    let _ = send(
                                        &to_child_reader,
                                        &serde_json::json!({
                                            "id": id,
                                            "type": "extension_ui_response",
                                            "cancelled": true
                                        }),
                                    );
                                }
                            }
                            "agent_end" | "session_settled" => {
                                deliver_held(
                                    &held_reader,
                                    &pending_reader,
                                    &to_child_reader,
                                    &state_path,
                                    &state_dir_reader,
                                    &target_reader,
                                    attempts,
                                );
                                // `idle` is a seat at its prompt, and this arm may have just handed the held
                                // text back: a prompt in flight — or a text still waiting for the next turn —
                                // is not that, so only a seat with neither reads as idle here.
                                if pending_reader.lock().is_some() || !held_reader.lock().is_empty() {
                                    let _ = write_state(&state_path, "working", "", attempts);
                                } else {
                                    let _ = write_state(&state_path, "idle", "", attempts);
                                }
                            }
                            _ => {}
                        }
                        if let Some(delta) = record
                            .get("assistantMessageEvent")
                            .and_then(|e| e.get("delta"))
                            .and_then(|d| d.as_str())
                        {
                            // Assistant prose is the one payload an operator reads as text rather than
                            // as an event name (`P:204-208`).
                            print!("{delta}");
                            let _ = std::io::stdout().flush();
                        } else {
                            mirror(kind);
                        }
                    }
                }
            }
            // The child's stdout ended. That is its own fact — the reader is the only party that can tell
            // an EOF from a sealed pipe or an exit (`SPEC` §3) — so it is published, not just mirrored.
            let _ = write_state(&state_path, "agent_stdio_eof", "the child's stdout ended", attempts);
            if !announced_ready {
                let _ = ready_tx.send(Err("the child's stdout ended before the ready frame".into()));
            }
        });
        ready_rx
            .recv_timeout(Duration::from_secs(30))
            .map_err(|_| "the agent did not announce readiness within 30s".to_string())?
    };
    ready?;

    // The handshake done, ask for the state the pane and the daemon both need (`P:299`).
    send(&to_child, &serde_json::json!({"id": "runner-get-state", "type": "get_state"}))?;

    let pane = spawn_pane_input(to_child.clone(), Arc::clone(&pending));
    let exit = wait_child(&mut child);
    let _ = pane;
    let _ = writer;
    match exit {
        Ok(code) => {
            mirror(&format!("EXITED {code}"));
            // The last writer wins and the most specific fact is the exit. It is written on the main thread
            // *after* the child is reaped, so it cannot lose a race with this process ending — which is what
            // makes "the seat is not-running with a reason" true for a reader that arrives afterwards.
            let _ = write_state(&state_path, "agent_exited", &format!("agent_exited({code})"), 0);
            Ok(())
        }
        Err(e) => {
            // A child killed by a signal, or one whose wait itself failed, has still *left*: publish it,
            // because a seat the transport killed must never be readable as healthy afterwards.
            mirror(&format!("EXITED {e}"));
            let _ = write_state(&state_path, "agent_exited", &format!("agent_exited({e})"), 0);
            Err(e)
        }
    }
}

/// Ends the translated line stream. It is not JSON, so a frame can never be mistaken for it.
const AGENT_EOF: &str = "\u{0}eof";

/// The lines the runner's reader consumes. omp's stdout is already the runner's protocol. Claude Code's is
/// translated frame by frame, merged with the answers the writer side produces, and announced `ready` the
/// moment it is spawned (see `claude.rs` for why there is nothing earlier to wait for).
fn agent_lines(
    stdout: std::process::ChildStdout,
    bridge: Option<Arc<claude::Bridge>>,
    reader_tx: Sender<String>,
    reader_rx: mpsc::Receiver<String>,
) -> Box<dyn Iterator<Item = std::io::Result<String>> + Send> {
    let Some(bridge) = bridge else {
        return Box::new(BufReader::new(stdout).lines());
    };
    let _ = reader_tx.send(serde_json::json!({"type": "ready"}).to_string());
    thread::spawn(move || {
        for line in BufReader::new(stdout).lines() {
            let Ok(line) = line else { break };
            match serde_json::from_str::<serde_json::Value>(line.trim()) {
                Ok(record) => {
                    for frame in bridge.from_agent(&record) {
                        let _ = reader_tx.send(frame.to_string());
                    }
                }
                // Not a frame: the child is writing something human-readable; the reader mirrors it.
                Err(_) => {
                    let _ = reader_tx.send(line);
                }
            }
        }
        let _ = reader_tx.send(AGENT_EOF.to_string());
    });
    Box::new(std::iter::from_fn(move || match reader_rx.recv() {
        Ok(line) if line == AGENT_EOF => None,
        Ok(line) => Some(Ok(line)),
        Err(_) => None,
    }))
}

/// Serve the daemon's mailbox: a frame arrives, it is relayed to the child, and a frame that carried a
/// `replyTo` is answered exactly once — "a local write succeeded" is not the fact "the frame was relayed".
fn spawn_mailbox(
    path: PathBuf,
    to_child: Sender<String>,
    state_dir: PathBuf,
    target: RunnerTarget,
    pending: Arc<Mutex<Option<PendingPrompt>>>,
) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| format!("cannot create run dir: {e}"))?;
    }
    let _ = fs::remove_file(&path);
    let listener =
        UnixListener::bind(&path).map_err(|e| format!("cannot bind mailbox {}: {e}", path.display()))?;
    info!("runner mailbox listening at {}", path.display());
    thread::spawn(move || {
        for stream in listener.incoming() {
            let stream = match stream {
                Ok(s) => s,
                Err(e) => {
                    warn!("runner mailbox accept error: {e}");
                    continue;
                }
            };
            serve_mailbox_stream(stream, &to_child, &state_dir, &target, &pending);
        }
    });
    Ok(())
}

/// One connection: read whole frames, relay each, answer if asked.
///
/// The reader bound is the contract's (`frame::read_line_bounded`): a peer that writes a line past
/// `MAX_FRAME_BYTES` ends the connection with `framing_error` instead of making this process allocate
/// without bound.
fn serve_mailbox_stream(
    stream: UnixStream,
    to_child: &Sender<String>,
    state_dir: &Path,
    target: &RunnerTarget,
    pending: &Arc<Mutex<Option<PendingPrompt>>>,
) {
    let mut reader = BufReader::new(stream);
    let state_path = state_file_path(state_dir, &target.room, &target.seat);
    loop {
        let line = match frame::read_line_bounded(&mut reader, frame::MAX_FRAME_BYTES) {
            Ok(l) if l.is_empty() => return,
            Ok(l) => l,
            Err(e) => {
                // Both a framing failure and a dead socket end this connection; the caller learns the
                // difference from its own reply socket timing out, never from a guessed success.
                warn!("runner mailbox: {e}");
                return;
            }
        };
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        let value: serde_json::Value = match serde_json::from_str(trimmed) {
            Ok(v) => v,
            Err(e) => {
                mirror(&format!("ERROR mailbox: {e}"));
                continue;
            }
        };
        let id = value
            .get("id")
            .and_then(|v| v.as_str())
            .unwrap_or("runner-frame")
            .to_string();
        let reply_to = value.get("replyTo").and_then(|v| v.as_str()).map(str::to_string);
        let kind = value
            .get("type")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let mut outcome = relay_frame(&value, &kind, &id, to_child, &state_path, target, pending);
        // "The channel is closed" is true but not *which* death it was. The runner publishes that fact in
        // its state file (`SPEC` §3), so the answer a caller gets and the file a reader finds agree, and
        // neither side has to infer a cause from an error string.
        if let Err(ref e) = outcome {
            if e.contains("channel is closed") {
                let published = fs::read_to_string(&state_path)
                    .ok()
                    .and_then(|t| serde_json::from_str::<serde_json::Value>(&t).ok())
                    .and_then(|v| v.get("state").and_then(|s| s.as_str()).map(str::to_string));
                if let Some(state) = published {
                    if matches!(
                        state.as_str(),
                        "agent_socket_closed" | "agent_stdio_eof" | "agent_exited"
                    ) {
                        outcome = Err(state);
                    }
                }
            }
        }
        match (reply_to, outcome) {
            (Some(reply_to), outcome) => {
                if let Err(e) = frame::reply(&reply_to, &id, outcome) {
                    warn!("runner mailbox: {e}");
                }
            }
            (None, Ok(_)) => {}
            (None, Err(e)) => mirror(&format!("ERROR mailbox: {e}")),
        }
    }
}

/// Relay one daemon frame to the child, or refuse it by name.
///
/// The relay's verb vocabulary is the spec's (§1): `compact` (landed), `agent` (the daemon's channel to the
/// agent without pasting into a pane), `text` (that verb's sugar, capped in the pane's own words) and
/// `status` (the read side of the state file, so no caller parses a file it does not own). An unknown
/// `type` is reported — `unsupported_type` to the caller when one is waiting, and the pane line the
/// reference printed when none is — because an invisible refusal is the same "the surface can lie" shape as
/// a stub that always succeeds.
fn relay_frame(
    value: &serde_json::Value,
    kind: &str,
    id: &str,
    to_child: &Sender<String>,
    state_path: &Path,
    target: &RunnerTarget,
    pending: &Arc<Mutex<Option<PendingPrompt>>>,
) -> Result<serde_json::Value, String> {
    match kind {
        "compact" => {
            send(to_child, &serde_json::json!({"id": id, "type": "compact"}))?;
            mirror(&format!("compact relayed to the agent (id {id})"));
            Ok(serde_json::json!({"relayed": "compact"}))
        }
        "agent" => {
            let cmd = value
                .get("cmd")
                .filter(|c| c.is_object())
                .ok_or_else(|| "the agent verb needs a cmd object".to_string())?;
            // Verbatim: one line, no field added, so the daemon's frame is what the agent reads.
            send(to_child, cmd)?;
            Ok(serde_json::json!({"relayed": "agent"}))
        }
        "text" => {
            let text = value
                .get("text")
                .and_then(|v| v.as_str())
                .ok_or_else(|| "the text verb needs a text string".to_string())?;
            // The pane's own cap and wording: what the agent's channel refuses is what the operator sees.
            if text.len() > MAX_PANE_INPUT_BYTES {
                return Err(PANE_INPUT_REJECTED.to_string());
            }
            send(
                to_child,
                &serde_json::json!({"id": id, "type": "prompt", "message": text}),
            )?;
            // Remember the prompt where the pane's own input remembers its own (see `spawn_pane_input`):
            // its response is the one this runner owns, and without the record a refusal of it answered
            // "nothing to resume" while the text was gone — the send that printed a delivery for text the
            // agent had refused.
            //
            // THE FRAME'S ORIGIN IS REMEMBERED WITH IT (`from`, `verb`), because this is the last moment the
            // sender is known: a text that is later held, handed back or given up on has to name whose it
            // was, and the response that reports the refusal carries an id and nothing else.
            *pending.lock() = Some(PendingPrompt::relayed(
                id,
                text,
                Origin::from_frame(value),
            ));
            // THE TURN IS PUBLISHED WHEN IT STARTS, not when it ends: a prompt handed to the agent is work
            // in flight, and a state file left at `idle` is how a mid-turn seat read as a seat at its
            // prompt while it was busy (`SPEC` §3).
            let _ = write_state(state_path, "working", "", 0);
            Ok(serde_json::json!({"relayed": "prompt"}))
        }
        "status" => {
            let text = fs::read_to_string(state_path).map_err(|e| {
                format!(
                    "no state is published for {}/{} ({e})",
                    target.room, target.seat
                )
            })?;
            let doc: serde_json::Value =
                serde_json::from_str(&text).map_err(|e| format!("unreadable state file: {e}"))?;
            Ok(doc)
        }
        other => {
            mirror(&format!("ERROR mailbox: unknown frame {other:?}"));
            Err("unsupported_type".to_string())
        }
    }
}

/// The seat a session name addresses, or the `<room>/<seat>` spec itself.
///
/// The IPC surface names seats by the session the supervisor derives (`agenthub--<room>--<seat>`); the
/// runner's own surface uses the spec. One spelling, so the two cannot drift.
pub fn target_from_session(session_or_target: &str) -> Result<RunnerTarget, String> {
    match session_or_target.split_once('/') {
        Some(_) => RunnerTarget::parse(session_or_target),
        None => {
            let rest = session_or_target.strip_prefix("agenthub--").ok_or_else(|| {
                format!(
                    "'{session_or_target}' is neither <room>/<seat> nor the session name \
                     agenthub--<room>--<seat>"
                )
            })?;
            let (room, seat) = rest
                .rsplit_once("--")
                .ok_or_else(|| format!("'{session_or_target}' does not name a room and a seat"))?;
            RunnerTarget::parse(&format!("{room}/{seat}"))
        }
    }
}

/// Relay one daemon frame into a seat's runner, spilling the payload when the encoded line reaches
/// `MAX_FRAME_BYTES`.
///
/// The daemon is the side holding the bytes, and the side a >1 MiB message would be refused by, so the
/// daemon writes the spill and the frame carries the handle (`SPEC §2`). With `want_reply`, the daemon binds
/// a socket, hands its path to the runner as `replyTo`, and waits for the runner's one line: the caller then
/// has the runner's own statement that the frame was relayed, which is the fact a local write cannot give.
/// A reply that is a `payload` frame is read back through `read_spill`, so the digest is verified before the
/// bytes are trusted.
pub fn send_to_seat(
    state_dir: &Path,
    session_or_target: &str,
    frame: &serde_json::Value,
    payload: Option<(&[u8], bool)>,
    want_reply: bool,
) -> Result<serde_json::Value, String> {
    let target = target_from_session(session_or_target)?;
    let mailbox = target.mailbox(state_dir);
    let mut stream = UnixStream::connect(&mailbox).map_err(|e| {
        format!(
            "no runner for {}/{}: cannot reach its mailbox {} ({e})",
            target.room,
            target.seat,
            mailbox.display()
        )
    })?;
    let id = frame
        .get("id")
        .and_then(|v| v.as_str())
        .unwrap_or("daemon-frame")
        .to_string();

    let reply_listener = if want_reply {
        let path = state_dir
            .join("rooms")
            .join(&target.room)
            .join("run")
            .join(format!("{}.reply-{}.sock", target.seat, std::process::id()));
        let _ = fs::remove_file(&path);
        Some((UnixListener::bind(&path).map_err(|e| {
            format!("cannot bind a reply socket {}: {e}", path.display())
        })?, path))
    } else {
        None
    };

    // One frame, measured on its encoded line: under the limit it goes as it is, at or above it the payload
    // becomes a file and the frame carries the handle.
    let mut to_send = frame.clone();
    if let Some(listener) = &reply_listener {
        to_send["replyTo"] = serde_json::Value::String(listener.1.to_string_lossy().into_owned());
    }
    let encoded = frame::encode_line(&to_send)?;
    let line = if encoded.len() < frame::MAX_FRAME_BYTES {
        (encoded, frame::Sent::Data)
    } else {
        let (bytes, utf8) = payload.ok_or_else(|| {
            format!(
                "frame {id} encodes to {} bytes with no payload to spill",
                encoded.len()
            )
        })?;
        frame::frame_or_spill(
            &to_send,
            bytes,
            utf8,
            &SpillTarget::Seat {
                room: target.room.clone(),
                seat: target.seat.clone(),
            }
            .dir(state_dir),
            &frame::next_spill_name(),
        )?
    };
    frame::write_sent(&mut stream, &line.0, &line.1)?;
    let _ = gc_after_spill(state_dir, &target, &line.1);

    match reply_listener {
        None => Ok(serde_json::json!({"id": id, "ok": true})),
        Some((listener, path)) => {
            listener
                .set_nonblocking(false)
                .map_err(|e| format!("cannot wait on the reply socket: {e}"))?;
            let (stream, _) = listener
                .accept()
                .map_err(|e| format!("no answer from the runner for {}/{}: {e}", target.room, target.seat))?;
            let mut reader = BufReader::new(stream);
            let line = frame::read_line_bounded(&mut reader, frame::MAX_FRAME_BYTES)?;
            let _ = fs::remove_file(&path);
            let answered: serde_json::Value = serde_json::from_str(line.trim())
                .map_err(|e| format!("the runner's answer is not a frame: {e}"))?;
            if answered.get("type").and_then(|v| v.as_str()) == Some("payload") {
                if let Some(handle) = answered.get("handle") {
                    let bytes = frame::read_spill(handle)?;
                    return Ok(serde_json::json!({
                        "id": id,
                        "ok": true,
                        "spilled": handle,
                        "bytes": bytes.len()
                    }));
                }
            }
            Ok(answered)
        }
    }
}

/// Keep a room's spill tree inside its budget, opportunistically, on the side that wrote a spill.
fn gc_after_spill(state_dir: &Path, target: &RunnerTarget, sent: &frame::Sent) -> Result<(), String> {
    if !matches!(sent, frame::Sent::Spilled { .. }) {
        return Ok(());
    }
    for dir in [
        SpillTarget::Seat { room: target.room.clone(), seat: target.seat.clone() }.dir(state_dir),
        SpillTarget::Room { room: target.room.clone() }.dir(state_dir),
    ] {
        frame::gc_spills(&dir, frame::SPILL_BUDGET_BYTES)?;
    }
    Ok(())
}

/// The id prefix of a compaction frame relayed to a seat. Its reply is not a turn: an agent with nothing yet
/// to compact answers `Nothing to compact (session too small)`, and that must not read as a refused turn.
const COMPACT_ID_PREFIX: &str = "forcecompact-";

/// Whether a response id belongs to a compaction frame this runner relayed.
///
/// A `compact` is fire-and-forget (`request_compact` sends it with no `replyTo`), so its reply answers a
/// *request* and can never be a turn's outcome, whatever it says — including a transport error, which the
/// ladder would otherwise refuse as "a retryable failure with no turn to resume" and block the seat for.
pub fn is_compact_reply(id: &str) -> bool {
    id.starts_with(COMPACT_ID_PREFIX)
}

/// Ask a running seat's runner to relay a compaction frame to its agent.
///
/// This is what replaces the `pidfd_getfd` theft: the daemon writes one JSON line into the seat's
/// mailbox socket and the runner — the process that owns the child's channel — relays it. A seat with no
/// runner is a refusal naming the seat, not a silent success.
pub fn request_compact(state_dir: &Path, session_or_target: &str) -> Result<String, String> {
    let target = target_from_session(session_or_target)?;
    let id = format!("{COMPACT_ID_PREFIX}{}", std::process::id());
    // Fire-and-forget, which is what compact's callers already had: no `replyTo`, so the relay is a write
    // into the seat's mailbox and nothing more. It goes through the same sender the daemon uses for anything
    // else it has to say to a seat, so the fallback exit cannot drift into a second copy of it.
    send_to_seat(
        state_dir,
        session_or_target,
        &serde_json::json!({"id": id, "type": "compact"}),
        None,
        false,
    )?;
    Ok(format!(
        "{}/{}: compact relayed to its runner (id {id})",
        target.room, target.seat
    ))
}

/// What `--approval-mode` an omp seat is launched with, from the seat's documented permission policy.
///
/// The vocabulary is the operator's own (`docs/AGENT-SETUP.md:389`: `locked|standard|open|yolo|none`, where
/// "`yolo` means the seat runs unattended with no approval prompts"), and the value the client actually
/// materialises is `builtin:yolo` (`internal/clientsync/rigverb.go:62`), whose comment gives the reason:
/// without a full-bypass launch "omp's always-ask floor cancels approvals headlessly and the seat parks".
/// `internal/clientpolicy/policy.go` states the same shape from the runner's side: "the rig orchestrator launches an omp
/// seat in exactly one of two postures - yolo, or always-ask which the managed runner auto-cancels".
///
/// So the unattended posture belongs to `yolo`, to the materialised `builtin:yolo`, and to an **omitted**
/// policy — the last as the documented default, not as a description of the live rig: the reviewer measured
/// that the live rig's own seat specs carry `permission_policy: builtin:yolo` **explicitly**
/// (`the room spec directory/rig-*.yaml`). Both spellings map to the same launch posture,
/// and an **empty string is refused by name** above, because a blank field is a config accident far more
/// often than a stated intent.
/// The four restrictive words take `always-ask`, whose dialogs this runner does not leave hanging: a dialog
/// it cannot present is answered cancelled (see the reader), which is the "auto-cancels" half of that
/// sentence. Anything else is refused by name, because guessing a launch posture is what inverted this
/// mapping in the first place.
pub fn approval_mode(policy: Option<&str>) -> Result<&'static str, String> {
    // An OMITTED field takes the documented unattended default, and a PRESENT but blank one is refused by
    // name (the lead's ruling): a blank value is far likelier to be a config accident than a stated intent,
    // and "blank means unattended" is the shape of an approval nobody meant to give. The two used to
    // collapse - a `String` with `#[serde(default)]` turned absence into "" - so the refusal swallowed the
    // default. The type carries the difference now, so this function can honour both rules.
    let Some(policy) = policy else {
        return Ok("yolo");
    };
    if policy.trim().is_empty() {
        return Err(
            "permission policy is empty; refusing to guess a launch posture. Use one of \
             locked|standard|open|yolo|none (or builtin:yolo), or omit the field to take the documented \
             unattended default"
                .to_string(),
        );
    }
    match policy.trim() {
        "yolo" | "builtin:yolo" => Ok("yolo"),
        "locked" | "standard" | "open" | "none" => Ok("always-ask"),
        other => Err(format!(
            "permission policy {other:?} is not one of locked|standard|open|yolo|none (nor builtin:yolo, \
             nor empty); refusing to guess a launch posture"
        )),
    }
}

/// Where a seat's own state is published. One spelling for the runner that writes it and the supervisor that
/// waits for it, so a second path cannot drift into existence.
pub fn state_file_path(state_dir: &Path, room: &str, seat: &str) -> PathBuf {
    state_dir
        .join("rooms")
        .join(room)
        .join("run")
        .join(format!("{seat}.state.json"))
}

/// What a failure this runner sees *is*, so a dead turn is not mistaken for a prompt.
///
/// The defect this replaces is named and measured: the rig orchestrator activity store maps an omp
/// `Notification`/`runtime_error` to `needs_input` (`agent-activity-store.js:262-265`), and `needs_input`
/// is the one state that fails closed (`session-transport.ts:1893-1900`) — so a dead turn's own error
/// report refuses the wakes it needs. Here a runtime error gets its own retryable classification, and
/// `needs_input` is reserved for a real prompt. The same finding measured that an RPC error does **not**
/// always kill the turn, so the resume keys on turn state, never on the presence of an error line.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Failure {
    /// The turn died on something a retry can plausibly fix (provider, transport, socket).
    Retryable(&'static str),
    /// The turn ended on something a retry cannot fix; a human or another seat must look.
    Terminal(&'static str),
    /// The agent declined a *request*, and no turn died: nothing is published and nothing is re-sent.
    ///
    /// Measured (2026-10-14, live room `4genthub-min`): a `compact` relayed to a seat with no turns yet is
    /// answered `Nothing to compact (session too small)`. Read as a refused turn it published `blocked`,
    /// and because the nudge loop wakes only `idle` seats, go-dev, fe-dev and reviewer were never handed a
    /// row — a healthy seat, killed by its own health report.
    Noop(&'static str),
}

/// The measured refusals that are NOT failures of a turn: omp's own answer when asked to compact a session
/// with nothing in it yet. Both halves of the message omp actually sends — `Nothing to compact (session
/// too small)` — are here, so a reworded half cannot quietly become a refused turn again.
pub const NOOP_SIGNATURES: [(&str, &str); 2] = [
    ("Nothing to compact", "omp has nothing to compact yet"),
    ("session too small", "the session is too small to compact"),
];

/// The measured signatures of a transport/provider failure (`FINDING-omp-runner-death-pattern`, and the
/// scout's map of `pi-runner.js`): these two are omp's own rpc errors, the third is its socket close.
pub const RETRYABLE_SIGNATURES: [(&str, &str); 8] = [
    ("RPC response exceeded the transport limit", "omp's rpc response cap"),
    ("rpc chunk payload exceeds the transport limit", "omp's rpc chunk cap"),
    ("The socket connection was closed unexpectedly", "the agent socket closed"),
    ("rate limit", "provider rate limit"),
    ("overloaded", "provider overloaded"),
    ("timed out", "timeout"),
    ("ECONNRESET", "connection reset"),
    ("fetch failed", "provider fetch failed"),
];

pub fn classify_failure(error: &str) -> Failure {
    // A refusal to do something that was itself optional is not a dead turn, and is checked first: it must
    // never reach the ladder that publishes `blocked`. A response that carried no id this runner knows is
    // the measured shape — `is_compact_reply` catches the replies that do carry one, and this catches the
    // ones omp answers without it.
    for (needle, label) in NOOP_SIGNATURES {
        if error.contains(needle) {
            return Failure::Noop(label);
        }
    }
    for (needle, label) in RETRYABLE_SIGNATURES {
        if error.contains(needle) {
            return Failure::Retryable(label);
        }
    }
    Failure::Terminal("the agent refused the turn")
}

/// How many times this runner re-sends a turn that died retryably, and how long it waits between tries.
pub const MAX_RESUME_ATTEMPTS: u32 = 3;

/// How many refused texts this runner keeps for the seat's next free turn.
///
/// EIGHT, and the number is the point rather than a round figure: the queue exists for a burst (a sender's
/// few messages landing inside one long turn), not for a backlog. A seat whose agent refuses prompt after
/// prompt is not going to get to the ninth either, and every held text is a message the seat has not seen -
/// so the bound turns an unbounded memory growth with an invisible cause into a published, named refusal
/// (`publish_hold`) that a reader can act on.
pub const MAX_HELD: usize = 8;

/// How many times ONE text is offered to the agent while it is held, before it goes back to its sender.
///
/// The bound this replaces was the resume ladder's own counter, and the sharing was the measured defect:
/// `deliver_held` incremented the SAME counter the ladder counts resume attempts in and compared the total
/// against `MAX_RESUME_ATTEMPTS`, so a text refused once, after three unrelated attempts on that seat, was
/// dropped on its FIRST delivery with its sender told nothing - 19 lines counted at 23:14Z in the window that
/// began 22:00Z on 2026-10-16, 13 of them addressed to the lead seat (the window is open-ended: a recount at
/// 23:41Z reads 33, 17 the lead's). A text's bound has to be its own, and reaching it has to be a RETURN
/// rather than a hole.
pub const MAX_HELD_ATTEMPTS: u32 = 3;

/// Who sent a text, and which verb carried it.
///
/// The pair every refusal names: a runner that cannot deliver a text must say whose it was and which path
/// brought it, or the sender reads a successful relay and never learns the text died - the silence that made
/// fe-dev's three lost sends to the lead seat a mystery on 2026-10-16. `from` is a session name whenever a
/// session sent it, which is also what makes the text handable BACK (`Origin::sender_session`).
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize)]
pub struct Origin {
    /// The sender's session name (`agenthub--<room>--<seat>`), or this runtime's own word for a path with no
    /// seat on the other end (the rail's nudge, the pane).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub from: Option<String>,
    /// The verb that carried the frame: `send`, `nudge`, `pane`, `return`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub verb: Option<String>,
}

impl Origin {
    /// The origin a frame declares (`from`, `verb`). Absent fields stay absent: a sender this runtime was not
    /// told about is named as unnamed rather than guessed at. An empty string is absent too - a caller with no
    /// session of its own sends no `from`, and an empty one would otherwise name nothing at all.
    pub fn from_frame(value: &serde_json::Value) -> Self {
        let field = |key: &str| {
            value
                .get(key)
                .and_then(|v| v.as_str())
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .map(str::to_string)
        };
        Self {
            from: field("from"),
            verb: field("verb"),
        }
    }

    /// The origin in the words a mirror line, a state classification or a frame back to the sender uses.
    pub fn label(&self) -> String {
        match (self.from.as_deref(), self.verb.as_deref()) {
            (Some(from), Some(verb)) => format!("{from} ({verb})"),
            (Some(from), None) => from.to_string(),
            (None, Some(verb)) => format!("an unnamed sender ({verb})"),
            (None, None) => "an unnamed sender".to_string(),
        }
    }

    /// The session a text can be handed back to, when this runtime can address it at all.
    pub fn sender_session(&self) -> Option<&str> {
        self.from
            .as_deref()
            .filter(|from| target_from_session(from).is_ok())
    }
}

/// The prompt this runner has in the agent's hands: its id, its text, and where it came from.
///
/// The origin rides with the prompt because the two places a prompt can die - the ladder's give-up and the
/// held queue's bound - are reached from a RESPONSE, where the only other sight of the text is this slot.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PendingPrompt {
    pub id: String,
    pub text: String,
    pub origin: Origin,
    /// How many times this text has already been offered to the agent while held (0 for a fresh prompt).
    pub held_attempts: u32,
}

impl PendingPrompt {
    /// A prompt this runner relays for a caller: fresh, so its held count starts at zero.
    pub fn relayed(id: &str, text: &str, origin: Origin) -> Self {
        Self {
            id: id.to_string(),
            text: text.to_string(),
            origin,
            held_attempts: 0,
        }
    }
}

/// A text the agent refused while its turn was running, waiting for the seat to be free again.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct HeldText {
    /// The id of the prompt whose refusal left this text here, so a reader can follow it back.
    pub refused_id: String,
    pub text: String,
    pub origin: Origin,
    /// Offers already spent on THIS text, `MAX_HELD_ATTEMPTS` being the bound. It travels with the entry
    /// through every requeue: a count that resets each turn is not a bound.
    pub attempts: u32,
}

pub fn resume_delay(attempt: u32) -> Duration {
    // 1s, 2s, 4s — bounded, and the same shape for every retryable class.
    Duration::from_secs(1u64 << attempt.min(3).saturating_sub(1).min(3))
}

/// How many times the daemon asks a seat to pick its rail work up, and how long it waits between tries.
///
/// `nudge_delay` mirrors `resume_delay`'s shape exactly - a bounded ladder, clamped past the bound - for
/// the same reason: a retry that grows without a ceiling is a spin. Bounded at three because a fourth
/// identical request to a seat that has answered nothing says nothing new; what remains then is the alert
/// on the room status, which is the signal a human reads.
pub const MAX_NUDGE_ATTEMPTS: u32 = 3;

pub fn nudge_delay(attempt: u32) -> Duration {
    // 1s, 2s, 4s - the same shape as `resume_delay`, so an operator who knows one ladder knows the other.
    Duration::from_secs(1u64 << attempt.min(3).saturating_sub(1).min(3))
}

/// The identity of the work a nudge is about: the seat and the rows waiting for it, sorted, so the same
/// pile is the same nudge however the rail listed it.
///
/// Keying the ladder on this is what re-arms it: a seat that finished its rows and was handed a
/// *different* pile is nudged again, while an unchanged pile that has been asked three times is left to
/// the alert. Without a key, either every new row would be silent (the bound never resetting) or a
/// restarted daemon would ask a fourth time (the count living in memory).
pub fn nudge_key(seat: &str, qitem_ids: &[String]) -> String {
    let mut ids: Vec<&str> = qitem_ids.iter().map(String::as_str).collect();
    ids.sort_unstable();
    format!("{seat}:{}", ids.join(","))
}

/// Where nudge attempts are recorded: `<state dir>/rooms/<room>/run/<seat>.nudge.jsonl`.
///
/// The home, and why: the room's `run/` dir is already where this seat's durable facts live - its
/// `state.json` and its mailbox `.sock` - so one reader looking for "what happened to this seat" looks in
/// one place. The state file itself cannot hold this: `write_state` publishes the *current* state and
/// rewrites on every publish, so a ladder of attempts would collapse into its last line. Append-only is
/// the point: an attempt log is a history, and the difference between "attempted once and got nowhere"
/// and "attempted three times" has to survive being read.
pub fn nudge_log_path(state_dir: &Path, room: &str, seat: &str) -> PathBuf {
    state_dir
        .join("rooms")
        .join(room)
        .join("run")
        .join(format!("{seat}.nudge.jsonl"))
}

/// One nudge attempt as the log records it, successful or not - one line per attempt, nothing overwritten.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct NudgeAttempt {
    pub ts: String,
    pub room: String,
    pub seat: String,
    /// The waiting pile this attempt belongs to (`nudge_key`), so the count is per pile.
    pub key: String,
    pub attempt: u32,
    pub max_attempts: u32,
    pub session_name: String,
    pub qitem_ids: Vec<String>,
    /// What became of the attempt: the agent-channel frame the seat's runner reported it relayed
    /// (`prompt`), or the failure's own words. "Attempted" and "delivered" are different records.
    pub outcome: String,
}

/// How far up the ladder this waiting pile has already been asked, read back from the durable log.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NudgeProgress {
    /// The highest attempt number recorded for this pile; 0 means it has never been asked.
    pub attempts: u32,
    /// When the most recent attempt was recorded, when its record carried a readable stamp.
    pub last_ts: Option<DateTime<Utc>>,
}

/// Reading the log rather than keeping a counter in memory is what makes the ladder survive a daemon
/// restart: the durable file is the state, so a restarted daemon resumes the third attempt instead of
/// starting a fresh first one against a seat that has already been asked twice.
pub fn nudge_progress(state_dir: &Path, room: &str, seat: &str, key: &str) -> NudgeProgress {
    let Ok(text) = fs::read_to_string(nudge_log_path(state_dir, room, seat)) else {
        return NudgeProgress::default();
    };
    let mut progress = NudgeProgress::default();
    for line in text.lines() {
        // A line this build cannot read is skipped, not fatal: the log is append-only and a foreign or
        // half-written line must not stop the ladder. It cannot weaken the bound - `attempts` only grows.
        let Ok(record) = serde_json::from_str::<NudgeAttempt>(line) else {
            continue;
        };
        if record.key != key {
            continue;
        }
        progress.attempts = progress.attempts.max(record.attempt);
        progress.last_ts = DateTime::parse_from_rfc3339(&record.ts)
            .ok()
            .map(|t| t.with_timezone(&Utc));
    }
    progress
}

/// Append one attempt record. Never truncates: the ladder's history is the point.
fn append_nudge_attempt(state_dir: &Path, attempt: &NudgeAttempt) -> Result<(), String> {
    let path = nudge_log_path(state_dir, &attempt.room, &attempt.seat);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| format!("cannot create run dir: {e}"))?;
    }
    let mut line =
        serde_json::to_string(attempt).map_err(|e| format!("cannot encode nudge attempt: {e}"))?;
    line.push('\n');
    let mut file = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&path)
        .map_err(|e| format!("cannot open the nudge log {}: {e}", path.display()))?;
    file.write_all(line.as_bytes())
        .map_err(|e| format!("cannot append to the nudge log {}: {e}", path.display()))
}

/// What the woken seat is told. It names the rows, so the agent reads the pile off the rail itself and
/// nobody has to guess which work the wake was about.
pub fn nudge_text(room: &str, seat: &str, qitem_ids: &[String], attempt: u32) -> String {
    format!(
        "[rail] {seat}: {} row(s) on room {room}'s rail are waiting for you: {}. Read each with \
         `4genteam room queue show <qitem_id>` and claim what you will do. (wake {attempt}/{MAX_NUDGE_ATTEMPTS})",
        qitem_ids.len(),
        qitem_ids.join(", ")
    )
}

/// One nudge: ask a seat's runner to hand its agent the prompt that its waiting rail work is about.
///
/// The frame is the runner's own `text` verb - the frame `send_to_seat` already speaks - which
/// `relay_frame` turns into the agent's `prompt` frame on the child channel. That channel is why this is
/// the right mechanism and `pty::TerminalManager::send_text` is not: `send_text` pastes into the tmux
/// pane, and the pane's input is the runner's own prompt reader (the pane's keystrokes are the human at
/// the terminal), so a nudge sent that way is typed in the runner's name into whatever the pane's input
/// happens to be - it does not carry the rail's identity to the agent, which is what the seat needs to
/// act on it.
///
/// `want_reply` is true on purpose: the recorded outcome is the runner's own answer that it relayed the
/// frame, never "a write returned". A write into a socket no runner is reading is not a delivery, and the
/// attempt log must not confuse the two.
pub fn nudge_seat(
    state_dir: &Path,
    room: &str,
    seat: &str,
    session_name: &str,
    qitem_ids: &[String],
    attempt: u32,
) -> Result<serde_json::Value, String> {
    // A PARKED SEAT IS NOT NUDGED, and the refusal comes before the attempt log: the ladder's state is that
    // durable log, so a parked seat must not spend attempts of it. The rail wake is the runtime's own route to
    // a seat, which is exactly why it is gated here rather than at its caller.
    crate::park::refuse(state_dir, room, seat, "the rail prompt was not delivered")?;
    let text = nudge_text(room, seat, qitem_ids, attempt);
    let id = format!("nudge-{}-{attempt}", std::process::id());
    let outcome = send_to_seat(
        state_dir,
        session_name,
        &serde_json::json!({
            "id": id,
            "type": "text",
            // The nudge's origin is the rail, not a seat: nothing on the other end of a nudge is a session
            // this runtime can hand a text back to, so it is named for what it is (`4genteam send` spoke
            // the same frame above a seat, and `deliver_text` stamps its caller's session there).
            "from": format!("the rail of room {room}"),
            "verb": "nudge",
            "text": text,
        }),
        None,
        true,
    );
    let record = NudgeAttempt {
        ts: Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
        room: room.to_string(),
        seat: seat.to_string(),
        key: nudge_key(seat, qitem_ids),
        attempt,
        max_attempts: MAX_NUDGE_ATTEMPTS,
        session_name: session_name.to_string(),
        qitem_ids: qitem_ids.to_vec(),
        outcome: match &outcome {
            // `frame::reply` wraps the runner's answer as `{"id":..,"ok":true,"data":{...}}`, and
            // `relay_frame`'s `text` arm answers `{"relayed":"prompt"}` - the agent-channel frame the
            // prompt became. That word is what the log records.
            Ok(answer) => answer
                .pointer("/data/relayed")
                .and_then(|v| v.as_str())
                .unwrap_or("relayed")
                .to_string(),
            Err(e) => e.clone(),
        },
    };
    // Written whether the delivery worked or not: an attempt the daemon made and did not record would be
    // read afterwards as an attempt it never made, and the ladder's bound would be a lie.
    append_nudge_attempt(state_dir, &record)?;
    outcome
}

/// The seat's own state, written where the daemon and `ps` can read it (the reference writes a
/// `runner-state.json` beside the session for the same reason, `pi-runner-protocol.js:26`).
pub fn write_state(state_path: &Path, state: &str, classification: &str, attempts: u32) -> Result<(), String> {
    let payload = serde_json::json!({
        "state": state,
        // THE WORD, PUBLISHED WHERE THE STATE IS PUBLISHED, derived from the same string in the same call
        // so the two cannot drift. A reader that wants the daemon's vocabulary reads this instead of
        // deriving its own - a second derivation anywhere would be the second vocabulary this exists to
        // prevent - and the runtime's own status serves it unchanged.
        "activity": crate::types::ActivityWord::from_runner_state(state).as_str(),
        "classification": classification,
        "resume_attempts": attempts,
        // THE PUBLISHER'S PID, because a claim is only as good as the process that can still update it: without
        // it a reader cannot tell a seat that IS working from one that WAS working when its runner died. Measured
        // 2026-10-15: a seat whose agent had been killed out from under its pane kept the word `working` in this
        // file for 42 minutes, and nothing was left to correct it.
        "pid": std::process::id(),
        "updated_at": chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
    });
    if let Some(parent) = state_path.parent() {
        fs::create_dir_all(parent).map_err(|e| format!("cannot create run dir: {e}"))?;
    }
    let text = serde_json::to_string_pretty(&payload).map_err(|e| format!("cannot encode state: {e}"))?;
    let tmp = state_path.with_extension("json.tmp");
    fs::write(&tmp, text).map_err(|e| format!("cannot write state: {e}"))?;
    fs::rename(&tmp, state_path).map_err(|e| format!("cannot publish state: {e}"))
}

/// What to do about an rpc error, decided from the turn's state rather than from the error text alone.
///
/// Measured, and the reason this exists: an rpc error does **not** always kill the turn
/// (`FINDING-omp-runner-death-pattern`), so a mid-turn error must never start a resume — that would
/// double the work the live turn is already doing.
///
/// The second rule is the lead's follow-up to the gate, and it is the same lie this cutover exists to
/// remove: an error this runner classifies but does **not** own used to leave the seat published as
/// `idle` — a seat that looks healthy and is not. Ownership therefore follows the classification: if the
/// turn is not running, the seat is not healthy, whatever the response's id says. A retryable error with a
/// pending prompt is resumed even when the id did not line up (the correlation gap is logged, not hidden);
/// a retryable error with no turn to resume, and any terminal error, publishes `blocked`.
///
/// The third rule is the refused send (U3 run 1 and the live room, measured 2026-10-14): a prompt this
/// runner sent, refused by the agent because a turn of its own was already running, must not be DROPPED.
/// The refusal is terminal and the turn is live, so neither of the arms above is honest — resuming would
/// double the work the live turn is doing, and `Nothing` is the arm that loses the text. It is held for the
/// next turn instead, and the state file says so while it waits.
///
/// A no-op refusal publishes nothing at all — there was no turn to lose.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Resume {
    Nothing(&'static str),
    /// OUR OWN PROMPT WAS REFUSED WHILE ANOTHER TURN HOLDS THE AGENT: the text is neither dropped
    /// (`Nothing`) nor re-sent into the turn that refused it (`Retry`) - it is HELD and delivered when that
    /// turn ends.
    Hold(&'static str),
    Retry {
        attempt: u32,
        delay_secs: u64,
        label: &'static str,
    },
    Blocked(&'static str),
}

pub fn decide_resume(
    ours_by_id: bool,
    turn_active: bool,
    has_pending_prompt: bool,
    error: &str,
    failures_so_far: u32,
) -> Resume {
    let ours = ours_by_id || has_pending_prompt;
    if turn_active {
        // The live turn owns the error; the seat is healthy and nothing here may resume it. The one
        // exception is a prompt of OUR OWN that the agent refused outright: that turn will never take it,
        // so holding it for the next one is the only arm that neither drops the text nor doubles the work
        // the live turn is already doing. A retryable error is not that case — the text stays with the live
        // turn, which is the rule parity 1 is locked on.
        if ours && matches!(classify_failure(error), Failure::Terminal(_)) {
            return Resume::Hold("the agent refused this prompt while its turn was running");
        }
        return Resume::Nothing("the turn is still running");
    }
    match classify_failure(error) {
        Failure::Terminal(label) => Resume::Blocked(label),
        // A declined request, not a dead turn: nothing to resume and nothing to publish — the seat keeps
        // the state it had, which for a fresh seat is `idle`, so the nudge loop can still wake it.
        Failure::Noop(label) => Resume::Nothing(label),
        Failure::Retryable(label) => {
            if !ours {
                // Nothing to re-send, and a seat that is not running must not read as healthy: blocked,
                // named, rather than idle.
                return Resume::Blocked("a retryable failure with no turn to resume");
            }
            let attempt = failures_so_far + 1;
            if attempt > MAX_RESUME_ATTEMPTS {
                Resume::Blocked(label)
            } else {
                Resume::Retry {
                    attempt,
                    delay_secs: resume_delay(attempt).as_secs(),
                    label,
                }
            }
        }
    }
}

/// Hold a refused text for the next free turn, behind whatever is already held, and answer the queue's length.
///
/// The queue is the required edit on `c0ffbe8` (reviewer, 2026-10-15): the hold was a single slot and the
/// second refusal inside one turn overwrote the first silently, so the first text appeared in no surface.
/// A text is appended here and leaves through `deliver_held` (delivered) or `return_to_sender` (handed back
/// to the session that sent it), in the order it was refused.
///
/// IT IS BOUNDED, and the bound is the lead's second required edit: every entry is a message the seat has not
/// seen and the queue lives in this process's memory, so a seat whose agent refuses every prompt (a turn that
/// never ends, an agent that answers each frame with the same refusal) would grow it for as long as the room
/// lives and never say so. `Err(depth)` is the refusal, and it carries the depth so the caller can name it -
/// a runner that will not keep the text says that out loud, to its sender, instead of dropping it.
fn hold_text(held: &Arc<Mutex<VecDeque<HeldText>>>, entry: HeldText) -> Result<usize, usize> {
    let mut queue = held.lock();
    if queue.len() >= MAX_HELD {
        return Err(queue.len());
    }
    queue.push_back(entry);
    Ok(queue.len())
}

/// Publish what became of a refused text where the seat's readers look, and answer the sentence it published.
///
/// THE COUNT GOES WHERE THE STATE GOES (the lead's second required edit). The state file's classification is
/// the surface this runner already uses for a held text, and a sentence that reads the same at one held text
/// and at eight is a fact with no size: `4genteam ps` and the daemon's `status` serve it, so the number of
/// texts waiting for a free turn is published with the word. A FULL QUEUE IS PUBLISHED AS `blocked`, because
/// a runner that cannot keep the text it was asked to keep is not healthy and a reader must not find `working`
/// and conclude the seat will get to it.
///
/// BOTH SENTENCES NAME THE ORIGIN, because a reader who has to guess whose text is waiting cannot tell its
/// sender anything, and the sender is the one party who can act on the refusal.
fn publish_hold(
    state_path: &Path,
    held: Result<usize, usize>,
    attempts: u32,
    origin: &Origin,
) -> String {
    match held {
        Ok(depth) => {
            let sentence = format!(
                "held: the text from {} waits for the next free turn ({depth} of {MAX_HELD} held)",
                origin.label()
            );
            let _ = write_state(state_path, "working", &sentence, attempts);
            sentence
        }
        Err(depth) => {
            let sentence = format!(
                "the held queue is full ({depth} of {MAX_HELD}): the text from {} could NOT be held",
                origin.label()
            );
            let _ = write_state(state_path, "blocked", &sentence, attempts);
            sentence
        }
    }
}

/// Hand a text back to the session that sent it, so a refusal is a conversation rather than a hole.
///
/// The AC of hub task `c94cecfc` is that a text addressed to a seat is either delivered at that seat's next
/// turn end or REFUSED BACK to its sender with the text and the reason - never dropped in silence. Delivery
/// is `deliver_held`'s job; this is the other arm, and it is what the bound reaches instead of deleting the
/// text (measured 2026-10-16 at 23:14Z: 19 give-up lines in the window that began 22:00Z, every one of them a
/// text nobody was told about; 33 at a 23:41Z recount).
///
/// The frame is the same `text` verb the sender's own send spoke, carrying the text whole and the reason it
/// was not delivered; its `from`/`verb` name this runner and the `return` path, so the returned text is
/// attributed rather than anonymous. `want_reply` is true: the answer is the sender's runner saying it
/// relayed the frame, never "a write returned", which is the distinction this whole mailbox exists on.
fn return_to_sender(
    state_dir: &Path,
    origin: &Origin,
    target: &RunnerTarget,
    refused_id: &str,
    text: &str,
    reason: &str,
) -> Result<String, String> {
    let sender = origin.sender_session().ok_or_else(|| {
        format!(
            "{} is not a session this runtime can hand a text back to",
            origin.label()
        )
    })?;
    let body = format!(
        "the text you sent to {}/{} was NOT delivered: {reason}.\nIt was {refused_id}. It comes back whole:\n{text}",
        target.room, target.seat
    );
    let id = format!(
        "runner-return-{}-{}",
        std::process::id(),
        Utc::now().timestamp_millis()
    );
    let answer = send_to_seat(
        state_dir,
        sender,
        &serde_json::json!({
            "id": id,
            "type": "text",
            "from": format!("the runner for {}/{}", target.room, target.seat),
            "verb": "return",
            "text": body,
        }),
        None,
        true,
    )?;
    Ok(answer
        .pointer("/data/relayed")
        .and_then(|v| v.as_str())
        .unwrap_or("relayed")
        .to_string())
}

/// The id of one held delivery. A per-text attempt number repeats by design (every text has a first offer),
/// so the sequence is the process's own: two held texts must never share an id, or a refusal of one would be
/// read as the other's.
fn next_held_delivery_id() -> String {
    static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    format!(
        "runner-held-{}-{}",
        std::process::id(),
        SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed) + 1
    )
}

/// Deliver the text a refused prompt left held, when the seat is free again.
///
/// The other half of `Resume::Hold`: the decision says the text must not be dropped, and this is where it
/// comes back. It is called from the two arms that mark a turn over (`turn_end`, `agent_end` /
/// `session_settled`) and is the only taker of the held queue, so one held text cannot be delivered twice.
/// It takes ONE text — the front of the queue, the one refused first — so two texts held in one turn come
/// back in order, one turn apart, rather than racing the turn the first delivery opens.
///
/// EVERY WAY OUT IS NAMED, and none of them is a hole:
///
/// * offered to the agent — the entry carries its own attempt count, so the bound is the TEXT's and not the
///   process's (the sharing with the resume ladder's counter is the defect this fixes);
/// * past `MAX_HELD_ATTEMPTS` — `return_to_sender` hands it back, whole, naming who sent it and why;
/// * a hand-back that cannot be made (no addressable sender, or its mailbox refused) — the text goes back to
///   the FRONT of the queue with its count reset, published, rather than out of the process;
/// * a channel that is gone — the text goes back to the front too, where the old code cleared the pending
///   slot and let the text fall out of the queue entirely.
fn deliver_held(
    held: &Arc<Mutex<VecDeque<HeldText>>>,
    pending: &Arc<Mutex<Option<PendingPrompt>>>,
    to_child: &Sender<String>,
    state_path: &Path,
    state_dir: &Path,
    target: &RunnerTarget,
    attempts: u32,
) {
    let Some(mut entry) = held.lock().pop_front() else {
        return;
    };
    entry.attempts += 1;
    if entry.attempts > MAX_HELD_ATTEMPTS {
        let reason = format!("the agent refused it {MAX_HELD_ATTEMPTS} times while it was held");
        match return_to_sender(
            state_dir,
            &entry.origin,
            target,
            &entry.refused_id,
            &entry.text,
            &reason,
        ) {
            Ok(outcome) => {
                let sentence = format!(
                    "gave up: the text {} from {} was refused {MAX_HELD_ATTEMPTS} times while held; handed back to its sender ({outcome})",
                    entry.refused_id,
                    entry.origin.label()
                );
                mirror(&sentence);
                let _ = write_state(state_path, "blocked", &sentence, attempts);
            }
            Err(e) => {
                let sentence = format!(
                    "the text {} from {} could not be handed back ({e}); it stays held and is offered again",
                    entry.refused_id,
                    entry.origin.label()
                );
                mirror(&sentence);
                let _ = write_state(state_path, "blocked", &sentence, attempts);
                entry.attempts = 0;
                held.lock().push_front(entry);
            }
        }
        return;
    }
    let id = next_held_delivery_id();
    *pending.lock() = Some(PendingPrompt {
        id: id.clone(),
        text: entry.text.clone(),
        origin: entry.origin.clone(),
        held_attempts: entry.attempts,
    });
    if let Err(e) = send(
        to_child,
        &serde_json::json!({"id": id, "type": "prompt", "message": entry.text}),
    ) {
        *pending.lock() = None;
        mirror(&format!("ERROR held delivery: {e}"));
        held.lock().push_front(entry);
        return;
    }
    mirror(&format!(
        "delivering the text {} from {} (attempt {}/{MAX_HELD_ATTEMPTS}): the seat is free again",
        entry.refused_id,
        entry.origin.label(),
        entry.attempts
    ));
    let _ = write_state(
        state_path,
        "working",
        &format!(
            "delivering the text this runner held for the next turn (attempt {}/{MAX_HELD_ATTEMPTS}, from {})",
            entry.attempts,
            entry.origin.label()
        ),
        attempts,
    );
}

/// The pane's input: typed lines become prompts, Ctrl-C aborts the turn, oversized input is refused in
/// the reference's own words (`P:34-36, 100-121`).
fn spawn_pane_input(
    to_child: Sender<String>,
    pending: Arc<Mutex<Option<PendingPrompt>>>,
) -> thread::JoinHandle<()> {
    thread::spawn(move || {
        let stdin = std::io::stdin();
        let reader = BufReader::new(stdin.lock());
        let mut prompts: u32 = 0;
        for line in reader.lines() {
            let line = match line {
                Ok(l) => l,
                Err(_) => break,
            };
            if line.len() > MAX_PANE_INPUT_BYTES {
                mirror(PANE_INPUT_REJECTED);
                continue;
            }
            let frame = if line.trim() == "\u{3}" || line.trim().is_empty() {
                serde_json::json!({"id": format!("runner-pane-abort-{prompts}"), "type": "abort"})
            } else {
                prompts += 1;
                let id = format!("runner-pane-prompt-{prompts}");
                // Remember it: a turn that dies retryably is resumed with THIS message, and its response
                // is the one this runner owns. Its origin is the pane - the person at the terminal - which
                // is what a refusal has to name when the text cannot be delivered.
                *pending.lock() = Some(PendingPrompt::relayed(
                    &id,
                    &line,
                    Origin {
                        from: None,
                        verb: Some("pane".to_string()),
                    },
                ));
                serde_json::json!({"id": id, "type": "prompt", "message": line})
            };
            if let Err(e) = send(&to_child, &frame) {
                mirror(&format!("ERROR pane input: {e}"));
                break;
            }
        }
    })
}

fn send(to_child: &Sender<String>, frame: &serde_json::Value) -> Result<(), String> {
    let mut line = serde_json::to_string(frame).map_err(|e| format!("cannot encode frame: {e}"))?;
    line.push('\n');
    to_child
        .send(line)
        .map_err(|_| "the agent's channel is closed".to_string())
}

/// The seat's own log: `<state dir>/rooms/<room>/logs/<seat>.jsonl`.
///
/// `logs/` is the third directory of the room's layout (`seats/`, `logs/`, `run/`), and this is what fills
/// it: every line the runner mirrors into the pane is appended here as well, so its own diagnostics - the
/// retry ladder, a refused pane input, a mailbox error - are still readable after the pane has scrolled
/// away. That is what the U3 acceptance reads the seat's log for (`logs/<seat>.jsonl`): a file that only
/// ever appears once something has gone wrong makes "no log yet" and "nothing happened" the same answer.
pub fn seat_log_path(state_dir: &Path, room: &str, seat: &str) -> PathBuf {
    state_dir
        .join("rooms")
        .join(room)
        .join("logs")
        .join(format!("{seat}.jsonl"))
}

/// The seat this process is the runner for, decided once at start (`run`). `None` until then, and in a
/// process that never became a runner, so nothing outside `run` can invent a path.
static SEAT_LOG: OnceLock<PathBuf> = OnceLock::new();

/// Append one mirrored line to the seat's log, creating `logs/` if the writer got there first.
///
/// The pane stays the operator's surface: a log that cannot be written must not stop the seat, so the
/// failure is said on stderr rather than returned.
fn append_mirrored(path: &Path, text: &str) {
    if let Some(parent) = path.parent() {
        let _ = fs::create_dir_all(parent);
    }
    let line = serde_json::json!({"ts": Utc::now().to_rfc3339(), "text": text}).to_string();
    match fs::OpenOptions::new().create(true).append(true).open(path) {
        Ok(mut file) => {
            let _ = writeln!(file, "{line}");
        }
        Err(e) => eprintln!("{PANE_PREFIX} cannot write the seat's log {}: {e}", path.display()),
    }
}

/// Mirror a line into the pane, prefixed the way the reference prefixed its own lines, and append it to
/// the seat's log so the same line survives the pane.
fn mirror(text: &str) {
    let mut out = std::io::stdout();
    let _ = writeln!(out, "{PANE_PREFIX} {text}");
    let _ = out.flush();
    if let Some(path) = SEAT_LOG.get() {
        append_mirrored(path, text);
    }
}

fn wait_child(child: &mut Child) -> Result<String, String> {
    match child.wait() {
        Ok(status) => Ok(match status.code() {
            Some(code) => format!("omp exited (code {code})"),
            None => "omp exited on a signal".to_string(),
        }),
        Err(e) => Err(format!("cannot wait for the agent: {e}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A held text as the Hold arm builds one: an id, the text, and the origin its sender is answered at.
    fn held_entry(refused_id: &str, text: &str, from: Option<&str>, attempts: u32) -> HeldText {
        HeldText {
            refused_id: refused_id.to_string(),
            text: text.to_string(),
            origin: Origin {
                from: from.map(str::to_string),
                verb: Some("send".to_string()),
            },
            attempts,
        }
    }

    #[test]
    fn the_seat_log_is_the_rooms_logs_dir_and_holds_what_the_pane_showed() {
        let dir = std::env::temp_dir().join(format!("seat-log-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let path = seat_log_path(&dir, "lab", "dev");
        assert_eq!(
            path,
            dir.join("rooms").join("lab").join("logs").join("dev.jsonl"),
            "the seat's log is the room's logs/ dir, one file per seat"
        );

        // The directory does not exist yet: the writer makes it, so a runner that starts before anyone else
        // touches the room still leaves its line where the acceptance reads it.
        append_mirrored(&path, PANE_INPUT_REJECTED);
        append_mirrored(&path, "retry 1/3 in 5s: the turn died");

        let text = fs::read_to_string(&path).expect("the log is written");
        let lines: Vec<&str> = text.lines().collect();
        assert_eq!(lines.len(), 2, "one line per mirrored line: {text}");
        for line in &lines {
            let doc: serde_json::Value = serde_json::from_str(line).expect("a line is JSON");
            assert!(
                doc["ts"].as_str().is_some_and(|t| !t.is_empty()),
                "a line carries its own time: {line}"
            );
        }
        // The two things a reader greps this file for: the ladder, and the cap line - legible as text
        // rather than escaped into a form no reader looking for them would match.
        assert!(text.contains("retry 1/3"), "the ladder is legible: {text}");
        assert!(text.contains(PANE_INPUT_REJECTED), "the cap line is legible: {text}");
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn target_parsing_names_what_is_wrong() {
        assert_eq!(
            RunnerTarget::parse("roomA/dev").unwrap(),
            RunnerTarget {
                room: "roomA".into(),
                seat: "dev".into()
            }
        );
        let err = RunnerTarget::parse("roomA").unwrap_err();
        assert!(err.contains("<room>/<seat>"), "{err}");
        let err = RunnerTarget::parse("/dev").unwrap_err();
        assert!(err.contains("both non-empty"), "{err}");
    }

    #[test]
    fn omp_argv_carries_the_seat_and_its_model() {
        let seat = SeatSpec {
            seat_key: "dev".into(),
            seat_type: "developer".into(),
            runtime: "omp".into(),
            model: Some("deepseek/deepseek-flash:high".into()),
            permission_policy: Some("builtin:yolo".into()),
            cwd: None,
            agent_dir: None,
            policy_document: None,
            env: Default::default(),
        };
        let argv = child_argv(&AgentRuntime::Omp, Path::new("/state/rooms/roomA/seats/dev"), &seat)
            .expect("argv");
        assert_eq!(
            argv,
            vec![
                "--mode",
                "rpc",
                "--session-dir",
                "/state/rooms/roomA/seats/dev/sessions",
                "--approval-mode",
                "yolo",
                "--model",
                "deepseek/deepseek-flash:high",
            ]
        );
    }

    #[test]
    fn claude_argv_is_the_streaming_channel_with_the_seats_posture_and_model() {
        let mut seat = SeatSpec {
            seat_key: "architect".into(),
            seat_type: "architect".into(),
            runtime: "claude-code".into(),
            model: Some("opus".into()),
            permission_policy: None,
            cwd: None,
            agent_dir: None,
            policy_document: None,
            env: Default::default(),
        };
        let argv = child_argv(&AgentRuntime::ClaudeCode, Path::new("/state/s"), &seat).expect("argv");
        assert_eq!(
            argv,
            vec![
                "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
                "--include-partial-messages", "--permission-mode", "bypassPermissions", "--model", "opus",
            ]
        );
        seat.permission_policy = Some("locked".into());
        seat.model = None;
        let argv = child_argv(&AgentRuntime::ClaudeCode, Path::new("/state/s"), &seat).expect("argv");
        assert_eq!(argv.last().map(String::as_str), Some("default"));
        assert_eq!(child_program(&AgentRuntime::ClaudeCode, &seat), "claude");
    }

    /// The gate finding, as a table: the documented vocabulary (`docs/AGENT-SETUP.md:389`), the value the
    /// client materialises (`internal/clientsync/rigverb.go:62`), the unspecified case, and a word the repo
    /// does not define. The unattended posture must be what an unspecified policy yields, because that is
    /// what the live rig's seats carry and an always-ask floor is what parks them.
    #[test]
    fn every_documented_policy_maps_and_an_unknown_one_is_refused() {
        for policy in ["yolo", "builtin:yolo"] {
            assert_eq!(approval_mode(Some(policy)).unwrap(), "yolo", "{policy:?}");
        }
        for policy in ["locked", "standard", "open", "none"] {
            assert_eq!(approval_mode(Some(policy)).unwrap(), "always-ask", "{policy:?}");
        }
        // An OMITTED field is not a blank one, and the two used to collapse: a `String` with
        // `#[serde(default)]` turned absence into "", so the blank-refusal below swallowed the documented
        // default. The type carries the difference now, and BOTH cases are asserted here because a check
        // that tried only one of them could not see the difference at all.
        assert_eq!(
            approval_mode(None).unwrap(),
            "yolo",
            "an omitted policy takes the documented unattended default"
        );
        // A blank field is refused by name (the lead's ruling): "blank means unattended" is the shape of an
        // approval nobody meant to give, and the refusal names the way out.
        for policy in ["", "  "] {
            let err = approval_mode(Some(policy)).unwrap_err();
            assert!(err.contains("empty"), "{policy:?}: {err}");
            assert!(err.contains("locked|standard|open|yolo|none"), "{policy:?}: {err}");
        }
        // "approve" is not in the repo's vocabulary - it was this module's own invention, which is how the
        // mapping came to be inverted. It is refused now, by name.
        let err = approval_mode(Some("approve")).unwrap_err();
        assert!(err.contains("approve"), "{err}");
        assert!(err.contains("locked|standard|open|yolo|none"), "{err}");
    }

    /// The layer the bug actually lived in: an ABSENT key and a key holding `""` must arrive as DIFFERENT
    /// values, because one takes the documented unattended default and the other is refused by name. The old
    /// `#[serde(default)] String` could not represent the difference at all - absence became `""`.
    #[test]
    fn an_absent_policy_and_a_blank_one_deserialize_differently() {
        let omitted: SeatSpec = serde_json::from_str(
            r#"{"seat_key":"dev","seat_type":"developer","runtime":"omp"}"#,
        )
        .expect("a seat with no permission_policy must deserialize");
        assert_eq!(omitted.permission_policy, None);
        assert_eq!(
            approval_mode(omitted.permission_policy.as_deref()).unwrap(),
            "yolo",
            "omitted takes the documented default"
        );

        let blank: SeatSpec = serde_json::from_str(
            r#"{"seat_key":"dev","seat_type":"developer","runtime":"omp","permission_policy":""}"#,
        )
        .expect("a seat with a blank permission_policy must deserialize");
        assert_eq!(blank.permission_policy, Some(String::new()));
        let err = approval_mode(blank.permission_policy.as_deref()).unwrap_err();
        assert!(err.contains("empty"), "{err}");
    }

    #[test]
    fn a_runtime_without_a_native_path_is_refused_by_name() {
        let seat = SeatSpec {
            seat_key: "dev".into(),
            seat_type: "developer".into(),
            runtime: "agy".into(),
            model: None,
            permission_policy: None,
            cwd: None,
            agent_dir: None,
            policy_document: None,
            env: Default::default(),
        };
        let err = child_argv(
            &AgentRuntime::from_str_loose("agy"),
            Path::new("/state"),
            &seat,
        )
        .unwrap_err();
        assert!(err.contains("agy"), "{err}");
        assert!(err.contains("omp"), "{err}");
    }

    #[test]
    fn the_mailbox_lives_inside_the_room() {
        let target = RunnerTarget::parse("roomA/dev").unwrap();
        assert_eq!(
            target.mailbox(Path::new("/state")),
            PathBuf::from("/state/rooms/roomA/run/dev.sock")
        );
    }

    /// The defect parity 1 exists for: a dead turn's own error report must NOT read as a prompt, and the
    /// three signatures the finding measured must read as retryable.
    #[test]
    fn a_dead_turn_is_retryable_and_is_not_a_prompt() {
        for (error, label) in [
            (
                "RPC response exceeded the transport limit",
                "omp's rpc response cap",
            ),
            (
                "rpc chunk payload exceeds the transport limit",
                "omp's rpc chunk cap",
            ),
            (
                "The socket connection was closed unexpectedly",
                "the agent socket closed",
            ),
            ("429 rate limit exceeded", "provider rate limit"),
        ] {
            match classify_failure(error) {
                Failure::Retryable(what) => assert_eq!(what, label, "{error}"),
                Failure::Terminal(what) => panic!("{error} classified terminal ({what})"),
                Failure::Noop(what) => panic!("{error} classified a no-op ({what})"),
            }
        }
        match classify_failure("unknown tool 'nope'") {
            Failure::Terminal(what) => assert!(what.contains("refused"), "{what}"),
            Failure::Retryable(what) => panic!("a refused turn is not retryable ({what})"),
            Failure::Noop(what) => panic!("a refused turn is not a no-op ({what})"),
        }
    }

    /// A compact reply carries the compact frame's id and is never a turn, whatever it says — so the caller
    /// must be able to tell it from a prompt's or a resume's reply by the id alone.
    #[test]
    fn a_compact_reply_is_told_from_a_turn_by_its_id() {
        let compact = format!("{COMPACT_ID_PREFIX}{}", std::process::id());
        assert!(is_compact_reply(&compact), "{compact}");
        assert!(!is_compact_reply("runner-resume-1-1"));
        assert!(!is_compact_reply("runner-get-state"));
        // The measured correlation gap: a response whose id this runner does not recognise arrives as "?".
        // It must NOT be read as a compact reply — the message table below is what saves that case.
        assert!(!is_compact_reply("?"));
    }

    /// The R2 defect, at the classifier: omp refuses to compact a seat that has no turns yet, and that
    /// refusal was read as a refused turn — so a healthy seat was published `blocked` at first start and,
    /// because the nudge loop wakes only `idle` seats, was never handed its queue rows.
    #[test]
    fn a_compact_refusal_with_nothing_to_compact_is_a_no_op() {
        for (error, label) in [
            (
                "Nothing to compact (session too small)",
                "omp has nothing to compact yet",
            ),
            ("Nothing to compact", "omp has nothing to compact yet"),
            ("session too small", "the session is too small to compact"),
        ] {
            match classify_failure(error) {
                Failure::Noop(what) => assert_eq!(what, label, "{error}"),
                Failure::Terminal(what) => panic!("a compact refusal classified terminal ({what})"),
                Failure::Retryable(what) => panic!("a compact refusal classified retryable ({what})"),
            }
            // The shape a fire-and-forget compact reply actually arrives in: no pending prompt, no turn
            // running. NOT blocked: the seat is healthy and has to stay wakeable.
            assert_eq!(
                decide_resume(false, false, false, error, 0),
                Resume::Nothing(label),
                "{error}"
            );
        }
    }

    /// The rule parity 1 turns on: a turn that is still running is never resumed from an error line, and
    /// an error that leaves the turn dead must never leave the seat published as healthy.
    #[test]
    fn an_error_mid_turn_resumes_nothing() {
        let err = "RPC response exceeded the transport limit";
        assert_eq!(
            decide_resume(true, true, true, err, 0),
            Resume::Nothing("the turn is still running")
        );
        assert_eq!(
            decide_resume(true, false, true, err, 0),
            Resume::Retry {
                attempt: 1,
                delay_secs: 1,
                label: "omp's rpc response cap"
            }
        );
        // The id did not line up, but a prompt is pending and the turn is not running: still resumed, and
        // the caller logs the mismatch.
        assert_eq!(
            decide_resume(false, false, true, err, 0),
            Resume::Retry {
                attempt: 1,
                delay_secs: 1,
                label: "omp's rpc response cap"
            }
        );
        // Nothing pending and nothing running: blocked, not silently idle - the seat is not healthy.
        assert_eq!(
            decide_resume(false, false, false, err, 0),
            Resume::Blocked("a retryable failure with no turn to resume")
        );
        assert_eq!(
            decide_resume(true, false, false, "unknown tool 'nope'", 0),
            Resume::Blocked("the agent refused the turn")
        );
        // Bounded: the fourth failure of the same turn stops trying.
        assert_eq!(
            decide_resume(true, false, true, err, MAX_RESUME_ATTEMPTS),
            Resume::Blocked("omp's rpc response cap")
        );
    }

    #[test]
    fn resume_attempts_are_bounded_and_back_off() {
        assert_eq!(resume_delay(1), Duration::from_secs(1));
        assert_eq!(resume_delay(2), Duration::from_secs(2));
        assert_eq!(resume_delay(3), Duration::from_secs(4));
        // Past the bound the schedule stops growing: a bounded retry must not become a busy loop.
        assert_eq!(resume_delay(4), Duration::from_secs(4));
        assert_eq!(resume_delay(MAX_RESUME_ATTEMPTS + 5), Duration::from_secs(4));
    }

    #[test]
    fn the_seat_state_file_is_published_atomically() {
        let dir = std::env::temp_dir().join(format!("runner-state-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let path = dir.join("dev.state.json");
        write_state(&path, "retrying", "provider rate limit", 2).expect("write");
        let read: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&path).expect("read")).expect("parse");
        assert_eq!(read["state"], "retrying");
        assert_eq!(read["classification"], "provider rate limit");
        assert_eq!(read["resume_attempts"], 2);
        // THE WORD IS PUBLISHED WITH THE STATE, derived from the same string in the same call, so the two
        // cannot drift: `retrying` is work in flight, which is `working` in the daemon's vocabulary - and a
        // reader that compares this against `agentActivity.state` compares like with like.
        assert_eq!(read["activity"], "working");
        assert!(read["updated_at"].as_str().is_some_and(|t| t.ends_with('Z')));
        // no temp file survives the publish
        assert!(!path.with_extension("json.tmp").exists());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn nudge_attempts_are_bounded_and_back_off() {
        assert_eq!(nudge_delay(1), Duration::from_secs(1));
        assert_eq!(nudge_delay(2), Duration::from_secs(2));
        assert_eq!(nudge_delay(3), Duration::from_secs(4));
        // Past the bound the schedule stops growing, exactly as `resume_delay` does: a bounded ladder must
        // not become a busy loop.
        assert_eq!(nudge_delay(4), Duration::from_secs(4));
        assert_eq!(nudge_delay(MAX_NUDGE_ATTEMPTS + 5), Duration::from_secs(4));
    }

    /// The pile is identified by which rows are waiting, order-insensitively: the same rows listed in a
    /// different order are the same nudge (the ladder must not reset on a re-order), and a different pile
    /// is a different nudge (the ladder must reset when the work changes).
    #[test]
    fn a_nudge_key_is_the_seat_and_its_waiting_rows() {
        let one = vec!["qitem-b".to_string(), "qitem-a".to_string()];
        let reordered = vec!["qitem-a".to_string(), "qitem-b".to_string()];
        assert_eq!(nudge_key("dev", &one), nudge_key("dev", &reordered));
        assert_ne!(nudge_key("dev", &one), nudge_key("dev", &["qitem-a".to_string()]));
        assert_ne!(nudge_key("dev", &one), nudge_key("reviewer", &one));
    }

    /// The log is what the ladder reads, so it has to round-trip: one line per attempt, the count and the
    /// latest stamp for *this pile* coming back, and another pile's attempts not counting towards it.
    #[test]
    fn the_nudge_log_is_the_ladders_durable_state() {
        let dir = std::env::temp_dir().join(format!("runner-nudge-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let ids = vec!["qitem-a".to_string()];
        let key = nudge_key("dev", &ids);

        // Nothing recorded yet: the pile has never been asked.
        assert_eq!(nudge_progress(&dir, "demo", "dev", &key), NudgeProgress::default());

        for attempt in 1..=2u32 {
            append_nudge_attempt(
                &dir,
                &NudgeAttempt {
                    ts: Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
                    room: "demo".into(),
                    seat: "dev".into(),
                    key: key.clone(),
                    attempt,
                    max_attempts: MAX_NUDGE_ATTEMPTS,
                    session_name: "agenthub--demo--dev".into(),
                    qitem_ids: ids.clone(),
                    outcome: "prompt".into(),
                },
            )
            .expect("append");
        }
        // A different pile's attempt is durable too, but it is not this ladder's progress.
        append_nudge_attempt(
            &dir,
            &NudgeAttempt {
                ts: Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
                room: "demo".into(),
                seat: "dev".into(),
                key: nudge_key("dev", &["qitem-z".to_string()]),
                attempt: 3,
                max_attempts: MAX_NUDGE_ATTEMPTS,
                session_name: "agenthub--demo--dev".into(),
                qitem_ids: vec!["qitem-z".to_string()],
                outcome: "prompt".into(),
            },
        )
        .expect("append");

        let progress = nudge_progress(&dir, "demo", "dev", &key);
        assert_eq!(progress.attempts, 2, "two attempts for this pile");
        assert!(progress.last_ts.is_some(), "the latest stamp is readable");

        // Append-only: three lines on disk, nothing overwritten.
        let log = std::fs::read_to_string(nudge_log_path(&dir, "demo", "dev")).expect("log");
        assert_eq!(log.lines().count(), 3);
        assert!(log.contains("\"attempt\":1") && log.contains("\"attempt\":3"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// Item 5 of the change, pinned: the nudge is the runner's own `text` frame on the *agent channel*, and
    /// what the agent receives is a `prompt` on the child's stdin - not a tmux `send-keys`. Fails without
    /// the wiring: an empty channel here is a nudge that reached nobody.
    ///
    /// The refused-send half is pinned here too: the relayed prompt is REMEMBERED in the pending slot (its
    /// response is the one this runner owns, so a refusal of it can be held rather than dropped), and the
    /// turn is PUBLISHED when it starts - a seat mid-turn no longer reads as `idle` while the sender is
    /// told the text was taken.
    #[test]
    fn a_text_frame_becomes_the_agents_prompt_on_the_child_channel() {
        let (to_child, from_threads) = mpsc::channel::<String>();
        let target = RunnerTarget::parse("demo/dev").unwrap();
        let dir = std::env::temp_dir().join(format!("runner-relay-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let state_path = dir.join("dev.state.json");
        let pending: Arc<Mutex<Option<PendingPrompt>>> = Arc::new(Mutex::new(None));
        let answer = relay_frame(
            &serde_json::json!({
                "id": "nudge-1",
                "type": "text",
                "text": "you have work waiting",
                "from": "agenthub--demo--lead",
                "verb": "send",
            }),
            "text",
            "nudge-1",
            &to_child,
            &state_path,
            &target,
            &pending,
        )
        .expect("a text frame is relayed");
        assert_eq!(answer, serde_json::json!({"relayed": "prompt"}));

        let line = from_threads.recv_timeout(Duration::from_secs(1)).expect("a frame reached the agent");
        let frame: serde_json::Value = serde_json::from_str(line.trim()).expect("a JSON frame");
        assert_eq!(frame["type"], "prompt");
        assert_eq!(frame["message"], "you have work waiting");
        assert_eq!(
            pending.lock().clone(),
            Some(PendingPrompt {
                id: "nudge-1".to_string(),
                text: "you have work waiting".to_string(),
                // THE ORIGIN IS REMEMBERED WITH THE PROMPT: the response that reports a refusal carries an
                // id and nothing else, so this is the last moment the sender is known - and a runner that
                // cannot deliver or cannot keep the text has to name whose it was.
                origin: Origin {
                    from: Some("agenthub--demo--lead".to_string()),
                    verb: Some("send".to_string()),
                },
                held_attempts: 0,
            }),
            "the relayed prompt is ours: its response is matched by this id, which is what lets a refusal of it be held"
        );
        let published: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&state_path).expect("state")).expect("parse");
        assert_eq!(published["state"], "working", "the turn is published when it starts");
        assert_eq!(published["activity"], "working");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// THE REFUSED SEND, at the decision and at the queue: a prompt this runner sent is refused by the agent
    /// because a turn of its own is running. The text must not be dropped, must not be re-sent into the turn
    /// that refused it, and must come back when that turn ends - which is what the live room and U3 run 1
    /// both failed on (the send printed a delivery while the text was gone).
    #[test]
    fn a_refused_prompt_is_held_and_delivered_when_the_turn_ends() {
        let refusal = "Agent is already processing. Use steer() or followUp() or wait for the current turn to finish.";
        assert_eq!(
            decide_resume(true, true, true, refusal, 0),
            Resume::Hold("the agent refused this prompt while its turn was running"),
            "a refusal of our own prompt mid-turn is held, not dropped"
        );
        // The locked rule stands: a retryable error mid-turn is still nothing to resume, and a refusal that
        // is not ours is not held either.
        assert_eq!(
            decide_resume(true, true, true, "RPC response exceeded the transport limit", 0),
            Resume::Nothing("the turn is still running")
        );
        assert_eq!(
            decide_resume(false, true, false, refusal, 0),
            Resume::Nothing("the turn is still running")
        );

        let dir = std::env::temp_dir().join(format!("runner-held-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let state_path = dir.join("dev.state.json");
        let (to_child, from_threads) = mpsc::channel::<String>();
        let target = RunnerTarget::parse("demo/dev").unwrap();
        let held: Arc<Mutex<VecDeque<HeldText>>> = Arc::new(Mutex::new(VecDeque::new()));
        let pending: Arc<Mutex<Option<PendingPrompt>>> = Arc::new(Mutex::new(None));
        let mut attempts: u32 = 0;
        assert_eq!(
            hold_text(
                &held,
                held_entry("send-1-1", "the text the agent refused", Some("agenthub--demo--lead"), 0)
            ),
            Ok(1),
            "a refused text is queued and the queue answers its depth"
        );
        deliver_held(&held, &pending, &to_child, &state_path, &dir, &target, attempts);
        let line = from_threads
            .recv_timeout(Duration::from_secs(1))
            .expect("the held text is delivered");
        let frame: serde_json::Value = serde_json::from_str(line.trim()).expect("a JSON frame");
        assert_eq!(frame["type"], "prompt");
        assert_eq!(frame["message"], "the text the agent refused");
        assert!(held.lock().is_empty(), "the queue is emptied: nothing is delivered twice");
        assert!(pending.lock().is_some(), "the redelivered prompt is ours to watch");
        assert_eq!(
            attempts, 0,
            "a held delivery is NOT a resume attempt: the ladder's count is the ladder's (the two shared one counter, and that is what dropped the 19 texts)"
        );
        let published: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&state_path).expect("state")).expect("parse");
        assert_eq!(published["state"], "working");
        assert_eq!(published["activity"], "working");

        // THE COUNT IS PUBLISHED WITH THE WORD (the lead's second required edit): the state file's own
        // classification carries the depth, so `4genteam ps` and the daemon's status show a queue and its
        // size rather than "held", a fact with no size that read the same at one text and at twenty.
        let sentence = publish_hold(&state_path, Ok(2), 0, &Origin::default());
        assert!(
            sentence.contains("2 of 8"),
            "the count is named in the sentence the state publishes: {sentence}"
        );
        let published: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&state_path).expect("state")).expect("parse");
        assert_eq!(published["classification"], sentence);
        assert_eq!(published["state"], "working");
        // AND A FULL QUEUE IS A REFUSAL THE RUNNER NAMES. The text is not kept and not silently dropped: the
        // depth the bound was reached at is published, and the seat is `blocked` because a runner that cannot
        // keep what it was asked to keep will not get to it by waiting.
        let full = publish_hold(&state_path, Err(MAX_HELD), 0, &Origin::default());
        assert!(
            full.contains("full (8 of 8)"),
            "the queue names the bound it reached: {full}"
        );
        let published: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&state_path).expect("state")).expect("parse");
        assert_eq!(published["state"], "blocked");
        assert_eq!(published["classification"], full);

        // THE BOUND IS THE TEXT'S, NOT THE PROCESS'S - the whole defect, in one case. The ladder's counter is
        // already past its own bound here (it reached MAX_RESUME_ATTEMPTS on unrelated attempts), and the text
        // in the queue has been offered zero times: it must still be delivered. Before this cut, `deliver_held`
        // incremented THIS counter and compared it, so the first offer of a fresh text was dropped in
        // silence - 19 lines counted at 23:14Z in the window that began 22:00Z on 2026-10-16, 13 of them
        // addressed to the lead seat (the window is open-ended: 33 at a 23:41Z recount).
        assert!(
            hold_text(
                &held,
                held_entry("send-1-2", "refused again", Some("agenthub--demo--lead"), 0)
            )
            .is_ok(),
            "the queue still has room"
        );
        attempts = MAX_RESUME_ATTEMPTS + 5;
        deliver_held(&held, &pending, &to_child, &state_path, &dir, &target, attempts);
        let late: serde_json::Value = serde_json::from_str(
            from_threads
                .recv_timeout(Duration::from_secs(1))
                .expect("a text whose own count is zero is offered, whatever the ladder reached")
                .trim(),
        )
        .expect("a JSON frame");
        assert_eq!(late["message"], "refused again");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// THE QUEUE IS BOUNDED (the lead's required edit on c0ffbe8, 2026-10-15): a seat whose agent refuses
    /// prompt after prompt must not grow this process's memory for the room's lifetime without anyone being
    /// told. The bound refuses the text that does not fit, states the depth, and EVICTS NOTHING - a held text
    /// still waits, and the count in front of it is unchanged.
    #[test]
    fn the_held_queue_is_bounded_and_names_the_depth_it_refused() {
        let held: Arc<Mutex<VecDeque<HeldText>>> = Arc::new(Mutex::new(VecDeque::new()));
        for i in 1..=MAX_HELD {
            assert_eq!(
                hold_text(
                    &held,
                    held_entry(
                        &format!("send-1-{i}"),
                        &format!("text {i}"),
                        Some("agenthub--demo--lead"),
                        0
                    )
                ),
                Ok(i),
                "the queue takes MAX_HELD texts and says how many it holds"
            );
        }
        assert_eq!(
            hold_text(
                &held,
                held_entry("send-1-over", "one too many", Some("agenthub--demo--lead"), 0)
            ),
            Err(MAX_HELD),
            "the text that does not fit is refused with the depth, not queued"
        );
        assert_eq!(
            held.lock().len(),
            MAX_HELD,
            "the refusal evicts nothing: every held text is still waiting, in order"
        );
        assert_eq!(
            held.lock().front().map(|entry| entry.refused_id.clone()),
            Some("send-1-1".to_string()),
            "the text refused first is still in front"
        );
    }

    /// THE REQUIRED EDIT, pinned (reviewer, 2026-10-15, `GATE-c0ffbe8-refused-send`): the hold is a QUEUE,
    /// not a single slot. Two prompts refused inside ONE turn are both kept — the second must not write over
    /// the first — and they come back in the order they were refused, one per turn end. With the slot the
    /// first text was in no surface at all: not delivered, not held, not named.
    #[test]
    fn two_refusals_in_one_turn_keep_both_texts_in_order() {
        let dir = std::env::temp_dir().join(format!("runner-held-queue-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let state_path = dir.join("dev.state.json");
        let (to_child, from_threads) = mpsc::channel::<String>();
        let target = RunnerTarget::parse("demo/dev").unwrap();
        let held: Arc<Mutex<VecDeque<HeldText>>> = Arc::new(Mutex::new(VecDeque::new()));
        let pending: Arc<Mutex<Option<PendingPrompt>>> = Arc::new(Mutex::new(None));
        let mut attempts: u32 = 0;

        assert_eq!(
            hold_text(
                &held,
                held_entry("send-1-1", "the first refused text", Some("agenthub--demo--lead"), 0)
            ),
            Ok(1)
        );
        assert_eq!(
            hold_text(
                &held,
                held_entry("send-1-2", "the second refused text", Some("agenthub--demo--lead"), 0)
            ),
            Ok(2)
        );
        assert_eq!(
            held.lock().len(),
            2,
            "the second refusal is queued behind the first, not written over it"
        );

        // The turn both were refused in ends: the text in front comes back and the second keeps waiting.
        deliver_held(&held, &pending, &to_child, &state_path, &dir, &target, attempts);
        let first: serde_json::Value = serde_json::from_str(
            from_threads
                .recv_timeout(Duration::from_secs(1))
                .expect("the first text is delivered")
                .trim(),
        )
        .expect("a JSON frame");
        assert_eq!(first["message"], "the first refused text");
        assert_eq!(
            held.lock().len(),
            1,
            "the text still waiting is not delivered along with the one in front"
        );

        // ... and the turn that delivery opened ends: the second follows, in the order it was refused.
        deliver_held(&held, &pending, &to_child, &state_path, &dir, &target, attempts);
        let second: serde_json::Value = serde_json::from_str(
            from_threads
                .recv_timeout(Duration::from_secs(1))
                .expect("the second text is delivered")
                .trim(),
        )
        .expect("a JSON frame");
        assert_eq!(second["message"], "the second refused text");
        assert!(held.lock().is_empty(), "both texts left the queue exactly once");
        assert_eq!(attempts, 0, "and neither delivery counted as a resume attempt");
        let published: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&state_path).expect("state")).expect("parse");
        assert_eq!(published["state"], "working");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// THE BOUND IS A RETURN, NOT A HOLE (hub task `c94cecfc`). A text the agent refuses `MAX_HELD_ATTEMPTS`
    /// times goes BACK to the session that sent it, whole, with the reason - and the give-up line names the
    /// origin, which is the second acceptance criterion.
    ///
    /// Before this cut the bound dropped it: `mirror("gave up: the text <id> refused was refused 3 times while
    /// held")`, state `blocked`, and no surface anywhere said who sent it (19 such lines counted at 23:14Z in
    /// the window that began 22:00Z on 2026-10-16, 13 addressed to the lead seat; 33 at a 23:41Z recount).
    #[test]
    fn a_held_text_past_its_own_bound_goes_back_to_its_sender() {
        let dir = std::env::temp_dir().join(format!("runner-giveup-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let state_path = dir.join("dev.state.json");
        let target = RunnerTarget::parse("demo/dev").unwrap();
        let (to_child, _from_threads) = mpsc::channel::<String>();
        let held: Arc<Mutex<VecDeque<HeldText>>> = Arc::new(Mutex::new(VecDeque::new()));
        let pending: Arc<Mutex<Option<PendingPrompt>>> = Arc::new(Mutex::new(None));

        // The sender's own mailbox, plugged to the same protocol `send_to_seat` speaks: it takes the frame and
        // answers on the reply socket the frame names, which is what makes the hand-back a delivery rather
        // than a successful local write.
        let mailbox = target_mailbox(&dir, "demo", "lead");
        let listener = UnixListener::bind(&mailbox).expect("the sender's mailbox");
        let peer = thread::spawn(move || {
            let (stream, _) = listener.accept().expect("the hand-back connects");
            let mut reader = BufReader::new(stream);
            let line = frame::read_line_bounded(&mut reader, frame::MAX_FRAME_BYTES)
                .expect("the hand-back frame");
            let value: serde_json::Value = serde_json::from_str(line.trim()).expect("a JSON frame");
            let reply_to = value["replyTo"].as_str().expect("the reply socket");
            let mut back = UnixStream::connect(reply_to).expect("a reply socket to answer on");
            writeln!(
                back,
                "{}",
                serde_json::json!({"id": value["id"], "ok": true, "data": {"relayed": "prompt"}})
            )
            .expect("answer");
            value
        });

        assert_eq!(
            hold_text(
                &held,
                held_entry(
                    "send-9-1",
                    "the text nobody took",
                    Some("agenthub--demo--lead"),
                    MAX_HELD_ATTEMPTS
                )
            ),
            Ok(1)
        );
        deliver_held(&held, &pending, &to_child, &state_path, &dir, &target, 0);
        let returned = peer.join().expect("the sender's side");

        assert_eq!(returned["type"], "text", "the text comes back on the verb it left on");
        assert_eq!(returned["verb"], "return", "and it says which path returned it");
        assert_eq!(returned["from"], "the runner for demo/dev", "attributed, not anonymous");
        let body = returned["text"].as_str().expect("the returned body");
        assert!(
            body.contains("the text nobody took"),
            "the text comes back WHOLE: {body}"
        );
        assert!(
            body.contains("refused it 3 times while it was held"),
            "and with the reason: {body}"
        );

        assert!(
            held.lock().is_empty(),
            "the text left the queue to its sender, not into nothing"
        );
        assert!(pending.lock().is_none(), "and no prompt was opened for it");
        let published: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&state_path).expect("state")).expect("parse");
        assert_eq!(published["state"], "blocked");
        let classification = published["classification"].as_str().expect("a sentence");
        assert!(
            classification.starts_with("gave up"),
            "the give-up path says what happened: {classification}"
        );
        assert!(
            classification.contains("agenthub--demo--lead (send)"),
            "AND IT NAMES THE TEXT'S ORIGIN - the sender's session and the verb that carried it: {classification}"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A hand-back that cannot be made loses NOTHING: the text stays in the queue, its count restarts so it is
    /// offered again rather than retried into the same hole, and the state file names the origin it could not
    /// reach. The pane's own prompts and the rail's nudges have no session to answer to, so this is their path.
    #[test]
    fn a_hand_back_with_no_addressable_sender_keeps_the_text_held() {
        let dir = std::env::temp_dir().join(format!("runner-giveup-none-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let state_path = dir.join("dev.state.json");
        let target = RunnerTarget::parse("demo/dev").unwrap();
        let (to_child, _from_threads) = mpsc::channel::<String>();
        let held: Arc<Mutex<VecDeque<HeldText>>> = Arc::new(Mutex::new(VecDeque::new()));
        let pending: Arc<Mutex<Option<PendingPrompt>>> = Arc::new(Mutex::new(None));

        assert_eq!(
            hold_text(
                &held,
                held_entry("send-9-2", "the pane's own text", None, MAX_HELD_ATTEMPTS)
            ),
            Ok(1)
        );
        deliver_held(&held, &pending, &to_child, &state_path, &dir, &target, 0);

        let queue = held.lock();
        assert_eq!(queue.len(), 1, "the text is still held: nothing was dropped");
        assert_eq!(
            queue.front().map(|entry| entry.attempts),
            Some(0),
            "its count restarts, so it is offered again next turn end"
        );
        drop(queue);
        let published: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&state_path).expect("state")).expect("parse");
        assert_eq!(published["state"], "blocked");
        let classification = published["classification"].as_str().expect("a sentence");
        assert!(
            classification.contains("could not be handed back"),
            "{classification}"
        );
        assert!(
            classification.contains("an unnamed sender"),
            "an origin nobody named is named as unnamed, not invented: {classification}"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A held delivery whose channel to the agent is GONE keeps the text in the queue. The old arm cleared the
    /// pending slot and returned, so the popped text left the process on the one path where the agent could not
    /// have received it at all - the second silent loss in the same function.
    #[test]
    fn a_held_delivery_that_cannot_reach_the_agent_keeps_the_text() {
        let dir = std::env::temp_dir().join(format!("runner-held-channel-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let state_path = dir.join("dev.state.json");
        let target = RunnerTarget::parse("demo/dev").unwrap();
        let (to_child, from_threads) = mpsc::channel::<String>();
        let held: Arc<Mutex<VecDeque<HeldText>>> = Arc::new(Mutex::new(VecDeque::new()));
        let pending: Arc<Mutex<Option<PendingPrompt>>> = Arc::new(Mutex::new(None));

        assert_eq!(
            hold_text(
                &held,
                held_entry("send-9-3", "the text the child cannot take", Some("agenthub--demo--lead"), 0)
            ),
            Ok(1)
        );
        drop(from_threads);
        deliver_held(&held, &pending, &to_child, &state_path, &dir, &target, 0);

        assert!(pending.lock().is_none(), "no prompt is left pending");
        let queue = held.lock();
        assert_eq!(
            queue.len(),
            1,
            "a delivery that could not even reach the channel keeps the text"
        );
        assert_eq!(queue.front().map(|entry| entry.refused_id.clone()), Some("send-9-3".to_string()));
        drop(queue);
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A seat the runtime can address: the room's own `run/<seat>.sock`, which is where `send_to_seat` looks.
    fn target_mailbox(state_dir: &Path, room: &str, seat: &str) -> PathBuf {
        let path = RunnerTarget::parse(&format!("{room}/{seat}"))
            .expect("a target")
            .mailbox(state_dir);
        fs::create_dir_all(path.parent().expect("the run dir")).expect("run dir");
        path
    }

    /// A PARKED SEAT IS NOT NUDGED. The rail wake is the runtime's own route to a seat - the nudge hands the
    /// agent the prompt that its waiting work is about - so it must not be the route that reaches a parked
    /// seat, and a refusal must not burn the ladder's attempts either: the ladder's state is the durable log.
    ///
    /// RED before the gate: the nudge went to a mailbox with no runner and the attempt was recorded.
    #[test]
    fn a_parked_seat_is_not_nudged() {
        let dir = std::env::temp_dir().join(format!("agenthub-nudge-park-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let seat_dir = dir.join("rooms").join("lab").join("seats").join("dev");
        std::fs::create_dir_all(&seat_dir).expect("seat dir");
        std::fs::write(
            seat_dir.join("parked.json"),
            r#"{"seat":"dev","reason":"made 300 tool calls in the last minute, over the 10 call ceiling","at":"2026-10-16T17:23:51Z","cost_usd":0.25,"calls":300}"#,
        )
        .expect("record");

        let err = nudge_seat(&dir, "lab", "dev", "agenthub--lab--dev", &["qitem-1".to_string()], 1)
            .expect_err("a parked seat is not nudged");
        assert!(
            err.contains("PARKED") && err.contains("the rail prompt was not delivered"),
            "the nudge must refuse with the park, got {err:?}"
        );
        assert!(
            !nudge_log_path(&dir, "lab", "dev").exists(),
            "a refused nudge must record no attempt: a parked seat does not spend the ladder"
        );

        // AND THE WAY BACK IS THE RECORD'S ABSENCE: the nudge is the route that hands a waiting seat the work
        // its rail holds, so the exit has to restore IT first. Nothing above caches the park, so a seat let back
        // in is reachable on the next tick rather than the next daemon, and the attempt the refusal withheld is
        // recorded now - the ladder's half of the round trip, and the inverse of the assertion above.
        std::fs::remove_file(seat_dir.join("parked.json")).expect("the record is the whole park");
        let after = nudge_seat(&dir, "lab", "dev", "agenthub--lab--dev", &["qitem-1".to_string()], 1);
        assert!(
            !matches!(&after, Err(e) if e.contains("PARKED")),
            "with the record gone the same nudge must answer for itself again: {after:?}"
        );
        assert!(
            nudge_log_path(&dir, "lab", "dev").exists(),
            "an unparked seat spends the ladder: the attempt the park withheld is recorded again"
        );

        let _ = std::fs::remove_dir_all(&dir);
    }
}
