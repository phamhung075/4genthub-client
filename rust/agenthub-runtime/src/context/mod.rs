use std::fs::{self, File};
use std::io::Write;
use std::os::fd::FromRawFd;
use std::process::Command;

pub const DEFAULT_LIMIT_TOKENS: usize = 150_000;
pub const DEFAULT_WARN_TOKENS: usize = 250_000;
pub const DEFAULT_HARD_TOKENS: usize = 300_000;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CompactionAction {
    Normal,
    SafePointNeeded,
    UrgentCompact,
    HardForceCompact,
}

pub struct ContextTracker {
    limit_tokens: usize,
    warn_tokens: usize,
    hard_tokens: usize,
}

impl Default for ContextTracker {
    fn default() -> Self {
        Self {
            limit_tokens: DEFAULT_LIMIT_TOKENS,
            warn_tokens: DEFAULT_WARN_TOKENS,
            hard_tokens: DEFAULT_HARD_TOKENS,
        }
    }
}

impl ContextTracker {
    pub fn new(limit: Option<usize>, warn: Option<usize>, hard: Option<usize>) -> Self {
        Self {
            limit_tokens: limit.unwrap_or(DEFAULT_LIMIT_TOKENS),
            warn_tokens: warn.unwrap_or(DEFAULT_WARN_TOKENS),
            hard_tokens: hard.unwrap_or(DEFAULT_HARD_TOKENS),
        }
    }

    /// Fast token estimator (~3.8 - 4 characters per token average for code/english).
    pub fn estimate_tokens(text: &str) -> usize {
        let bytes = text.len();
        if bytes == 0 {
            return 0;
        }
        // Count whitespace words + punctuation
        let word_count = text.split_whitespace().count();
        // Weighted heuristic: average between byte count / 4 and word count * 1.33
        let byte_est = bytes / 4;
        let word_est = (word_count * 4) / 3;
        (byte_est + word_est) / 2
    }

    /// Assess current token count against compaction limits.
    pub fn assess_threshold(&self, tokens: usize) -> CompactionAction {
        if tokens >= self.hard_tokens {
            CompactionAction::HardForceCompact
        } else if tokens >= self.warn_tokens {
            CompactionAction::UrgentCompact
        } else if tokens >= self.limit_tokens {
            CompactionAction::SafePointNeeded
        } else {
            CompactionAction::Normal
        }
    }

    /// Assemble 4-tier context hierarchy.
    pub fn assemble_context(
        company_overlay: Option<&str>,
        room_guide: Option<&str>,
        seat_role: Option<&str>,
        task_context: Option<&str>,
    ) -> String {
        let mut out = String::new();
        if let Some(co) = company_overlay {
            out.push_str("<!-- Tier 1: Company Overlay -->\n");
            out.push_str(co.trim());
            out.push_str("\n\n");
        }
        if let Some(rg) = room_guide {
            out.push_str("<!-- Tier 2: Room Guidelines -->\n");
            out.push_str(rg.trim());
            out.push_str("\n\n");
        }
        if let Some(sr) = seat_role {
            out.push_str("<!-- Tier 3: Seat Role -->\n");
            out.push_str(sr.trim());
            out.push_str("\n\n");
        }
        if let Some(tc) = task_context {
            out.push_str("<!-- Tier 4: Task Context -->\n");
            out.push_str(tc.trim());
            out.push_str("\n");
        }
        out
    }
}

// ---------------------------------------------------------------------------
// Native Force-Compaction (absorbed from forcecompact)
// ---------------------------------------------------------------------------

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
    stat.rsplit_once(')')?.1.split_whitespace().nth(1)?.parse().ok()
}

fn socket_inode(pid: u32, fd: &str) -> Option<String> {
    let link = fs::read_link(format!("/proc/{pid}/fd/{fd}")).ok()?;
    link.to_str()?.strip_prefix("socket:[")?.strip_suffix(']').map(String::from)
}

fn peer_inode(inode: &str) -> Option<String> {
    let out = Command::new("ss").args(["-xH"]).output().ok()?;
    String::from_utf8_lossy(&out.stdout).lines().find_map(|l| {
        let f: Vec<&str> = l.split_whitespace().collect();
        (f.len() >= 8 && f[5] == inode).then(|| f[7].to_string())
    })
}

/// Send an immediate active RPC compaction frame to an `omp` seat session.
pub fn send_force_compact(session_name: &str) -> Result<String, String> {
    let needle = format!("--session-name {session_name} ");
    let runner = pids().into_iter().find(|&p| {
        cmdline(p).is_some_and(|c| (c.contains("pi-runner.js") || c.contains("agenthub-runtime")) && format!("{c} ").contains(&needle))
    });
    let Some(runner) = runner else {
        return Err(format!("no runner process for session {session_name}"));
    };

    let omp = pids().into_iter().find(|&p| {
        ppid(p) == Some(runner) && cmdline(p).is_some_and(|c| c.contains("--mode rpc"))
    });
    let Some(omp) = omp else {
        return Err(format!("runner {runner} has no omp --mode rpc child"));
    };

    let Some(stdin_sock) = socket_inode(omp, "0") else {
        return Err(format!("omp {omp}: stdin is not a socket"));
    };
    let Some(peer) = peer_inode(&stdin_sock) else {
        return Err(format!("no peer for socket {stdin_sock} in ss"));
    };
    let runner_fd = fs::read_dir(format!("/proc/{runner}/fd"))
        .into_iter()
        .flatten()
        .filter_map(|e| e.ok()?.file_name().to_str().map(String::from))
        .find(|fd| socket_inode(runner, fd).as_deref() == Some(peer.as_str()));
    let Some(runner_fd) = runner_fd else {
        return Err(format!("runner {runner} holds no fd for socket {peer}"));
    };

    // SAFETY: plain syscalls; the descriptors returned are owned by this process.
    let pidfd = unsafe { syscall(SYS_PIDFD_OPEN, runner as i64, 0i64) };
    if pidfd < 0 {
        return Err(format!("pidfd_open({runner}): {}", std::io::Error::last_os_error()));
    }
    let fd = unsafe { syscall(SYS_PIDFD_GETFD, pidfd, runner_fd.parse::<i64>().unwrap_or(-1), 0i64) };
    if fd < 0 {
        return Err(format!(
            "pidfd_getfd(runner {runner}, fd {runner_fd}): {} (needs CAP_SYS_PTRACE)",
            std::io::Error::last_os_error()
        ));
    }
    let mut sock = unsafe { File::from_raw_fd(fd as i32) };
    let id = format!("forcecompact-{}", std::process::id());
    writeln!(sock, "{{\"id\":\"{id}\",\"type\":\"compact\"}}")
        .map_err(|e| format!("write to omp {omp}: {e}"))?;

    Ok(format!("{session_name}: compact sent to omp {omp} (id {id})"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_token_estimation() {
        let text = "Hello world! This is a test of token estimation in Rust.";
        let tokens = ContextTracker::estimate_tokens(text);
        assert!(tokens > 5 && tokens < 25);
    }

    #[test]
    fn test_threshold_assessments() {
        let tracker = ContextTracker::default();
        assert_eq!(tracker.assess_threshold(50_000), CompactionAction::Normal);
        assert_eq!(tracker.assess_threshold(160_000), CompactionAction::SafePointNeeded);
        assert_eq!(tracker.assess_threshold(260_000), CompactionAction::UrgentCompact);
        assert_eq!(tracker.assess_threshold(310_000), CompactionAction::HardForceCompact);
    }

    #[test]
    fn test_context_assembly() {
        let ctx = ContextTracker::assemble_context(
            Some("Company Policy"),
            Some("Room Rules"),
            Some("Developer Role"),
            Some("Fix Bug #123"),
        );
        assert!(ctx.contains("Tier 1: Company Overlay"));
        assert!(ctx.contains("Tier 2: Room Guidelines"));
        assert!(ctx.contains("Tier 3: Seat Role"));
        assert!(ctx.contains("Tier 4: Task Context"));
    }
}
