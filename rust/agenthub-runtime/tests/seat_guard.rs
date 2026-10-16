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

/// The rule that refused the call, or `None` when it was allowed.
fn verdict(name: &str, args: serde_json::Value) -> Option<String> {
    let dir = std::env::temp_dir().join(format!("seat-guard-test-{}-{}", std::process::id(), CALLS.fetch_add(1, Ordering::Relaxed)));
    std::fs::create_dir_all(&dir).unwrap();
    let mut child = Command::new("node")
        .arg(guard())
        .args(["--agy-hook", "pre"])
        .env("AGENTHUB_SEAT", "dev")
        .env("AGENTHUB_WORKTREE", WORKTREE)
        .env("PI_CODING_AGENT_DIR", &dir)
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
