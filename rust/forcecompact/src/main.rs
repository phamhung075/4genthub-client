//! forcecompact <session> - send `{"type":"compact"}` to the omp child of an OpenRig seat.
//!
//! A seat is `pi-runner.js` (parent) holding one end of a socketpair whose other end is the stdin of
//! `omp --mode rpc`. The runner turns pane text into an RPC `prompt`, or a `steer` while the seat is
//! streaming, so a `/compact` typed into a working seat is read as text. This tool duplicates the
//! runner's end of the socket with pidfd_getfd(2) and writes the RPC command itself.
//!
//! pidfd_getfd needs PTRACE_MODE_ATTACH on the runner. Under Yama ptrace_scope=1 that means
//! CAP_SYS_PTRACE: `sudo setcap cap_sys_ptrace+ep forcecompact`.

use std::fs::{self, File};
use std::io::Write;
use std::os::fd::FromRawFd;
use std::process::{Command, ExitCode};

const SYS_PIDFD_OPEN: i64 = 434;
const SYS_PIDFD_GETFD: i64 = 438;

extern "C" {
    fn syscall(num: i64, ...) -> i64;
}

fn cmdline(pid: u32) -> Option<String> {
    let raw = fs::read(format!("/proc/{pid}/cmdline")).ok()?;
    Some(String::from_utf8_lossy(&raw).replace('\0', " "))
}

fn pids() -> Vec<u32> {
    fs::read_dir("/proc")
        .into_iter()
        .flatten()
        .filter_map(|e| e.ok()?.file_name().to_str()?.parse().ok())
        .collect()
}

fn ppid(pid: u32) -> Option<u32> {
    let stat = fs::read_to_string(format!("/proc/{pid}/stat")).ok()?;
    // the command name is parenthesised and may contain spaces: parse after the last ')'
    stat.rsplit_once(')')?.1.split_whitespace().nth(1)?.parse().ok()
}

fn socket_inode(pid: u32, fd: &str) -> Option<String> {
    let link = fs::read_link(format!("/proc/{pid}/fd/{fd}")).ok()?;
    link.to_str()?.strip_prefix("socket:[")?.strip_suffix(']').map(String::from)
}

/// Peer inode of a unix socket, from `ss -xH` rows: `u_str ESTAB 0 0 * <local> * <peer>`.
fn peer_inode(inode: &str) -> Option<String> {
    let out = Command::new("ss").args(["-xH"]).output().ok()?;
    String::from_utf8_lossy(&out.stdout).lines().find_map(|l| {
        let f: Vec<&str> = l.split_whitespace().collect();
        (f.len() >= 8 && f[5] == inode).then(|| f[7].to_string())
    })
}

fn fail(msg: String) -> ExitCode {
    eprintln!("forcecompact: {msg}");
    ExitCode::FAILURE
}

fn main() -> ExitCode {
    let session = match std::env::args().nth(1) {
        Some(s) if s != "--help" && s != "-h" => s,
        _ => return fail("usage: forcecompact <rig>-<seat>@<rig>".into()),
    };
    let needle = format!("--session-name {session} ");
    let runner = pids().into_iter().find(|&p| {
        cmdline(p).is_some_and(|c| c.contains("pi-runner.js") && format!("{c} ").contains(&needle))
    });
    let Some(runner) = runner else { return fail(format!("no pi-runner for session {session}")) };

    let omp = pids().into_iter().find(|&p| {
        ppid(p) == Some(runner) && cmdline(p).is_some_and(|c| c.contains("--mode rpc"))
    });
    let Some(omp) = omp else { return fail(format!("runner {runner} has no omp --mode rpc child")) };

    let Some(stdin_sock) = socket_inode(omp, "0") else {
        return fail(format!("omp {omp}: stdin is not a socket"));
    };
    let Some(peer) = peer_inode(&stdin_sock) else {
        return fail(format!("no peer for socket {stdin_sock} in `ss -xH`"));
    };
    let runner_fd = fs::read_dir(format!("/proc/{runner}/fd"))
        .into_iter()
        .flatten()
        .filter_map(|e| e.ok()?.file_name().to_str().map(String::from))
        .find(|fd| socket_inode(runner, fd).as_deref() == Some(peer.as_str()));
    let Some(runner_fd) = runner_fd else {
        return fail(format!("runner {runner} holds no fd for socket {peer}"));
    };

    // SAFETY: plain syscalls; the descriptors returned are owned by this process.
    let pidfd = unsafe { syscall(SYS_PIDFD_OPEN, runner as i64, 0i64) };
    if pidfd < 0 {
        return fail(format!("pidfd_open({runner}): {}", std::io::Error::last_os_error()));
    }
    let fd = unsafe { syscall(SYS_PIDFD_GETFD, pidfd, runner_fd.parse::<i64>().unwrap_or(-1), 0i64) };
    if fd < 0 {
        let e = std::io::Error::last_os_error();
        return fail(format!(
            "pidfd_getfd(runner {runner}, fd {runner_fd}): {e} (needs CAP_SYS_PTRACE: sudo setcap cap_sys_ptrace+ep <this binary>)"
        ));
    }
    let mut sock = unsafe { File::from_raw_fd(fd as i32) };
    let id = format!("forcecompact-{}", std::process::id());
    if let Err(e) = writeln!(sock, "{{\"id\":\"{id}\",\"type\":\"compact\"}}") {
        return fail(format!("write to omp {omp}: {e}"));
    }
    println!("{session}: compact sent to omp {omp} via runner {runner} fd {runner_fd} (id {id})");
    ExitCode::SUCCESS
}
