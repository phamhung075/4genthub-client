use std::collections::HashMap;
use std::fs;
use std::io::Write;
use std::path::Path;
use std::process::Command;
use tracing::{error, info, warn};

pub struct TerminalManager;

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
        let check = Command::new("tmux")
            .args(["has-session", "-t", session_name])
            .output();

        if let Ok(out) = check {
            if out.status.success() {
                warn!("session {} already exists; killing first", session_name);
                let _ = Command::new("tmux").args(["kill-session", "-t", session_name]).output();
            }
        }

        // Prepare new tmux session detached
        let mut cmd = Command::new("tmux");
        cmd.args(["new-session", "-d", "-s", session_name, "-c"]);
        cmd.arg(cwd.as_os_str());

        // Inject env variables into session. A denied key is dropped even when a caller supplied it: the
        // default deny is the point, and "this seat needs to address another rig" is the case being closed.
        for (k, v) in env_vars {
            if crate::env_is_denied(k) {
                continue;
            }
            cmd.env(k, v);
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
        let denied = crate::denied_env_keys();
        if denied.is_empty() {
            cmd.arg(runtime_cmd);
        } else {
            let mut wrapped = String::from("env");
            for key in &denied {
                wrapped.push_str(" -u ");
                wrapped.push_str(key);
            }
            wrapped.push(' ');
            wrapped.push_str(runtime_cmd);
            cmd.arg(wrapped);
        }

        let output = cmd.output().map_err(|e| format!("failed to start tmux: {e}"))?;
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(format!("tmux new-session failed: {stderr}"));
        }

        // Get the pane PID
        let pid_out = Command::new("tmux")
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
        let output = Command::new("tmux")
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
    pub fn send_text(session_name: &str, text: &str) -> Result<(), String> {
        let temp_dir = std::env::temp_dir();
        let buf_id = format!("agenthub-buf-{}", uuid::Uuid::new_v4());
        let temp_file = temp_dir.join(format!("{buf_id}.txt"));

        fs::write(&temp_file, text).map_err(|e| format!("failed to write temp paste file: {e}"))?;

        // 1. Load buffer
        let load = Command::new("tmux")
            .args(["load-buffer", "-b", &buf_id])
            .arg(temp_file.as_os_str())
            .output()
            .map_err(|e| format!("tmux load-buffer failed: {e}"))?;

        if !load.status.success() {
            let _ = fs::remove_file(&temp_file);
            return Err(format!("tmux load-buffer failed: {}", String::from_utf8_lossy(&load.stderr)));
        }

        // 2. Paste buffer with -p (bracketed), -r (raw LF), -d (drop buffer after paste)
        let paste = Command::new("tmux")
            .args(["paste-buffer", "-t", session_name, "-b", &buf_id, "-d", "-r", "-p"])
            .output();

        let _ = fs::remove_file(&temp_file);

        let paste_out = paste.map_err(|e| format!("tmux paste-buffer failed: {e}"))?;
        if !paste_out.status.success() {
            return Err(format!("tmux paste-buffer failed: {}", String::from_utf8_lossy(&paste_out.stderr)));
        }

        // 3. Send Enter key
        let enter = Command::new("tmux")
            .args(["send-keys", "-t", session_name, "Enter"])
            .output()
            .map_err(|e| format!("tmux send-keys Enter failed: {e}"))?;

        if !enter.status.success() {
            return Err(format!("tmux send-keys Enter failed: {}", String::from_utf8_lossy(&enter.stderr)));
        }

        Ok(())
    }

    /// Check if a session is currently running.
    pub fn is_session_alive(session_name: &str) -> bool {
        let out = Command::new("tmux")
            .args(["has-session", "-t", session_name])
            .output();
        out.map(|o| o.status.success()).unwrap_or(false)
    }

    /// Capture pane text scrollback for telemetry/watch.
    pub fn capture_pane(session_name: &str, lines: usize) -> Result<String, String> {
        let lines_arg = format!("-{lines}");
        let out = Command::new("tmux")
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
