use std::collections::HashMap;
use std::fs;
use std::io::Write;
use std::path::Path;
use std::process::Command;
use std::time::Duration;
use tracing::{info, warn};

pub struct TerminalManager;

/// Every tmux call this module makes goes through here, for one reason expressed twice: the client must not
/// carry this runtime's denied addressing into the server it may create, and it must reach the server this
/// runtime OWNS rather than one the host happens to be running. Both halves are load-bearing:
/// `TMUX_TMPDIR` (set once at startup, see `crate::own_tmux_root`) decides WHICH server, and the scrub
/// decides whether that server starts clean - measured, a private server created by an unscrubbed client
/// carried the host's variables just as the shared one did (7 of them), because a server takes the
/// environment of whoever creates it. The first tmux call in `launch_session` is `ensure_server`, which starts
/// ours from a scrubbed client and keeps no seat's environment on its argv - it is the one call that may
/// create the server, so it is the one call that must.
/// Pane lines compared before and after Enter: enough to hold the input box and what sits around it.
const SUBMIT_CAPTURE_LINES: usize = 30;
/// Time the pane gets to repaint after a paste and after each Enter.
const SUBMIT_SETTLE: Duration = Duration::from_millis(400);

/// What `send_text` can say about a send. tmux accepting the keystrokes is not the same as the seat
/// receiving a message, so the answer names which one was observed.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum SendOutcome {
    /// The pane changed after Enter: the input box took the text.
    Submitted,
    /// The pane did not change after a second Enter, or could not be read: the text may still be staged.
    Unconfirmed,
}

/// Press Enter and decide from two pane captures whether the text was submitted. A pane identical to the
/// one before Enter means the input did not move, so Enter is pressed once more; a pane still identical
/// is `Unconfirmed`. A pane that changes for another reason (a spinner) reads as submitted, so
/// `Submitted` is an observation about the pane, not a receipt from the agent.
fn confirm_submission(
    before: Option<String>,
    mut press_enter: impl FnMut() -> Result<(), String>,
    mut capture: impl FnMut() -> Option<String>,
    settle: Duration,
) -> Result<SendOutcome, String> {
    let Some(before) = before else {
        std::thread::sleep(settle);
        press_enter()?;
        return Ok(SendOutcome::Unconfirmed);
    };
    for _ in 0..SUBMIT_ENTER_ATTEMPTS {
        std::thread::sleep(settle);
        press_enter()?;
        std::thread::sleep(settle);
        match capture() {
            Some(after) if after != before => return Ok(SendOutcome::Submitted),
            Some(_) => {}
            None => return Ok(SendOutcome::Unconfirmed),
        }
    }
    Ok(SendOutcome::Unconfirmed)
}

/// Enter presses before a send is reported unconfirmed: the first, and one retry.
const SUBMIT_ENTER_ATTEMPTS: usize = 2;

/// Variables tmux itself sets in every pane. A pane that carries them makes a bare `tmux` command in the seat
/// act on the ROOM'S OWN server (`$TMUX` beats `TMUX_TMPDIR`): fe-dev's `tmux kill-server` ended all five seats
/// on 2026-10-15. They are not in the daemon's environment, so `denied_env_keys` never finds them; the wrapper
/// names them. Without them a bare `tmux` in a seat reaches the user's default server, not the room's.
const PANE_TMUX_KEYS: [&str; 2] = ["TMUX", "TMUX_PANE"];

/// The ONE string tmux hands to the pane's shell: `env -u <key>... <runtime_cmd>`, for every denied key and the
/// two tmux pane variables.
fn pane_command(denied: &[String], runtime_cmd: &str) -> String {
    let mut wrapped = String::from("env");
    for key in denied.iter().map(String::as_str).chain(PANE_TMUX_KEYS) {
        wrapped.push_str(" -u ");
        wrapped.push_str(key);
    }
    wrapped.push(' ');
    wrapped.push_str(runtime_cmd);
    wrapped
}

/// The `-e KEY=VALUE` arguments for one session, minus every denied key.
///
/// `-e` is what gives a seat its own identity instead of the server's, so the list itself is not optional; what
/// the deny filter adds is the credential half. A seat's env map is the state root's whole `.env`, so without
/// the filter `AGENTHUB_CONNECTOR_TOKEN` rides `tmux new-session -e AGENTHUB_CONNECTOR_TOKEN=<value>` into `ps`
/// - measured on this box 2026-10-15.
fn pane_env_args(env_vars: &HashMap<String, String>) -> Vec<String> {
    let mut args = Vec::new();
    for (k, v) in env_vars {
        if crate::env_is_denied(k) {
            continue;
        }
        args.push("-e".to_string());
        args.push(format!("{k}={v}"));
    }
    args
}

fn tmux_command() -> Command {
    let mut cmd = Command::new("tmux");
    crate::scrub_env(&mut cmd);
    cmd
}

/// Start this root's tmux server from a client whose command line carries no seat's environment, and keep it
/// alive until a seat's pane exists.
///
/// WHY THE SERVER NEEDS A START OF ITS OWN: a tmux server is created by whichever client reaches it first, and
/// the server KEEPS THAT CLIENT'S ARGV for as long as it runs - `/proc/<server pid>/cmdline` is world-readable
/// (0444), while the same process's environment is not. The session-creating client below is the one that must
/// carry a seat's environment (`new-session -e KEY=VALUE`), so when it is also the creating client, every one
/// of those pairs is published on the longest-lived process in the room. Measured on this box 2026-10-16, after
/// the connector token left the `-e` list: the room's server argv held `DEEPSEEK_API_KEY=`, `TYPESAFE_API_KEY=`
/// and `AGENTHUB_TOKEN=`, and it held them because the first seat's `new-session` had created it. Those three
/// keys cannot be denied the way the connector token was - a seat's model calls read the first two and its own
/// `mcp.json` reads the third - so the fix is the argv, not the key. Started here, the server's argv is this
/// call's, and a seat's `-e` pairs live only in that seat's own client, which is gone within the launch.
///
/// `exit-empty off` is what makes the bootstrap hold. Under tmux's default an empty server exits as soon as its
/// last client disconnects, so a `start-server`-only client leaves nothing behind: measured here, a bare
/// `start-server` on a fresh root had no server a moment later, and `new-session` then created the real one out
/// of its own argv - which is exactly how the pairs got onto it. Off, the empty server waits for the seats.
/// The consequence is a decision rather than a side effect: this root's server now OUTLIVES its last session.
/// The root belongs to the state root and is shared by every room under it, so no per-room stop may kill it,
/// and a stop now leaves an idle server (with a clean argv) instead of none.
///
/// A server that is already up is left exactly as it is - not started, and its own `exit-empty` not touched -
/// because whether an older server's argv is clean cannot be read from here and a live server may be carrying
/// live seats. A server started before this change has `exit-empty` on, so it still exits with its last session
/// and the next launch bootstraps over its grave; one started by this call is clean and stays clean.
fn ensure_server() -> Result<(), String> {
    if matches!(tmux_command().args(["list-sessions"]).output(), Ok(out) if out.status.success()) {
        return Ok(());
    }
    let out = tmux_command()
        .args(["start-server", ";", "set", "-g", "exit-empty", "off"])
        .output()
        .map_err(|e| format!("failed to start the tmux server: {e}"))?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        return Err(format!("tmux start-server failed: {}", stderr.trim()));
    }
    Ok(())
}

/// What tmux said about the pane it created: the pid when it gave one, and the read's OWN failure in its own
/// words when it did not.
///
/// A PID OF 0 IS A PID, NOT "UNKNOWN", and this type exists because the two were the same value. Measured
/// 2026-10-15 in a C4 acceptance run: `list-panes` answered an unparsable line 2.09ms after the session was
/// created, `unwrap_or(0)` collapsed it to 0, the log printed `started with pane pid 0`, and the supervisor
/// then reported `its pane is gone` - tmux's refusal went to stderr, was never read, and a read that failed
/// looked exactly like a dead pane. The failure now travels as `unread`, and every reader of this record is
/// expected to say WHICH of the two facts it has.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PaneRecord {
    pub pid: Option<u32>,
    pub unread: Option<String>,
}

impl PaneRecord {
    /// The pid as a sentence for a log line or a refusal: never a bare 0 manufactured from a parse failure.
    pub fn describe(&self) -> String {
        match (self.pid, self.unread.as_deref()) {
            (Some(pid), _) => format!("pane pid {pid}"),
            (None, Some(reason)) => format!("no readable pane pid ({reason})"),
            (None, None) => "no pane pid recorded".to_string(),
        }
    }
}

impl From<Result<u32, String>> for PaneRecord {
    /// The ONE place a read becomes a record, so that "the read failed" and "the pid is 0" cannot be confused
    /// by a caller writing its own match.
    fn from(read: Result<u32, String>) -> Self {
        match read {
            Ok(pid) => PaneRecord { pid: Some(pid), unread: None },
            Err(reason) => PaneRecord { pid: None, unread: Some(reason) },
        }
    }
}

/// The pane pid tmux reports for a session, or the read's failure in words a reader can act on.
///
/// The exit status is CHECKED and tmux's stderr is carried: `list-panes` refusing (a session that is not there,
/// a server that is not answering) and a pane whose pid is genuinely 0 are different facts, and only one of them
/// means the pane is gone.
pub fn pane_pid(session_name: &str) -> Result<u32, String> {
    let out = tmux_command()
        .args(["list-panes", "-t", session_name, "-F", "#{pane_pid}"])
        .output()
        .map_err(|e| format!("tmux list-panes could not be run: {e}"))?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
        return Err(if stderr.is_empty() {
            format!("tmux list-panes exited {} with nothing on stderr", out.status)
        } else {
            format!("tmux list-panes refused: {stderr}")
        });
    }
    parse_pane_pid(&String::from_utf8_lossy(&out.stdout))
}

/// Whether tmux reports the session's pane as dead, or the read's failure in its own words - the same split
/// `PaneRecord` keeps for the pid, and for the same measured reason.
fn pane_dead(session_name: &str) -> Result<bool, String> {
    let out = tmux_command()
        .args(["list-panes", "-t", session_name, "-F", "#{pane_dead}"])
        .output()
        .map_err(|e| format!("tmux list-panes could not be run: {e}"))?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
        return Err(if stderr.is_empty() {
            format!("tmux list-panes exited {} with nothing on stderr", out.status)
        } else {
            format!("tmux list-panes refused: {stderr}")
        });
    }
    match String::from_utf8_lossy(&out.stdout).lines().next().unwrap_or("").trim() {
        "0" => Ok(false),
        "1" => Ok(true),
        other => Err(format!("tmux reported an unreadable pane state {other:?}")),
    }
}

/// The one line `list-panes -F #{pane_pid}` is expected to answer, parsed without inventing a value.
///
/// Separate from the process call so both branches can be tested without a tmux server: "0" is a pane whose
/// process is gone and parses, and anything tmux might print instead is a FAILED READ rather than a pid.
fn parse_pane_pid(stdout: &str) -> Result<u32, String> {
    let answer = stdout.lines().next().unwrap_or("").trim();
    answer
        .parse::<u32>()
        .map_err(|_| format!("tmux list-panes answered {answer:?}, which is not a pane pid"))
}

impl TerminalManager {
    /// Launch an agent runtime inside a dedicated tmux session.
    pub fn launch_session(
        session_name: &str,
        cwd: &Path,
        runtime_cmd: &str,
        env_vars: &HashMap<String, String>,
    ) -> Result<PaneRecord, String> {
        info!("launching session {} in cwd {}", session_name, cwd.display());

        // The server first, and from a client that carries none of this seat's environment: whoever creates it
        // keeps its argv (see `ensure_server`), and every call below connects to the server this one started.
        ensure_server()?;

        // Check if session already exists
        let check = tmux_command()
            .args(["has-session", "-t", session_name])
            .output();

        if let Ok(out) = check {
            if out.status.success() {
                warn!("session {} already exists; killing first", session_name);
                let _ = tmux_command().args(["kill-session", "-t", session_name]).output();
            }
        }

        // The tmux SERVER's global environment is the half no client-side scrub can reach, and the `-e` list
        // cannot out-vote it: a server that already exists keeps whatever it was created with, a pane takes the
        // server's environment, and a key merely left out of `-e` still arrives. Measured on this box
        // 2026-10-15 with the server up: `tmux show-environment -g` held AGENTHUB_CONNECTOR_TOKEN while this
        // runtime was the only thing creating sessions. The server exists by this point - `ensure_server` above
        // starts it, from an already-scrubbed client, when it is not there, and `has-session` cannot create one
        // now that it is - so this loop can only ever clean a server, never make a dirty one. A key the server
        // never held is not an error.
        //
        // The credential class is cleaned HERE rather than by the `env -u` wrapper below: the wrapper prints the
        // key's NAME on the pane's command line for the seat's whole life, which is the surface the owner's own
        // measurement greps, and a deny list that names the credential would publish it. Prefix keys carry no
        // such cost, and the wrapper is what covers them.
        for key in crate::denied_name_keys() {
            let _ = tmux_command()
                .args(["set-environment", "-g", "-u", &key])
                .output();
        }

        // Prepare new tmux session detached
        let mut cmd = tmux_command();
        cmd.args(["new-session", "-d", "-s", session_name, "-c"]);
        cmd.arg(cwd.as_os_str());

        // Inject env variables into the session with `-e`. Setting them on the tmux CLIENT does not work:
        // tmux builds a new session's environment from the server's global environment, which is whatever
        // the first seat's launch created it with, so every later seat came up with the first seat's
        // AGENTHUB_SEAT/ROLE/SESSION_NAME (measured: dev and reviewer ran as `lead`). A denied key is
        // dropped even when a caller supplied it: the default deny is the point, and "this seat needs to
        // address another rig" is the case being closed.
        cmd.args(pane_env_args(env_vars));

        // The pane command is exec'd through `env -u <key>` for every denied variable this process carries,
        // so the runner — and every agent it spawns, which is what `scrub_env` covers — starts without the
        // live rig's addressing. Doing it at exec rather than after the fact is deliberate:
        // `/proc/<pid>/environ` is the environment the kernel was given, which is where the design's
        // predicate is measured, and tmux hands a pane its session environment from the tmux *server* when
        // the variable is not in the client's — a server the live rig may have started.
        // `runtime_cmd` arrives as ONE string and tmux hands it to the shell, so the wrapper has to stay a
        // single string too: passing `env` as its own argv element makes `env` treat the whole command as a
        // program name, and the pane dies instantly (which is how the first version of this failed - visible
        // as a room that starts no seat at all).
        cmd.arg(pane_command(&crate::denied_env_keys(), runtime_cmd));

        // The tmux *client* is ours and short-lived, but it is a process of this launch and it carries the
        // daemon's environment: measured, it held the host's addressing variables while the runner and the agent
        // it starts were clean. Scrubbing it here costs nothing and leaves no process in the launch carrying
        // the live rig's addressing.
        crate::scrub_env(&mut cmd);
        let output = cmd.output().map_err(|e| format!("failed to start tmux: {e}"))?;
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("tmux new-session failed: {stderr}"));
        }

        // What tmux says about the pane it created: the pid, or the read's failure in its own words. Never a
        // bare 0 - see PaneRecord.
        let pane = PaneRecord::from(pane_pid(session_name));
        if let Some(reason) = pane.unread.as_deref() {
            warn!("session {session_name} was created but its pane pid could not be read: {reason}");
        }

        info!("session {} started with {}", session_name, pane.describe());
        Ok(pane)
    }

    /// Stop an active session.
    pub fn stop_session(session_name: &str) -> Result<(), String> {
        let output = tmux_command()
            .args(["kill-session", "-t", session_name])
            .output()
            .map_err(|e| format!("failed to kill tmux session: {e}"))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            // Ignore session not found. `no current target` is the SAME fact on a server that holds no sessions
            // at all - measured 2026-10-18 with `exit-empty off`, a `kill-session -t <name>` on a live, empty
            // server answers `no current target` rather than `can't find session` - and a reseat that refuses
            // there would refuse to start the first seat of a room whose server has no seats yet.
            if !stderr.contains("can't find session")
                && !stderr.contains("no server running")
                && !stderr.contains("no current target")
            {
                return Err(format!("tmux kill-session failed: {stderr}"));
            }
        }
        Ok(())
    }

    /// Send text reliably using bracketed paste to avoid mangling escape chars and newlines.
    pub fn send_text(session_name: &str, text: &str) -> Result<SendOutcome, String> {
        let temp_dir = std::env::temp_dir();
        let buf_id = format!("agenthub-buf-{}", uuid::Uuid::new_v4());
        let temp_file = temp_dir.join(format!("{buf_id}.txt"));

        fs::write(&temp_file, text).map_err(|e| format!("failed to write temp paste file: {e}"))?;

        // 1. Load buffer
        let load = tmux_command()
            .args(["load-buffer", "-b", &buf_id])
            .arg(temp_file.as_os_str())
            .output()
            .map_err(|e| format!("tmux load-buffer failed: {e}"))?;

        if !load.status.success() {
            let _ = fs::remove_file(&temp_file);
            return Err(format!("tmux load-buffer failed: {}", String::from_utf8_lossy(&load.stderr)));
        }

        // 2. Paste buffer with -p (bracketed), -r (raw LF), -d (drop buffer after paste)
        let paste = tmux_command()
            .args(["paste-buffer", "-t", session_name, "-b", &buf_id, "-d", "-r", "-p"])
            .output();

        let _ = fs::remove_file(&temp_file);

        let paste_out = paste.map_err(|e| format!("tmux paste-buffer failed: {e}"))?;
        if !paste_out.status.success() {
            return Err(format!("tmux paste-buffer failed: {}", String::from_utf8_lossy(&paste_out.stderr)));
        }

        // 3. Press Enter, then check the pane moved. A TUI that was not reading when the paste landed
        // leaves the text staged in its input box, and tmux reports success either way.
        let before = Self::capture_pane(session_name, SUBMIT_CAPTURE_LINES).ok();
        confirm_submission(
            before,
            || Self::press_enter(session_name),
            || Self::capture_pane(session_name, SUBMIT_CAPTURE_LINES).ok(),
            SUBMIT_SETTLE,
        )
    }

    fn press_enter(session_name: &str) -> Result<(), String> {
        let enter = tmux_command()
            .args(["send-keys", "-t", session_name, "Enter"])
            .output()
            .map_err(|e| format!("tmux send-keys Enter failed: {e}"))?;
        if enter.status.success() {
            Ok(())
        } else {
            Err(format!("tmux send-keys Enter failed: {}", String::from_utf8_lossy(&enter.stderr)))
        }
    }

    /// Check if a session is currently running.
    pub fn is_session_alive(session_name: &str) -> bool {
        let out = tmux_command()
            .args(["has-session", "-t", session_name])
            .output();
        out.map(|o| o.status.success()).unwrap_or(false)
    }

    /// THE PANE'S OWN LIVENESS, the strongest proof a runtime with no runner can give: the session exists AND
    /// tmux does not report its pane dead.
    ///
    /// `#{pane_dead}` is the only field that answers the second half. Measured here 2026-10-17: a pane kept by
    /// `remain-on-exit` after its command exited still answers `has-session` 0 and still reports the PID of the
    /// dead process (`118226 1` from `list-panes`), so neither the session nor a nonzero pid can tell the two
    /// apart. A pane that exits without `remain-on-exit` takes the session with it, and then the first half
    /// answers. A FAILED READ IS NOT A DEATH (the rule `dce05c8` measured for the pid): only tmux reporting the
    /// pane dead excludes here, because a probe that failed must not silence a live seat.
    pub fn pane_is_live(session_name: &str) -> bool {
        Self::is_session_alive(session_name) && !matches!(pane_dead(session_name), Ok(true))
    }

    /// Capture pane text scrollback for telemetry/watch.
    pub fn capture_pane(session_name: &str, lines: usize) -> Result<String, String> {
        let lines_arg = format!("-{lines}");
        let out = tmux_command()
            .args(["capture-pane", "-p", "-t", session_name, "-S", &lines_arg])
            .output()
            .map_err(|e| format!("tmux capture-pane failed: {e}"))?;

        if out.status.success() {
            Ok(String::from_utf8_lossy(&out.stdout).to_string())
        } else {
            Err(String::from_utf8_lossy(&out.stderr).to_string())
        }
    }
}

#[cfg(test)]
mod submit_tests {
    use super::*;
    use std::cell::Cell;

    const ZERO: Duration = Duration::ZERO;

    #[test]
    fn a_pane_that_changes_after_enter_is_submitted_with_one_press() {
        let presses = Cell::new(0);
        let out = confirm_submission(
            Some("> hello".into()),
            || { presses.set(presses.get() + 1); Ok(()) },
            || Some("> ".into()),
            ZERO,
        );
        assert_eq!(out, Ok(SendOutcome::Submitted));
        assert_eq!(presses.get(), 1);
    }

    #[test]
    fn a_staged_pane_gets_one_retry_then_submits() {
        let presses = Cell::new(0);
        let out = confirm_submission(
            Some("> hello".into()),
            || { presses.set(presses.get() + 1); Ok(()) },
            || Some(if presses.get() < 2 { "> hello".into() } else { "> ".into() }),
            ZERO,
        );
        assert_eq!(out, Ok(SendOutcome::Submitted));
        assert_eq!(presses.get(), 2);
    }

    #[test]
    fn a_pane_that_never_moves_is_unconfirmed_after_two_presses() {
        let presses = Cell::new(0);
        let out = confirm_submission(
            Some("> hello".into()),
            || { presses.set(presses.get() + 1); Ok(()) },
            || Some("> hello".into()),
            ZERO,
        );
        assert_eq!(out, Ok(SendOutcome::Unconfirmed));
        assert_eq!(presses.get(), 2);
    }

    #[test]
    fn an_unreadable_pane_is_unconfirmed_not_submitted() {
        assert_eq!(
            confirm_submission(None, || Ok(()), || None, ZERO),
            Ok(SendOutcome::Unconfirmed)
        );
        assert_eq!(
            confirm_submission(Some("x".into()), || Ok(()), || None, ZERO),
            Ok(SendOutcome::Unconfirmed)
        );
    }

    #[test]
    fn a_failed_enter_is_an_error() {
        let out = confirm_submission(Some("x".into()), || Err("boom".into()), || None, ZERO);
        assert_eq!(out, Err("boom".to_string()));
    }

    #[test]
    fn outcomes_serialize_kebab_case() {
        assert_eq!(serde_json::to_string(&SendOutcome::Submitted).unwrap(), "\"submitted\"");
        assert_eq!(serde_json::to_string(&SendOutcome::Unconfirmed).unwrap(), "\"unconfirmed\"");
    }
}

#[cfg(test)]
mod launch_env_tests {
    use super::*;

    /// The second seat launched into a server the first one created must carry its OWN identity. tmux
    /// builds a session's environment from the server's, so this fails whenever the variables ride the
    /// client's environment instead of `new-session -e`.
    #[test]
    fn the_pane_command_unsets_the_tmux_pane_variables_and_the_denied_keys() {
        assert_eq!(
            pane_command(&["RIGGED_A".to_string()], "/bin/runner x"),
            "env -u RIGGED_A -u TMUX -u TMUX_PANE /bin/runner x"
        );
        assert_eq!(pane_command(&[], "/bin/runner x"), "env -u TMUX -u TMUX_PANE /bin/runner x");
    }

    /// The two mechanisms are kept apart on purpose. The credential is denied by NAME and cleaned off the tmux
    /// SERVER, so it must never appear in `denied_env_keys`: that list is what the `env -u` wrapper prints on
    /// the pane's command line, and the owner's measurement greps exactly that line. A credential routed to the
    /// wrong mechanism breaks the property the fix is for.
    #[test]
    fn the_credential_is_cleaned_by_the_server_and_never_named_on_the_pane_command() {
        std::env::set_var("AGENTHUB_CONNECTOR_TOKEN", "set-by-this-test");
        assert!(crate::env_is_denied("AGENTHUB_CONNECTOR_TOKEN"));
        assert!(crate::denied_name_keys().contains(&"AGENTHUB_CONNECTOR_TOKEN".to_string()));
        assert!(!crate::denied_env_keys().contains(&"AGENTHUB_CONNECTOR_TOKEN".to_string()));
        assert!(!pane_command(&crate::denied_env_keys(), "/bin/runner x").contains("CONNECTOR_TOKEN"));
        std::env::remove_var("AGENTHUB_CONNECTOR_TOKEN");
    }

    /// The `-e` list is the leak's other half: a seat's env map is the state root's whole `.env`, so the
    /// credential would ride `tmux new-session -e AGENTHUB_CONNECTOR_TOKEN=<value>` where `ps` reads it - while
    /// the keys a seat actually reads (`AGENTHUB_TOKEN` for its own MCP config) survive the filter.
    #[test]
    fn a_denied_credential_never_becomes_a_pane_env_argument() {
        let env = HashMap::from([
            ("AGENTHUB_CONNECTOR_TOKEN".to_string(), "connector".to_string()),
            ("AGENTHUB_TOKEN".to_string(), "mcp".to_string()),
            ("AGENTHUB_SEAT".to_string(), "dev".to_string()),
            ("RIGGED_SESSION_NAME".to_string(), "elsewhere".to_string()),
        ]);
        let args = pane_env_args(&env);
        assert!(!args.iter().any(|a| a.contains("AGENTHUB_CONNECTOR_TOKEN")), "{args:?}");
        assert!(!args.iter().any(|a| a.contains("RIGGED_SESSION_NAME")), "{args:?}");
        assert!(args.contains(&"AGENTHUB_SEAT=dev".to_string()), "{args:?}");
        assert!(args.contains(&"AGENTHUB_TOKEN=mcp".to_string()), "{args:?}");
    }

    #[test]
    fn each_session_gets_its_own_env_not_the_servers() {
        let _server = super::test_servers::own("launch-env");
        let dir = std::env::temp_dir().join(format!("pty-launch-env-{}", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        let sessions: Vec<(String, std::path::PathBuf)> = ["first", "second"]
            .iter()
            .map(|seat| {
                let session = format!("pty-launch-env-{}-{}", std::process::id(), seat);
                let out = dir.join(seat);
                let env = HashMap::from([("AGENTHUB_SEAT".to_string(), seat.to_string())]);
                let cmd = format!("sh -c 'printf %s \"$AGENTHUB_SEAT\" > {}; sleep 5'", out.display());
                TerminalManager::launch_session(&session, &dir, &cmd, &env).unwrap();
                (session, out)
            })
            .collect();

        for (seat, (_, out)) in ["first", "second"].iter().zip(&sessions) {
            let deadline = std::time::Instant::now() + Duration::from_secs(5);
            while !out.exists() && std::time::Instant::now() < deadline {
                std::thread::sleep(Duration::from_millis(50));
            }
            assert_eq!(fs::read_to_string(out).unwrap_or_default(), *seat);
        }
        for (session, _) in &sessions {
            let _ = TerminalManager::stop_session(session);
        }
        let _ = fs::remove_dir_all(&dir);
    }

    /// A value the test itself writes, so a hit on a command line can only have come from the launch.
    const SECRET_IN_THE_ENV: &str = "sk-the-owner-would-grep-this";

    /// THE OWNER'S MEASUREMENT, pinned: after a launch, the tmux SERVER's command line holds none of the seat's
    /// environment - and the pane still holds all of it.
    ///
    /// The server is the one process of a room that outlives every seat, and `/proc/<server pid>/cmdline` is
    /// world-readable, so a `-e KEY=VALUE` that lands on the CREATING client's argv is a credential published
    /// for the room's whole life: measured on this box 2026-10-16, the live room's server carried
    /// `DEEPSEEK_API_KEY=`, `TYPESAFE_API_KEY=` and `AGENTHUB_TOKEN=`, and it carried them because the first
    /// seat's `new-session` had created that server. The second half is what the fix must not cost: the pane
    /// still receives the value, because what moved is WHICH process's argv the pair is written on.
    #[test]
    fn the_server_is_not_the_process_that_carries_the_seats_environment() {
        if !Path::new("/proc/self").exists() {
            eprintln!("no /proc: this case IS that measurement, so it cannot be taken here");
            return;
        }
        let server = super::test_servers::own("server-argv");
        let dir = std::env::temp_dir().join(format!("pty-server-argv-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let session = format!("pty-server-argv-{}", std::process::id());
        let delivered = dir.join("seat-env");
        let env = HashMap::from([
            ("DEEPSEEK_API_KEY".to_string(), SECRET_IN_THE_ENV.to_string()),
            ("AGENTHUB_SEAT".to_string(), "dev".to_string()),
        ]);
        let cmd = format!(
            "sh -c 'printf %s \"$DEEPSEEK_API_KEY\" > {}; sleep 5'",
            delivered.display()
        );
        TerminalManager::launch_session(&session, &dir, &cmd, &env).unwrap();

        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while !delivered.exists() && std::time::Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(50));
        }
        assert_eq!(
            fs::read_to_string(&delivered).unwrap_or_default(),
            SECRET_IN_THE_ENV,
            "the seat must still get its own environment: the fix is about the server's argv, not the delivery"
        );

        let argv = server_argv(server.root()).expect("the root's own tmux server");
        assert!(argv.len() > 1, "a one-word argv is not a server's: {argv:?}");
        for arg in &argv {
            assert!(
                !arg.contains(SECRET_IN_THE_ENV),
                "the seat's value is on the server's command line, where `ps` reads it: {arg}"
            );
            assert!(
                !credential_shaped(arg),
                "a `NAME=VALUE` whose name is a credential rides the server's command line: {arg}"
            );
        }

        let _ = TerminalManager::stop_session(&session);
        let _ = fs::remove_dir_all(&dir);
    }

    /// An empty server's `kill-session` answers `no current target`, which is the same fact as `can't find
    /// session` for a server that holds seats: there is nothing to stop. Measured 2026-10-18 on a root whose
    /// server was up with `exit-empty off` and no sessions - a reseat of a room's first seat died there with
    /// `tmux kill-session failed: no current target` before it could start anything.
    #[test]
    fn a_stop_on_a_server_holding_no_sessions_is_nothing_to_stop() {
        let _server = super::test_servers::own("stop-empty");
        super::ensure_server().expect("a server with no sessions");
        assert!(
            TerminalManager::stop_session("agenthub--nowhere--nobody").is_ok(),
            "an empty server is not a refusal to stop a session that is not there"
        );
    }

    /// The umask the runtime takes reaches a pane's command, and through it the agent that writes the
    /// transcripts.
    ///
    /// WHY THE PANE AND NOT THIS PROCESS: the runtime's own files are the small half of the state root. The
    /// large half is written by the AGENT inside a pane - a seat's transcript is the newest `*.jsonl` under its
    /// sessions directory, and it records whatever the seat printed - so the honest measurement is what a
    /// command RUN IN A PANE reports, not what this test process holds. The chain the assertion rides is
    /// fork/exec: this process, the tmux client it starts, the server that client creates, the pane's shell.
    #[test]
    fn a_pane_runs_under_the_umask_the_runtime_took() {
        crate::restrict_umask();
        let _server = super::test_servers::own("umask-chain");
        let dir = std::env::temp_dir().join(format!("pty-umask-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let session = format!("pty-umask-{}", std::process::id());
        let reported = dir.join("umask");
        let cmd = format!("sh -c 'umask > {}; sleep 5'", reported.display());
        TerminalManager::launch_session(&session, &dir, &cmd, &HashMap::new()).unwrap();

        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while !reported.exists() && std::time::Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(50));
        }
        assert_eq!(
            fs::read_to_string(&reported).unwrap_or_default().trim(),
            "0077",
            "a pane's command must create nothing the group or another user can read"
        );

        let _ = TerminalManager::stop_session(&session);
        let _ = fs::remove_dir_all(&dir);
    }

    /// The owner's own predicate, kept as written: a `KEY=VALUE` whose key names a `TOKEN`, `KEY`, `SECRET` or
    /// `PASSWORD`. The bootstrap's own command has no `=` on it at all, so a hit here is a seat's environment.
    fn credential_shaped(arg: &str) -> bool {
        let Some((name, _)) = arg.split_once('=') else {
            return false;
        };
        ["TOKEN", "KEY", "SECRET", "PASSWORD"].iter().any(|word| name.contains(word))
    }

    /// The command line of THIS root's tmux server, read the way the owner's measurement reads it: the server is
    /// the `tmux: server` process whose environment names this root, and its argv is world-readable.
    fn server_argv(root: &Path) -> Option<Vec<String>> {
        let wanted = format!("TMUX_TMPDIR={}", root.display());
        let mut pid = None;
        for entry in fs::read_dir("/proc").ok()?.flatten() {
            let Ok(candidate) = entry.file_name().to_string_lossy().parse::<u32>() else {
                continue;
            };
            let process = entry.path();
            let is_server = fs::read_to_string(process.join("comm"))
                .map(|comm| comm.trim() == "tmux: server")
                .unwrap_or(false);
            if !is_server {
                continue;
            }
            let environ = fs::read(process.join("environ")).unwrap_or_default();
            if environ.split(|b| *b == 0).any(|kv| kv == wanted.as_bytes()) {
                pid = Some(candidate);
                break;
            }
        }
        let argv = fs::read(format!("/proc/{}/cmdline", pid?)).ok()?;
        Some(
            argv.split(|b| *b == 0)
                .filter(|arg| !arg.is_empty())
                .map(|arg| String::from_utf8_lossy(arg).to_string())
                .collect(),
        )
    }
}

/// ONE lock over the process-wide `TMUX_TMPDIR`, and a tmux server directory of the caller's own.
///
/// The shared state is not a tmux quirk, it is `own_tmux_root`'s write to `std::env`: every tmux client this
/// runtime starts resolves WHICH server it talks to from that variable **at exec time**. Measured 2026-10-15 by
/// pointing one client at another directory while its session was alive: `tmux ls` answers "error connecting to
/// <dir>/tmux-1000/default (No such file or directory)" - a live session reported as gone. So tests that open
/// real panes share one server, and
/// `main::tests::the_tmux_root_is_derived_from_the_state_dir_and_created_private` moves the variable to its own
/// scratch directory and DELETES that directory when it finishes. Under the harness's default parallelism that
/// is what made the two pane-spawning supervisor tests report "its pane is gone and no ready frame ever
/// arrived": their session was fine, their NEXT tmux client was looking somewhere else.
///
/// The lock is the fix, and it lives in the TESTS' own setup rather than in the suite invocation: a test that
/// owns a root for its scratch state dir takes this first, points the process at a server of its own, and no
/// other pane test is in flight while it does.
#[cfg(test)]
pub(crate) mod test_servers {
    use std::ffi::OsString;
    use std::path::{Path, PathBuf};
    use std::sync::{Mutex, MutexGuard};

    static TMUX_ENV: Mutex<()> = Mutex::new(());

    /// One test's own tmux root, and the lock that keeps the process pointed at it.
    ///
    /// The lock is held for the guard's whole life and the root is removed when it drops, which is the property
    /// the tests below rely on: with `pty::ensure_server` setting `exit-empty off`, a server no longer exits
    /// with its last session, so the socket - and this directory - would otherwise outlive every test that
    /// opened a pane.
    pub(crate) struct Server {
        _env: MutexGuard<'static, ()>,
        root: PathBuf,
        /// What `TMUX_TMPDIR` held before this test took it, handed back when the guard drops.
        previous: Option<OsString>,
    }

    impl Server {
        pub(crate) fn root(&self) -> &Path {
            &self.root
        }
    }

    impl Drop for Server {
        fn drop(&mut self) {
            // The variable is set back to THIS root for the kill: it is process-wide, and a `kill-server` that
            // resolved to the room's own server would end every seat in the live room. Then the variable goes
            // back to whatever the previous test (or nothing) had put there.
            std::env::set_var("TMUX_TMPDIR", &self.root);
            let _ = super::tmux_command().args(["kill-server"]).output();
            let _ = std::fs::remove_dir_all(&self.root);
            match self.previous.take() {
                Some(previous) => std::env::set_var("TMUX_TMPDIR", previous),
                None => std::env::remove_var("TMUX_TMPDIR"),
            }
        }
    }

    /// Hold the process's tmux root for the rest of the test, pointed at a scratch server of this test's own.
    /// The guard must stay alive while the test opens panes.
    pub(crate) fn own(tag: &str) -> Server {
        let guard = TMUX_ENV.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
        let root = std::env::temp_dir().join(format!("agenthub-tmux-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).expect("scratch tmux server dir");
        let previous = std::env::var_os("TMUX_TMPDIR");
        std::env::set_var("TMUX_TMPDIR", &root);
        Server {
            _env: guard,
            root,
            previous,
        }
    }
}

#[cfg(test)]
mod pane_pid_tests {
    use super::*;

    /// THE FALSIFICATION THE ORDER ASKS FOR: the read fails, and the failure is not a pid.
    ///
    /// Both halves of the boundary are pinned here, because they were one value: an answer tmux might give
    /// instead of a pid is a FAILED READ, and a pane whose process is gone - 0 - is a PID that parses.
    #[test]
    fn an_unparsable_answer_is_a_failed_read_not_a_pid_of_zero() {
        for answer in ["", "\n", "no server running on /tmp/tmux-1000/default", "not-a-pid", "7958 extra"] {
            let err = parse_pane_pid(answer).expect_err("this is not a pane pid");
            assert!(err.contains("which is not a pane pid"), "{err}");
            assert!(!err.contains("pane pid 0"), "{err}");
        }
        assert_eq!(parse_pane_pid("0\n"), Ok(0));
        assert_eq!(parse_pane_pid("7958\n"), Ok(7958));
        assert_eq!(parse_pane_pid(" 8167 "), Ok(8167));
    }

    /// Against real tmux: a session that is not there is refused in TMUX'S OWN WORDS, and the refusal does not
    /// read as a pane pid of 0.
    #[test]
    fn a_session_that_is_not_there_is_refused_in_tmuxs_own_words() {
        let session = format!("pane-pid-absent-{}", std::process::id());
        let err = pane_pid(&session).expect_err("a session that does not exist has no pane pid");
        assert!(
            err.contains("tmux list-panes refused") || err.contains("could not be run"),
            "the read's failure must travel in its own words: {err}"
        );
        assert!(!err.contains("pane pid 0"), "{err}");
    }

    /// And a session that IS there hands back a pid the record describes as one - the value the supervisor now
    /// reports instead of a manufactured 0.
    #[test]
    fn a_live_session_answers_a_pid_and_the_record_describes_it() {
        // A LIVE pane for the whole read, so this case holds the process's tmux root for a scratch server of its
        // own the way every other pane-opening case does: `TMUX_TMPDIR` is process-wide, and a guarded test that
        // drops its root runs `kill-server` against it - which ended this case's session.
        let _server = test_servers::own("pane-pid-live");
        let dir = std::env::temp_dir().join(format!("pty-pane-pid-{}", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        let session = format!("pane-pid-live-{}", std::process::id());
        let record = TerminalManager::launch_session(&session, &dir, "sleep 5", &HashMap::new()).unwrap();
        assert!(record.pid.is_some(), "a live pane must answer a pid: {record:?}");
        assert!(record.unread.is_none(), "{record:?}");
        assert!(record.describe().starts_with("pane pid "), "{}", record.describe());
        let _ = TerminalManager::stop_session(&session);
        let _ = fs::remove_dir_all(&dir);
    }

    /// The description never invents a pid: an unknown pid says so and does not print a 0.
    #[test]
    fn an_unknown_pid_describes_its_failure() {
        let record = PaneRecord {
            pid: None,
            unread: Some("tmux list-panes refused: no server running".into()),
        };
        let described = record.describe();
        assert!(described.contains("no readable pane pid"), "{described}");
        assert!(described.contains("no server running"), "{described}");
        assert!(!described.contains('0'), "{described}");
    }

    /// A READ THAT FAILED BECOMES THE RECORD'S OWN WORDS, and a read that succeeded becomes a pid - the one
    /// boundary the supervisor and the log both read, so neither can turn a failure into a 0 on its own.
    #[test]
    fn a_failed_read_becomes_words_and_a_successful_one_becomes_a_pid() {
        let failed = PaneRecord::from(Err("tmux list-panes refused: can't find session".to_string()));
        assert_eq!(failed.pid, None);
        assert_eq!(
            failed.unread.as_deref(),
            Some("tmux list-panes refused: can't find session")
        );
        assert!(failed.describe().contains("can't find session"), "{}", failed.describe());

        let read = PaneRecord::from(Ok(8167));
        assert_eq!(read.pid, Some(8167));
        assert_eq!(read.unread, None);
        assert_eq!(read.describe(), "pane pid 8167");

        // The value the old code manufactured: a genuine 0 is a PID, and it stays distinguishable from a
        // failed read because it arrives as Ok.
        let zero = PaneRecord::from(Ok(0));
        assert_eq!(zero.pid, Some(0));
        assert_eq!(zero.unread, None);
        assert_eq!(zero.describe(), "pane pid 0");
    }
}
