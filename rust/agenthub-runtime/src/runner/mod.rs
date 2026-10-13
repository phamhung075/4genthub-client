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
//! `pi-runner-protocol.js`, **PR**, in `@openrig/cli/daemon/dist/adapters`):
//!
//! * the reference child's channel is a pair of **pipes** on fd 0/1/2, not a socketpair
//!   (`P:818-822`); the design calls it a socketpair. Ownership is what matters — nobody else holds
//!   this channel — so this module uses pipes and says so rather than inventing a socketpair to match
//!   a sentence.
//! * compaction is **observed**, never issued, by the reference (`P:227-238`; no `{"type":"compact"}`
//!   anywhere in the package). The frame below is this runtime's own, and the daemon reaches it over the
//!   seat's mailbox socket instead of stealing a descriptor with `pidfd_getfd`, which needed
//!   `CAP_SYS_PTRACE` and is retired with this module.

use crate::types::{AgentRuntime, RoomSpec, SeatSpec};
use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use parking_lot::Mutex;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{self, Sender};
use std::sync::Arc;
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
            argv.push(approval_mode(&seat.permission_policy)?.to_string());
            if let Some(model) = seat.model.as_deref().filter(|m| !m.trim().is_empty()) {
                argv.push("--model".to_string());
                argv.push(model.to_string());
            }
            Ok(argv)
        }
        other => Err(format!(
            "runner has no native rpc path for runtime '{}'; seat '{}' needs one of: omp",
            other.as_str(),
            seat.seat_key
        )),
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
    let argv = child_argv(&runtime, &seat_dir, &seat)?;
    let cwd = seat
        .cwd
        .clone()
        .or_else(|| spec.cwd.clone())
        .unwrap_or_else(|| ".".to_string());
    fs::create_dir_all(&seat_dir).map_err(|e| format!("cannot create seat dir: {e}"))?;

    // The supervisor already put AGENTHUB_* and PI_CODING_AGENT_DIR into this process's environment when
    // it launched the pane, so the child inherits the seat's identity rather than a second copy of it.
    let mut child = Command::new(&seat.runtime)
        .args(&argv)
        .current_dir(&cwd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit()) // the pane: the human sees omp's own errors
        .spawn()
        .map_err(|e| format!("cannot spawn '{}': {e}", seat.runtime))?;
    info!(
        "runner for {}/{} spawned {} {:?} (pid {:?})",
        target.room,
        target.seat,
        seat.runtime,
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
    let writer = thread::spawn(move || {
        let mut stdin = child_stdin;
        for frame in from_threads {
            if let Err(e) = stdin.write_all(frame.as_bytes()).and_then(|_| stdin.flush()) {
                // The child is gone; the reader below sees the exit and decides how to report it.
                warn!("runner: cannot write to the child: {e}");
                break;
            }
        }
    });

    let mailbox = target.mailbox(state_dir);
    spawn_mailbox(mailbox.clone(), to_child.clone())?;

    // The RPC reader: responses are answered to a pending id, events are mirrored to the pane.
    // The RPC reader: responses answer a pending id, events drive the pane and the seat's state, and a
    // turn that dies retryably is resumed from here (never from the sight of an error line alone).
    let state_path = state_file_path(state_dir, &target.room, &target.seat);
    let pending: Arc<Mutex<Option<(String, String)>>> = Arc::new(Mutex::new(None));
    let ready = {
        let (ready_tx, ready_rx) = mpsc::channel::<Result<(), String>>();
        let pending_reader = Arc::clone(&pending);
        let to_child_reader = to_child.clone();
        let state_path = state_path.clone();
        thread::spawn(move || {
            let reader = BufReader::new(child_stdout);
            let mut announced_ready = false;
            let mut turn_active = false;
            let mut attempts: u32 = 0;
            for line in reader.lines() {
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
                            guard.as_ref().is_some_and(|(pid, _)| pid == id)
                        };
                        if let Some(err) = &error_text {
                            mirror(&format!("ERROR rpc: {err}"));
                        }
                        if failed {
                            if let Some(err) = error_text {
                                let has_pending = pending_reader.lock().is_some();
                                if !is_our_prompt && has_pending && !turn_active {
                                    // The measured correlation gap: omp answered an error without an id this
                                    // runner recognises. Say so, then treat the pending prompt as the turn.
                                    mirror("rpc error without a matching id; treating the pending prompt as its turn");
                                }
                                match decide_resume(is_our_prompt, turn_active, has_pending, &err, attempts) {
                                    Resume::Nothing(reason) => {
                                        mirror(&format!("rpc error, nothing to resume: {reason}"));
                                    }
                                    Resume::Retry {
                                        attempt,
                                        delay_secs,
                                        label,
                                    } => {
                                        attempts = attempt;
                                        mirror(&format!(
                                            "retry {attempt}/{MAX_RESUME_ATTEMPTS} in {delay_secs}s: {label} :: {err}"
                                        ));
                                        let _ = write_state(&state_path, "retrying", label, attempts);
                                        let message =
                                            pending_reader.lock().as_ref().map(|(_, m)| m.clone());
                                        if let Some(message) = message {
                                            let sender = to_child_reader.clone();
                                            let pending_retry = Arc::clone(&pending_reader);
                                            thread::spawn(move || {
                                                thread::sleep(Duration::from_secs(delay_secs));
                                                let id = format!(
                                                    "runner-resume-{}-{attempt}",
                                                    std::process::id()
                                                );
                                                *pending_retry.lock() =
                                                    Some((id.clone(), message.clone()));
                                                let _ = send(
                                                    &sender,
                                                    &serde_json::json!({
                                                        "id": id,
                                                        "type": "prompt",
                                                        "message": message
                                                    }),
                                                );
                                            });
                                        }
                                    }
                                    Resume::Blocked(reason) => {
                                        mirror(&format!("gave up: {reason} :: {err}"));
                                        attempts += 1;
                                        let _ =
                                            write_state(&state_path, "blocked", reason, attempts);
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
                            "turn_start" => turn_active = true,
                            "turn_end" => turn_active = false,
                            // omp's own bounded provider retry (`P:180-250`): log it, and do NOT stack a
                            // second retry on top — if it succeeds the prompt response is a success.
                            "auto_retry_start" => {
                                mirror("provider retry in progress (omp's own)");
                                let _ = write_state(&state_path, "retrying", "omp auto-retry", attempts);
                            }
                            "auto_retry_end" => mirror("provider retry finished"),
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
                                let _ = write_state(&state_path, "idle", "", attempts);
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
            Ok(())
        }
        Err(e) => Err(e),
    }
}

/// Serve the daemon's mailbox: a frame arrives, it goes to the child.
fn spawn_mailbox(path: PathBuf, to_child: Sender<String>) -> Result<(), String> {
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
            serve_mailbox_stream(stream, &to_child);
        }
    });
    Ok(())
}

fn serve_mailbox_stream(stream: UnixStream, to_child: &Sender<String>) {
    let reader = BufReader::new(stream);
    for line in reader.lines() {
        let line = match line {
            Ok(l) => l,
            Err(e) => {
                warn!("runner mailbox read error: {e}");
                return;
            }
        };
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        match serde_json::from_str::<serde_json::Value>(trimmed) {
            // The native compact frame: the daemon no longer steals a descriptor to send it.
            Ok(frame) if frame.get("type").and_then(|t| t.as_str()) == Some("compact") => {
                let id = frame
                    .get("id")
                    .and_then(|v| v.as_str())
                    .unwrap_or("runner-compact");
                match send(
                    to_child,
                    &serde_json::json!({"id": id, "type": "compact"}),
                ) {
                    Ok(()) => mirror(&format!("compact relayed to the agent (id {id})")),
                    Err(e) => mirror(&format!("ERROR compact: {e}")),
                }
            }
            Ok(other) => mirror(&format!(
                "ERROR mailbox: unknown frame {}",
                other.get("type").and_then(|t| t.as_str()).unwrap_or("?")
            )),
            Err(e) => mirror(&format!("ERROR mailbox: {e}")),
        }
    }
}

/// Ask a running seat's runner to relay a compaction frame to its agent.
///
/// This is what replaces the `pidfd_getfd` theft: the daemon writes one JSON line into the seat's
/// mailbox socket and the runner — the process that owns the child's channel — relays it. A seat with no
/// runner is a refusal naming the seat, not a silent success.
pub fn request_compact(state_dir: &Path, session_or_target: &str) -> Result<String, String> {
    let target = match session_or_target.split_once('/') {
        Some(_) => RunnerTarget::parse(session_or_target)?,
        // The IPC surface names seats by the session the supervisor derives: agenthub--<room>--<seat>.
        None => {
            let rest = session_or_target
                .strip_prefix("agenthub--")
                .ok_or_else(|| {
                    format!(
                        "'{session_or_target}' is neither <room>/<seat> nor the session name \
                         agenthub--<room>--<seat>"
                    )
                })?;
            let (room, seat) = rest.rsplit_once("--").ok_or_else(|| {
                format!("'{session_or_target}' does not name a room and a seat")
            })?;
            RunnerTarget::parse(&format!("{room}/{seat}"))?
        }
    };
    let mailbox = target.mailbox(state_dir);
    let mut stream = UnixStream::connect(&mailbox).map_err(|e| {
        format!(
            "no runner for {}/{}: cannot reach its mailbox {} ({e})",
            target.room,
            target.seat,
            mailbox.display()
        )
    })?;
    let id = format!("forcecompact-{}", std::process::id());
    let frame = serde_json::json!({"id": id, "type": "compact"});
    let mut line =
        serde_json::to_string(&frame).map_err(|e| format!("cannot encode compact frame: {e}"))?;
    line.push('\n');
    stream
        .write_all(line.as_bytes())
        .map_err(|e| format!("cannot write to the runner mailbox {}: {e}", mailbox.display()))?;
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
/// `internal/clientpolicy/policy.go` states the same shape from the runner's side: "OpenRig launches an omp
/// seat in exactly one of two postures - yolo, or always-ask which the managed runner auto-cancels".
///
/// So the unattended posture belongs to `yolo`, to the materialised `builtin:yolo`, and to an **unspecified**
/// policy - the last one is what the live rig's seats carry, and it must not be the posture that parks them.
/// The four restrictive words take `always-ask`, whose dialogs this runner does not leave hanging: a dialog
/// it cannot present is answered cancelled (see the reader), which is the "auto-cancels" half of that
/// sentence. Anything else is refused by name, because guessing a launch posture is what inverted this
/// mapping in the first place.
pub fn approval_mode(policy: &str) -> Result<&'static str, String> {
    match policy.trim() {
        "" | "yolo" | "builtin:yolo" => Ok("yolo"),
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
/// The defect this replaces is named and measured: the OpenRig activity store maps an omp
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
}

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
    for (needle, label) in RETRYABLE_SIGNATURES {
        if error.contains(needle) {
            return Failure::Retryable(label);
        }
    }
    Failure::Terminal("the agent refused the turn")
}

/// How many times this runner re-sends a turn that died retryably, and how long it waits between tries.
pub const MAX_RESUME_ATTEMPTS: u32 = 3;

pub fn resume_delay(attempt: u32) -> Duration {
    // 1s, 2s, 4s — bounded, and the same shape for every retryable class.
    Duration::from_secs(1u64 << attempt.min(3).saturating_sub(1).min(3))
}

/// The seat's own state, written where the daemon and `ps` can read it (the reference writes a
/// `runner-state.json` beside the session for the same reason, `pi-runner-protocol.js:26`).
pub fn write_state(state_path: &Path, state: &str, classification: &str, attempts: u32) -> Result<(), String> {
    let payload = serde_json::json!({
        "state": state,
        "classification": classification,
        "resume_attempts": attempts,
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
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Resume {
    Nothing(&'static str),
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
    if turn_active {
        // The live turn owns the error; the seat is healthy and nothing here may touch it.
        return Resume::Nothing("the turn is still running");
    }
    let ours = ours_by_id || has_pending_prompt;
    match classify_failure(error) {
        Failure::Terminal(label) => Resume::Blocked(label),
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

/// The pane's input: typed lines become prompts, Ctrl-C aborts the turn, oversized input is refused in
/// the reference's own words (`P:34-36, 100-121`).
fn spawn_pane_input(
    to_child: Sender<String>,
    pending: Arc<Mutex<Option<(String, String)>>>,
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
                // is the one this runner owns.
                *pending.lock() = Some((id.clone(), line.clone()));
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

/// Mirror a line into the pane, prefixed the way the reference prefixed its own lines.
fn mirror(text: &str) {
    let mut out = std::io::stdout();
    let _ = writeln!(out, "{PANE_PREFIX} {text}");
    let _ = out.flush();
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
            permission_policy: "builtin:yolo".into(),
            cwd: None,
            agent_dir: None,
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

    /// The gate finding, as a table: the documented vocabulary (`docs/AGENT-SETUP.md:389`), the value the
    /// client materialises (`internal/clientsync/rigverb.go:62`), the unspecified case, and a word the repo
    /// does not define. The unattended posture must be what an unspecified policy yields, because that is
    /// what the live rig's seats carry and an always-ask floor is what parks them.
    #[test]
    fn every_documented_policy_maps_and_an_unknown_one_is_refused() {
        for policy in ["yolo", "builtin:yolo", "", "  "] {
            assert_eq!(approval_mode(policy).unwrap(), "yolo", "{policy:?}");
        }
        for policy in ["locked", "standard", "open", "none"] {
            assert_eq!(approval_mode(policy).unwrap(), "always-ask", "{policy:?}");
        }
        // "approve" is not in the repo's vocabulary - it was this module's own invention, which is how the
        // mapping came to be inverted. It is refused now, by name.
        let err = approval_mode("approve").unwrap_err();
        assert!(err.contains("approve"), "{err}");
        assert!(err.contains("locked|standard|open|yolo|none"), "{err}");
    }

    #[test]
    fn a_runtime_without_a_native_path_is_refused_by_name() {
        let seat = SeatSpec {
            seat_key: "dev".into(),
            seat_type: "developer".into(),
            runtime: "claude-code".into(),
            model: None,
            permission_policy: String::new(),
            cwd: None,
            agent_dir: None,
            env: Default::default(),
        };
        let err = child_argv(
            &AgentRuntime::from_str_loose("claude-code"),
            Path::new("/state"),
            &seat,
        )
        .unwrap_err();
        assert!(err.contains("claude-code"), "{err}");
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
            }
        }
        match classify_failure("unknown tool 'nope'") {
            Failure::Terminal(what) => assert!(what.contains("refused"), "{what}"),
            Failure::Retryable(what) => panic!("a refused turn is not retryable ({what})"),
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
        assert!(read["updated_at"].as_str().is_some_and(|t| t.ends_with('Z')));
        // no temp file survives the publish
        assert!(!path.with_extension("json.tmp").exists());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
