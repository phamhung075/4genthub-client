//! The seat guard's rules, run the way agy runs it: `node seat-guard.ts --agy-hook pre` with the payload on stdin.
//! Every case names the rule that must refuse it, or none, and the refusal must be logged as `blocked`.

use std::io::Write;
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::process::{Command, Stdio};

static CALLS: AtomicUsize = AtomicUsize::new(0);
/// The directory the seat starts in: the first entry of the list the launcher names, and what a relative path in
/// a tool call resolves against.
const WORKTREE: &str = "/work/wt/dev";
/// The list the launcher names, as `AGENTHUB_WORKTREES`: the seat's start directory first, then the rest of its own
/// worktrees - the ones the room holds under `<room>/wt/`, one per repo the seat edits. Another seat's worktree
/// and the checkout the seats on this host share are not in it, so they are out of scope by construction.
const WORKTREES: &str = "/work/wt/dev:/work/wt/dev-client";

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
    verdict_with(dir, WORKTREES, name, args)
}

/// The same verdict with the launcher's list spelled out, so a case can show that the LIST is what the guard
/// honors: the seat's own worktrees go in, another seat's does not.
fn verdict_with(dir: &std::path::Path, worktrees: &str, name: &str, args: serde_json::Value) -> Option<String> {
    refusal(dir, worktrees, name, args).map(|r| {
        let rule = r.split(['(', ')']).nth(1).unwrap_or_default().to_owned();
        let log = std::fs::read_to_string(dir.join("tool-calls.jsonl")).unwrap_or_default();
        assert!(log.contains(&format!("\"rule\":\"{rule}\"")), "a refusal is logged as blocked: {log}");
        rule
    })
}

/// What the guard answered, whole: the reason a seat reads, which names the directories it may write.
fn refusal(dir: &std::path::Path, worktrees: &str, name: &str, args: serde_json::Value) -> Option<String> {
    std::fs::create_dir_all(dir).unwrap();
    let mut child = Command::new("node")
        .arg(guard())
        .args(["--agy-hook", "pre"])
        .env("AGENTHUB_SEAT", "dev")
        .env("AGENTHUB_WORKTREES", worktrees)
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
    answer["reason"].as_str().map(str::to_owned)
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
        .env("AGENTHUB_WORKTREES", WORKTREES)
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

/// Deleting or rewriting in place what enforces the rules puts the seat outside them as surely as editing it does:
/// `rm`, `sed -i`, `truncate`, `dd of=`, an `mv` away and an `ln` over the file were all judged by NO rule, because
/// writeTargets saw only a redirect, a `tee` and a copy target. The park record and the room's budget are the same
/// kind of state, so they are named here too. A glob (`rm <seat dir>/*`), `find -delete`, `xargs rm` and a command
/// inside quotes are still unjudged: the guard is a net for a seat's mistakes, not a sandbox.
#[test]
fn a_seat_cannot_delete_or_rewrite_the_files_the_rules_rest_on() {
    let root = fresh_dir("state");
    let seat = root.join("rooms/r/seats/dev");
    std::fs::create_dir_all(seat.parent().unwrap()).unwrap();
    let room = seat.parent().unwrap().parent().unwrap();
    let seat_file = |name: &str| seat.join(name).display().to_string();
    let run = |cmd: String| verdict_in(&seat, "run_command", serde_json::json!({ "CommandLine": cmd }));

    let cases = vec![
        format!("rm {}", seat_file("config.yml")),
        format!("rm {}", seat_file("parked.json")),
        format!("rm -rf {}/hooks/pre/seat-guard.ts", seat.display()),
        format!("sed -i s/x/y/ {}", seat_file("tool-calls.jsonl")),
        format!("perl -pi -e s/x/y/ {}", seat_file("parked.json")),
        format!("truncate -s0 {}", seat_file("tool-calls.jsonl")),
        format!("dd of={}", seat_file("parked.json")),
        format!("mv {} /tmp/gone", seat_file("config.yml")),
        format!("ln -sf /dev/null {}", seat_file("tool-calls.jsonl")),
        format!("rm {}", room.join("limits.json").display()),
    ];
    for cmd in cases {
        assert_eq!(run(cmd.clone()).as_deref(), Some("protected-paths"), "{cmd}");
    }
    assert_eq!(
        verdict_in(&seat, "write_to_file", serde_json::json!({ "TargetFile": seat_file("parked.json") })).as_deref(),
        Some("protected-paths")
    );
    assert_eq!(
        verdict_in(&seat, "write_to_file", serde_json::json!({ "TargetFile": room.join("limits.json") })).as_deref(),
        Some("protected-paths")
    );
}

/// The net is not a blanket ban on deleting or rewriting: a seat's own worktree, the temp dir and a target it
/// cannot judge (a variable) stay its own.
#[test]
fn an_ordinary_deletion_still_stays_the_seat_s_own() {
    let seat = fresh_dir("seat");
    let run = |cmd: String| verdict_in(&seat, "run_command", serde_json::json!({ "CommandLine": cmd }));
    for cmd in [
        format!("rm -rf {WORKTREE}/dist"),
        "rm /tmp/scratch".to_string(),
        "rm -rf $SCRATCH/x".to_string(),
        format!("sed -i s/x/y/ {WORKTREE}/main.go"),
        format!("dd if=/dev/zero of={WORKTREE}/big.bin count=1"),
        format!("mv {WORKTREE}/a.go {WORKTREE}/b.go"),
    ] {
        assert_eq!(run(cmd.clone()), None, "{cmd}");
    }
}

/// The launcher names the directories a seat may write, and the list IS the scope: both of the seat's worktrees
/// answered, another seat's worktree and the checkout the seats on this host share refused. The guard derives none
/// of it - the runtime owns the room layout - so the case runs the guard with a list spelled out.
#[test]
fn the_scope_is_the_list_the_launcher_names_and_nothing_else() {
    let seat = fresh_dir("scope");
    let write_in = |worktrees: &str, path: String| {
        verdict_with(&seat, worktrees, "write_to_file", serde_json::json!({ "TargetFile": path }))
    };
    let list = "/work/wt/dev:/work/wt/dev-client";
    assert_eq!(write_in(list, "/work/wt/dev/a.go".into()), None);
    assert_eq!(write_in(list, "/work/wt/dev-client/rust/x.rs".into()), None, "a seat's second worktree is its own");
    assert_eq!(write_in(list, "a.go".into()), None, "a relative path is the first directory's");
    assert_eq!(write_in(list, "/tmp/scratch.txt".into()), None);
    assert_eq!(
        write_in(list, "/work/wt/other/a.go".into()).as_deref(),
        Some("worktree-scope"),
        "another seat's worktree is not named, so it is not in scope"
    );
    assert_eq!(
        write_in(list, "/home/daihu/__projects__/4genthub/agenthub-frontend/e2e/x.ts".into()).as_deref(),
        Some("worktree-scope"),
        "the checkout every seat on this host shares is not named either"
    );

    // The refusal says where the seat may write, because the directories are the launcher's to name and the seat's
    // to read: a refusal that named only one of them sent a seat to the wrong half of its own work.
    let reason = refusal(&seat, list, "write_to_file", serde_json::json!({ "TargetFile": "/work/wt/other/a.go" }))
        .expect("a path outside the list is refused");
    assert!(reason.contains("/work/wt/dev-client"), "the reason names the whole list: {reason}");
}

/// A guard whose launcher named no directory allows the seat's own directory and the temp dir and nothing else. The
/// session environment decides the workspace, so a list that never arrived must fail CLOSED: the version this
/// replaces answered `return ""` - every write allowed, including into the shared checkout (measured 2026-10-16:
/// five omp seats rooted at the daemon's own directory).
#[test]
fn a_guard_with_no_list_allows_only_the_seat_directory_and_the_temp_dir() {
    let seat = fresh_dir("no-list");
    let write_with = |worktrees: &str, path: String| {
        verdict_with(&seat, worktrees, "write_to_file", serde_json::json!({ "TargetFile": path }))
    };
    assert_eq!(
        write_with("", "/home/daihu/__projects__/4genthub/agenthub-frontend/e2e/x.ts".into()).as_deref(),
        Some("worktree-scope")
    );
    assert_eq!(write_with("", "/work/wt/dev/a.go".into()).as_deref(), Some("worktree-scope"));
    assert_eq!(write_with("", seat.join("sessions/x.jsonl").display().to_string()), None);
    assert_eq!(write_with("", "/tmp/scratch.txt".into()), None);
    // A relative path is still judged, against the seat's own directory - which a seat may always write.
    assert_eq!(
        write_with("", "a.go".into()),
        None,
        "with no list a relative path resolves inside the seat directory, which is always the seat's own"
    );
}
