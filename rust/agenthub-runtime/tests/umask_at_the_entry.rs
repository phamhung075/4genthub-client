//! The umask is taken at the runtime's OWN entry point, and this is the case that pins that call.
//!
//! WHY IT RUNS THE BINARY: `restrict_umask` already has a case of its own, and that case calls the
//! function directly (`pty::launch_env_tests::a_pane_runs_under_the_umask_the_runtime_took`), so it stays
//! green when the call in `main` is deleted. The call site is what covers the whole tree - the runtime's
//! own writes, the tmux client it starts, the server that client creates, each pane's shell and the agent
//! in it, which is where the seats' transcripts come from.
//!
//! WHY THE DAEMON: a process's umask can only be read while the process is alive (`/proc/<pid>/status`),
//! and `daemon` is the one subcommand that stays up long enough to be read - the socket it binds is the
//! readiness signal. `whoami` would answer and exit first.
#![cfg(target_os = "linux")]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

/// What the tree must run under: nothing the group or another user can read, and the owner keeps all
/// three bits. `0022` is the value the defect was measured under and the one the shell wrapper below
/// hands down, so what the assertion reads is the runtime's own call and not this seat's tree (0077).
const INHERITED: &str = "0022";
const WANT: &str = "0077";

#[test]
fn the_entry_point_takes_the_umask_the_whole_tree_inherits() {
    let scratch =
        std::env::temp_dir().join(format!("agenthub-runtime-umask-{}", std::process::id()));
    let _ = fs::remove_dir_all(&scratch);
    fs::create_dir_all(&scratch).expect("scratch root");
    let state = scratch.join("state");
    let socket = scratch.join("daemon.sock");
    let log = scratch.join("daemon.log");

    let mut child = Command::new("/bin/sh")
        .arg("-c")
        .arg(r#"umask 022; exec "$0" "$@""#)
        .arg(env!("CARGO_BIN_EXE_agenthub-runtime"))
        .arg("-s")
        .arg(&state)
        .arg("--socket")
        .arg(&socket)
        .arg("daemon")
        .env_clear()
        .env("HOME", &scratch)
        .env("PATH", "/usr/bin:/bin")
        .stdin(Stdio::null())
        .stdout(Stdio::from(fs::File::create(&log).expect("daemon log")))
        .stderr(Stdio::from(fs::File::create(&log).expect("daemon log")))
        .spawn()
        .expect("spawn the built runtime");

    // Readiness: the daemon has taken the umask before it binds, so a socket that exists means the entry
    // has already run.
    let deadline = Instant::now() + Duration::from_secs(10);
    while !socket.exists() {
        if let Some(status) = child.try_wait().expect("poll the daemon") {
            let _ = child.wait();
            let _ = fs::remove_dir_all(&scratch);
            panic!(
                "the daemon exited ({status}) before it bound {}\n{}",
                socket.display(),
                fs::read_to_string(&log).unwrap_or_default()
            );
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            let _ = fs::remove_dir_all(&scratch);
            panic!(
                "the daemon did not bind {} within 10s\n{}",
                socket.display(),
                fs::read_to_string(&log).unwrap_or_default()
            );
        }
        std::thread::sleep(Duration::from_millis(5));
    }

    let status = fs::read_to_string(format!("/proc/{}/status", child.id()))
        .expect("the live daemon's status");
    let got = umask_field(&status);
    let state_mode = mode(&state);
    let _ = child.kill();
    let _ = child.wait();
    let _ = fs::remove_dir_all(&scratch);

    assert_eq!(
        got, WANT,
        "the daemon's own umask is {got:?}, want {WANT:?} - a dropped call leaves it at the inherited {INHERITED:?}"
    );
    assert_eq!(
        state_mode, 0o700,
        "the state root the runtime creates for itself must be owner-only, not {state_mode:o}"
    );
}

/// The value of the `Umask:` line of a /proc/<pid>/status body.
fn umask_field(status: &str) -> String {
    status
        .lines()
        .find_map(|line| line.strip_prefix("Umask:"))
        .map(|value| value.trim().to_string())
        .unwrap_or_default()
}

/// A path's permission bits, without the file-type bits `mode()` also carries.
fn mode(path: &Path) -> u32 {
    fs::metadata(path)
        .expect("the runtime must have created it")
        .permissions()
        .mode()
        & 0o777
}
