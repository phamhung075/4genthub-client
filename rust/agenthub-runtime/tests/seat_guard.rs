//! The seat guard's rules, run the way agy runs it: `node seat-guard.ts --agy-hook pre` with the payload on stdin.
//! Every case names the rule that must refuse it, or none, and the refusal must be logged as `blocked`.

use std::io::Write;
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::process::{Command, Stdio};

static CALLS: AtomicUsize = AtomicUsize::new(0);
const WORKTREE: &str = "/work/wt/dev";

fn guard() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("assets/seat-guard.ts")
}

/// A directory of this run's own, so no two cases share a log.
fn fresh_dir(tag: &str) -> PathBuf {
    std::env::temp_dir().join(format!("seat-guard-{tag}-{}-{}", std::process::id(), CALLS.fetch_add(1, Ordering::Relaxed)))
}

/// The rule that refused the call, or `None` when it was allowed.
fn verdict(name: &str, args: serde_json::Value) -> Option<String> {
    verdict_in(&fresh_dir("test"), name, args)
}

/// The same verdict with the seat's own directory - the one the guard logs to, and the one whose config it must
/// refuse to let the seat edit - pinned to `dir`.
fn verdict_in(dir: &std::path::Path, name: &str, args: serde_json::Value) -> Option<String> {
    std::fs::create_dir_all(dir).unwrap();
    let mut child = Command::new("node")
        .arg(guard())
        .args(["--agy-hook", "pre"])
        .env("AGENTHUB_SEAT", "dev")
        .env("AGENTHUB_WORKTREE", WORKTREE)
        .env("PI_CODING_AGENT_DIR", dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("node is needed to run the seat guard");
    let payload = serde_json::json!({"toolCall": {"name": name, "args": args}});
    child.stdin.take().unwrap().write_all(payload.to_string().as_bytes()).unwrap();
    let out = child.wait_with_output().unwrap();
    let answer: serde_json::Value = serde_json::from_slice(&out.stdout).expect("the guard answers JSON");
    let reason = answer["reason"].as_str().map(str::to_owned);
    reason.map(|r| {
        let rule = r.split(['(', ')']).nth(1).unwrap_or_default().to_owned();
        let log = std::fs::read_to_string(dir.join("tool-calls.jsonl")).unwrap_or_default();
        assert!(log.contains(&format!("\"rule\":\"{rule}\"")), "a refusal is logged as blocked: {log}");
        rule
    })
}

/// The one log line a call through the guard produced.
fn logged(name: &str, args: serde_json::Value) -> serde_json::Value {
    let dir = std::env::temp_dir().join(format!("seat-guard-log-{}-{}", std::process::id(), CALLS.fetch_add(1, Ordering::Relaxed)));
    std::fs::create_dir_all(&dir).unwrap();
    let mut child = Command::new("node")
        .arg(guard())
        .args(["--agy-hook", "pre"])
        .env("AGENTHUB_SEAT", "dev")
        .env("AGENTHUB_SESSION_NAME", "agenthub--room--dev")
        .env("AGENTHUB_WORKTREE", WORKTREE)
        .env("PI_CODING_AGENT_DIR", &dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("node is needed to run the seat guard");
    let payload = serde_json::json!({"toolCall": {"name": name, "args": args}});
    child.stdin.take().unwrap().write_all(payload.to_string().as_bytes()).unwrap();
    child.wait().unwrap();
    let log = std::fs::read_to_string(dir.join("tool-calls.jsonl")).unwrap();
    serde_json::from_str(log.lines().next().expect("a refusal is logged")).unwrap()
}

#[test]
fn a_log_line_carries_the_session_the_target_class_and_the_blocked_rule() {
    let line = logged("run_command", serde_json::json!({ "CommandLine": "cd /x && git push origin main" }));
    assert_eq!(line["session"], "agenthub--room--dev");
    assert_eq!(line["target_class"], "git push");
    assert_eq!(line["blocked_rule"], "push");
    assert_eq!(line["rule"], "push", "the existing field stays");
    assert_eq!(line["outcome"], "blocked");
    let secret = logged("run_command", serde_json::json!({ "CommandLine": "docker run -e API_KEY=hunter2 x" }));
    assert_eq!(secret["target_class"], "docker run");
    assert!(!secret["target_class"].as_str().unwrap().contains("hunter2"));
}

fn bash(cmd: &str) -> Option<String> {
    verdict("run_command", serde_json::json!({ "CommandLine": cmd }))
}

fn write(path: &str) -> Option<String> {
    verdict("write_to_file", serde_json::json!({ "TargetFile": path }))
}

fn refused(rule: &str, cmds: &[&str]) {
    for cmd in cmds {
        assert_eq!(bash(cmd).as_deref(), Some(rule), "{cmd}");
    }
}

fn allowed(cmds: &[&str]) {
    for cmd in cmds {
        assert_eq!(bash(cmd), None, "{cmd}");
    }
}

#[test]
fn the_existing_rules_still_judge_commands_not_quoted_words() {
    refused("push", &["git push", "cd x && git push origin main"]);
    refused("docker", &["docker ps"]);
    allowed(&[
        "git status | head",
        "4genteam room queue create --body \"run git push --dry-run, pkill, git reset --hard\"",
        "git commit -m x --body-file - <<'EOF'\nOWNER_DOCS_COMMIT=1 git reset --hard\nEOF",
    ]);
}

#[test]
fn a_seat_cannot_bypass_the_hooks() {
    refused(
        "hook-bypass",
        &[
            "OWNER_DOCS_COMMIT=1 git commit -m x",
            "env OWNER_DOCS_COMMIT=1 git commit -m x",
            "env -u AGENTHUB_SESSION_NAME git commit -m x",
            "git commit --no-verify -m x",
            "git commit -n -m x",
            "git commit -nm x",
            "git -c core.hooksPath=/dev/null commit -m x",
            "git config core.hooksPath /tmp/none",
        ],
    );
    allowed(&["git commit -m \"use --no-verify never\" -- a.go", "env -u TMUX tmux -L s ls", "git merge -n topic"]);
}

#[test]
fn a_seat_cannot_run_destructive_git() {
    refused(
        "git-destructive",
        &[
            "git reset --hard HEAD~1",
            "git clean -fd",
            "git checkout -- .",
            "git restore .",
            "git push --force origin main",
            "git add -A",
            "git add .",
            "git add --all",
            "git commit --amend -m x",
            "git stash drop",
        ],
    );
    allowed(&["git reset --soft HEAD~1", "git clean -n", "git checkout -- a.go", "git add -- a.go", "git commit -m x -- a.go"]);
}

#[test]
fn a_seat_cannot_kill_by_pattern_or_kill_pids_it_does_not_own() {
    refused(
        "process-kill",
        &["pkill omp", "killall node", "pgrep -f omp", "kill $(pgrep omp)", "kill 1", "ps | xargs kill"],
    );
    // The test process is the guard's parent, which the guard treats as the seat's own agent.
    let own = format!("kill -9 {}", std::process::id());
    allowed(&["pgrep -x omp", "kill -l", &own]);
}

#[test]
fn a_seat_writes_only_inside_its_worktree_and_not_to_frozen_or_protected_paths() {
    assert_eq!(write("/work/wt/dev/a.go"), None);
    assert_eq!(write("a.go"), None, "a relative path is the worktree's");
    assert_eq!(write("/tmp/scratch.txt"), None);
    assert_eq!(write("/home/other/main/a.go").as_deref(), Some("worktree-scope"));
    assert_eq!(write("/work/wt/dev/NEXT-GEN2.md").as_deref(), Some("frozen-paths"));
    assert_eq!(write("/work/wt/dev/ai_docs/reports-status/x.md").as_deref(), Some("frozen-paths"));
    assert_eq!(write("/work/wt/dev/GATE-7.md").as_deref(), Some("frozen-paths"));
    assert_eq!(write("/work/wt/dev/scripts/git-hooks/pre-commit").as_deref(), Some("protected-paths"));
    assert_eq!(write("/work/wt/dev/rust/assets/seat-guard.ts").as_deref(), Some("protected-paths"));
    refused("worktree-scope", &["echo x > /home/other/main/a.go", "cp a.go /home/other/main/", "echo x | tee /home/other/b"]);
    refused("frozen-paths", &["echo x >> NEXT-GEN2.md"]);
    refused("protected-paths", &["echo x > scripts/git-hooks/pre-commit"]);
    allowed(&["echo x > a.go", "make 2>&1 | tail", "echo x > /dev/null", "cp a.go b.go"]);
}

/// The guard rides two files that live in the seat's OWN directory, which worktree-scope allows the seat to write:
/// the omp config its deny patterns are rendered into (`4genteam policy apply`, header "do not edit") and the log
/// its refusals are recorded in. Editing either is how a seat disables the guard without touching the guard.
#[test]
fn a_seat_cannot_edit_the_config_or_the_log_the_guard_rests_on() {
    let seat = fresh_dir("seat");
    let in_seat = |name: &str| seat.join(name).display().to_string();
    let write_to = |path: &str| verdict_in(&seat, "write_to_file", serde_json::json!({ "TargetFile": path }));

    assert_eq!(write_to(&in_seat("config.yml")).as_deref(), Some("protected-paths"));
    assert_eq!(write_to(&in_seat("tool-calls.jsonl")).as_deref(), Some("protected-paths"));
    // The other two faces of a write reach the same rule: a shell that outlives the tool, and a copy over it.
    assert_eq!(
        verdict_in(&seat, "run_command", serde_json::json!({ "CommandLine": format!("echo x > {}", in_seat("config.yml")) }))
            .as_deref(),
        Some("protected-paths")
    );
    assert_eq!(
        verdict_in(&seat, "run_command", serde_json::json!({ "CommandLine": format!("cp evil.yml {}", in_seat("config.yml")) }))
            .as_deref(),
        Some("protected-paths")
    );
    // The rule names the seat's own directory, not the file names: a project's own config.yml stays the seat's to edit.
    assert_eq!(write("/work/wt/dev/config.yml"), None);
    assert_eq!(write("/work/wt/dev/tool-calls.jsonl"), None);
}
