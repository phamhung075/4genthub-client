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
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{self, Sender};
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
            // `--approval-mode` is the OMP spelling (`PR:224-225`); a seat's permission policy names it.
            argv.push("--approval-mode".to_string());
            argv.push(if seat.permission_policy == "approve" {
                "yolo".to_string()
            } else {
                "always-ask".to_string()
            });
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
    let ready = {
        let (ready_tx, ready_rx) = mpsc::channel::<Result<(), String>>();
        thread::spawn(move || {
            let reader = BufReader::new(child_stdout);
            let mut announced_ready = false;
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
                        if id == "runner-get-state" {
                            match record.get("success").and_then(|v| v.as_bool()) {
                                Some(false) => {
                                    let _ = ready_tx.send(Err(format!(
                                        "the agent answered get_state with an error: {}",
                                        record
                                            .get("error")
                                            .and_then(|v| v.as_str())
                                            .unwrap_or("no error given")
                                    )));
                                }
                                _ => {
                                    let _ = ready_tx.send(Ok(()));
                                }
                            }
                        }
                        if let Some(err) = record.get("error").and_then(|v| v.as_str()) {
                            mirror(&format!("ERROR rpc: {err}"));
                        }
                    }
                    _ => {
                        // Events: `agent_*`, `turn_*`, `message_*`, `tool_execution_*`, `queue_update`,
                        // `compaction_*`, `auto_retry_*`, `extension_*` (`P:180-250`). The pane gets the
                        // same line the reference mirrored, and the status work of parity 1/4 joins here.
                        if let Some(kind) = record.get("type").and_then(|t| t.as_str()) {
                            // Assistant prose arrives as `message_update`/`text_delta`, and that is the one
                            // event whose payload the pane shows as text rather than as a name (`P:204-208`).
                            if let Some(delta) = record
                                .get("assistantMessageEvent")
                                .and_then(|e| e.get("delta"))
                                .and_then(|d| d.as_str())
                            {
                                print!("{delta}");
                                let _ = std::io::stdout().flush();
                            } else {
                                mirror(kind);
                            }
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

    let pane = spawn_pane_input(to_child.clone());
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

/// The pane's input: typed lines become prompts, Ctrl-C aborts the turn, oversized input is refused in
/// the reference's own words (`P:34-36, 100-121`).
fn spawn_pane_input(to_child: Sender<String>) -> thread::JoinHandle<()> {
    thread::spawn(move || {
        let stdin = std::io::stdin();
        let reader = BufReader::new(stdin.lock());
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
                serde_json::json!({"id": "runner-pane-abort", "type": "abort"})
            } else {
                serde_json::json!({"id": "runner-pane-prompt", "type": "prompt", "message": line})
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
            permission_policy: "approve".into(),
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
}
