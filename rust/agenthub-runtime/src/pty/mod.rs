use std::collections::HashMap;
use std::fs;
use std::io::Write;
use std::path::Path;
use std::process::Command;
use std::time::Duration;
use tracing::{error, info, warn};

pub struct TerminalManager;

/// Every tmux call this module makes goes through here, for one reason expressed twice: the client must not
/// carry this runtime's denied addressing into the server it may create, and it must reach the server this
/// runtime OWNS rather than one the host happens to be running. Both halves are load-bearing:
/// `TMUX_TMPDIR` (set once at startup, see `crate::own_tmux_root`) decides WHICH server, and the scrub
/// decides whether that server starts clean - measured, a private server created by an unscrubbed client
/// carried the host's variables just as the shared one did (7 of them), because a server takes the
/// environment of whoever creates it. The first tmux call in `launch_session` runs before any other scrub,
/// and it is exactly the call that would create ours.
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

fn tmux_command() -> Command {
    let mut cmd = Command::new("tmux");
    crate::scrub_env(&mut cmd);
    cmd
}

impl TerminalManager {
    /// Launch an agent runtime inside a dedicated tmux session.
    pub fn launch_session(
        session_name: &str,
        cwd: &Path,
        runtime_cmd: &str,
        env_vars: &HashMap<String, String>,
    ) -> Result<u32, String> {
        info!("launching session {} in cwd {}", session_name, cwd.display());

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
        for (k, v) in env_vars {
            if crate::env_is_denied(k) {
                continue;
            }
            cmd.arg("-e").arg(format!("{k}={v}"));
        }

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

        // Get the pane PID
        let pid_out = tmux_command()
            .args(["list-panes", "-t", session_name, "-F", "#{pane_pid}"])
            .output()
            .map_err(|e| format!("failed to list pane pid: {e}"))?;

        let pid_str = String::from_utf8_lossy(&pid_out.stdout).trim().to_string();
        let pid: u32 = pid_str.lines().next().and_then(|l| l.parse().ok()).unwrap_or(0);

        info!("session {} started with pane pid {}", session_name, pid);
        Ok(pid)
    }

    /// Stop an active session.
    pub fn stop_session(session_name: &str) -> Result<(), String> {
        let output = tmux_command()
            .args(["kill-session", "-t", session_name])
            .output()
            .map_err(|e| format!("failed to kill tmux session: {e}"))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            // Ignore session not found
            if !stderr.contains("can't find session") && !stderr.contains("no server running") {
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

    #[test]
    fn each_session_gets_its_own_env_not_the_servers() {
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
}
