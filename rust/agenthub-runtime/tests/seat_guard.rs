//! The seat guard's rules, run the way agy runs it: `node seat-guard.ts --agy-hook pre` with the payload on stdin.
//! Every case names the rule that must refuse it, or none, and the refusal must be logged as `blocked`.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicUsize, Ordering};

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
    std::env::temp_dir().join(format!(
        "seat-guard-{tag}-{}-{}",
        std::process::id(),
        CALLS.fetch_add(1, Ordering::Relaxed)
    ))
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
fn verdict_with(
    dir: &std::path::Path,
    worktrees: &str,
    name: &str,
    args: serde_json::Value,
) -> Option<String> {
    refusal(dir, worktrees, name, args).map(|r| {
        let rule = r.split(['(', ')']).nth(1).unwrap_or_default().to_owned();
        let log = std::fs::read_to_string(dir.join("tool-calls.jsonl")).unwrap_or_default();
        assert!(
            log.contains(&format!("\"rule\":\"{rule}\"")),
            "a refusal is logged as blocked: {log}"
        );
        rule
    })
}

/// What the guard answered, whole: the reason a seat reads, which names the directories it may write.
fn refusal(dir: &Path, worktrees: &str, name: &str, args: serde_json::Value) -> Option<String> {
    refusal_as(dir, worktrees, "", name, args)
}

/// The same, with the seat's own session in the environment - the source a queue create is stamped with, and the
/// value the coalesce rule matches a pending row against.
fn refusal_as(
    dir: &Path,
    worktrees: &str,
    session: &str,
    name: &str,
    args: serde_json::Value,
) -> Option<String> {
    refusal_for_seat(dir, worktrees, session, "dev", name, args)
}

/// The same, with the seat KEY the launcher exported (`AGENTHUB_SEAT`) spelled out: the owner's push
/// authority is decided from it when the session name is a stale one.
fn refusal_for_seat(
    dir: &Path,
    worktrees: &str,
    session: &str,
    seat: &str,
    name: &str,
    args: serde_json::Value,
) -> Option<String> {
    refusal_full(dir, worktrees, session, seat, "", name, args)
}

/// The same, with the room's `wt/` handed over as the LANE ROOT (`AGENTHUB_LANES_ROOT`): the guard admits
/// `<wt>/<seat>` and `<wt>/<seat>-*` by the seat key it was launched with, and refuses the rest of the room.
fn refusal_lanes(dir: &Path, lanes: &str, name: &str, args: serde_json::Value) -> Option<String> {
    refusal_full(dir, WORKTREES, "", "dev", lanes, name, args)
}

/// The guard call every helper above makes: the seat's session, the seat key, and the lane root when a case names
/// one. An empty lane root is ABSENT from the environment - which is exactly what a launch predating the variable
/// looks like, so the unset case is the old launch and not a second spelling of the new one.
fn refusal_full(
    dir: &Path,
    worktrees: &str,
    session: &str,
    seat: &str,
    lanes: &str,
    name: &str,
    args: serde_json::Value,
) -> Option<String> {
    std::fs::create_dir_all(dir).unwrap();
    let mut command = Command::new("node");
    command
        .arg(guard())
        .args(["--agy-hook", "pre"])
        .env("AGENTHUB_SEAT", seat)
        .env("AGENTHUB_SESSION_NAME", session)
        .env_remove("OPENRIG_SESSION_NAME")
        .env("AGENTHUB_WORKTREES", worktrees)
        .env("PI_CODING_AGENT_DIR", dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    if lanes.is_empty() {
        command.env_remove("AGENTHUB_LANES_ROOT");
    } else {
        command.env("AGENTHUB_LANES_ROOT", lanes);
    }
    let mut child = command
        .spawn()
        .expect("node is needed to run the seat guard");
    let payload = serde_json::json!({"toolCall": {"name": name, "args": args}});
    child
        .stdin
        .take()
        .unwrap()
        .write_all(payload.to_string().as_bytes())
        .unwrap();
    let out = child.wait_with_output().unwrap();
    let answer: serde_json::Value =
        serde_json::from_slice(&out.stdout).expect("the guard answers JSON");
    answer["reason"].as_str().map(str::to_owned)
}

/// The one log line a call through the guard produced.
fn logged(name: &str, args: serde_json::Value) -> serde_json::Value {
    let dir = std::env::temp_dir().join(format!(
        "seat-guard-log-{}-{}",
        std::process::id(),
        CALLS.fetch_add(1, Ordering::Relaxed)
    ));
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
    child
        .stdin
        .take()
        .unwrap()
        .write_all(payload.to_string().as_bytes())
        .unwrap();
    child.wait().unwrap();
    let log = std::fs::read_to_string(dir.join("tool-calls.jsonl")).unwrap();
    serde_json::from_str(log.lines().next().expect("a refusal is logged")).unwrap()
}

#[test]
fn a_log_line_carries_the_session_the_target_class_and_the_blocked_rule() {
    let line = logged(
        "run_command",
        serde_json::json!({ "CommandLine": "cd /x && git push origin main" }),
    );
    assert_eq!(line["session"], "agenthub--room--dev");
    assert_eq!(line["target_class"], "git push");
    assert_eq!(line["blocked_rule"], "push");
    assert_eq!(line["rule"], "push", "the existing field stays");
    assert_eq!(line["outcome"], "blocked");
    let secret = logged(
        "run_command",
        serde_json::json!({ "CommandLine": "docker run -e API_KEY=hunter2 x" }),
    );
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

/// The owner's 2026-10-18 ruling: the lead pushes after the reviewer gate and a green test run, and every
/// other seat is still blocked. The exception is the seat KEY, read from either session spelling or from
/// the key the launcher exported; a name that merely starts with it is another seat.
#[test]
fn the_lead_pushes_and_no_other_seat_does() {
    let dir = fresh_dir("lead");
    let push = || serde_json::json!({ "CommandLine": "git push origin main" });

    for session in ["agenthub--room--lead", "room-lead@room"] {
        assert_eq!(
            refusal_as(&dir, WORKTREES, session, "run_command", push()),
            None,
            "{session}"
        );
    }
    assert_eq!(
        refusal_for_seat(
            &dir,
            WORKTREES,
            "agenthub--room--dev",
            "lead",
            "run_command",
            push()
        ),
        None,
        "the exported seat key alone admits the lead"
    );

    for session in [
        "",
        "agenthub--room--dev",
        "agenthub--room--lead-dev",
        "room-lead2@room",
    ] {
        let verdict = refusal_as(&dir, WORKTREES, session, "run_command", push());
        assert!(
            verdict
                .as_deref()
                .is_some_and(|reason| reason.contains("(push)")),
            "{session}: want the push rule, got {verdict:?}"
        );
    }

    // The rest of the lead's guards stand: a force push is destructive git for every seat.
    for session in ["agenthub--room--lead", "agenthub--room--dev"] {
        let verdict = refusal_as(
            &dir,
            WORKTREES,
            session,
            "run_command",
            serde_json::json!({ "CommandLine": "git push --force origin main" }),
        );
        assert!(
            verdict
                .as_deref()
                .is_some_and(|reason| reason.contains("(git-destructive)")),
            "{session}: want the destructive-git rule, got {verdict:?}"
        );
    }
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
    allowed(&[
        "git commit -m \"use --no-verify never\" -- a.go",
        "env -u TMUX tmux -L s ls",
        "git merge -n topic",
    ]);
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
    allowed(&[
        "git reset --soft HEAD~1",
        "git clean -n",
        "git checkout -- a.go",
        "git add -- a.go",
        "git commit -m x -- a.go",
    ]);
}

#[test]
fn a_seat_cannot_kill_by_pattern_or_kill_pids_it_does_not_own() {
    refused(
        "process-kill",
        &[
            "pkill omp",
            "killall node",
            "pgrep -f omp",
            "kill $(pgrep omp)",
            "kill 1",
            "ps | xargs kill",
        ],
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
    assert_eq!(
        write("/home/other/main/a.go").as_deref(),
        Some("worktree-scope")
    );
    assert_eq!(
        write("/work/wt/dev/NEXT-GEN2.md").as_deref(),
        Some("frozen-paths")
    );
    assert_eq!(
        write("/work/wt/dev/ai_docs/reports-status/x.md").as_deref(),
        Some("frozen-paths")
    );
    assert_eq!(
        write("/work/wt/dev/GATE-7.md").as_deref(),
        Some("frozen-paths")
    );
    assert_eq!(
        write("/work/wt/dev/scripts/git-hooks/pre-commit").as_deref(),
        Some("protected-paths")
    );
    assert_eq!(
        write("/work/wt/dev/rust/assets/seat-guard.ts").as_deref(),
        Some("protected-paths")
    );
    refused(
        "worktree-scope",
        &[
            "echo x > /home/other/main/a.go",
            "cp a.go /home/other/main/",
            "echo x | tee /home/other/b",
        ],
    );
    refused("frozen-paths", &["echo x >> NEXT-GEN2.md"]);
    refused(
        "protected-paths",
        &["echo x > scripts/git-hooks/pre-commit"],
    );
    allowed(&[
        "echo x > a.go",
        "make 2>&1 | tail",
        "echo x > /dev/null",
        "cp a.go b.go",
    ]);
}

/// The guard rides two files that live in the seat's OWN directory, which worktree-scope allows the seat to write:
/// the omp config its deny patterns are rendered into (`4genteam policy apply`, header "do not edit") and the log
/// its refusals are recorded in. Editing either is how a seat disables the guard without touching the guard.
#[test]
fn a_seat_cannot_edit_the_config_or_the_log_the_guard_rests_on() {
    let seat = fresh_dir("seat");
    let in_seat = |name: &str| seat.join(name).display().to_string();
    let write_to = |path: &str| {
        verdict_in(
            &seat,
            "write_to_file",
            serde_json::json!({ "TargetFile": path }),
        )
    };

    assert_eq!(
        write_to(&in_seat("config.yml")).as_deref(),
        Some("protected-paths")
    );
    assert_eq!(
        write_to(&in_seat("tool-calls.jsonl")).as_deref(),
        Some("protected-paths")
    );
    // The other two faces of a write reach the same rule: a shell that outlives the tool, and a copy over it.
    assert_eq!(
        verdict_in(
            &seat,
            "run_command",
            serde_json::json!({ "CommandLine": format!("echo x > {}", in_seat("config.yml")) })
        )
        .as_deref(),
        Some("protected-paths")
    );
    assert_eq!(
        verdict_in(
            &seat,
            "run_command",
            serde_json::json!({ "CommandLine": format!("cp evil.yml {}", in_seat("config.yml")) })
        )
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
    let run = |cmd: String| {
        verdict_in(
            &seat,
            "run_command",
            serde_json::json!({ "CommandLine": cmd }),
        )
    };

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
        assert_eq!(
            run(cmd.clone()).as_deref(),
            Some("protected-paths"),
            "{cmd}"
        );
    }
    assert_eq!(
        verdict_in(
            &seat,
            "write_to_file",
            serde_json::json!({ "TargetFile": seat_file("parked.json") })
        )
        .as_deref(),
        Some("protected-paths")
    );
    assert_eq!(
        verdict_in(
            &seat,
            "write_to_file",
            serde_json::json!({ "TargetFile": room.join("limits.json") })
        )
        .as_deref(),
        Some("protected-paths")
    );
}

/// The net is not a blanket ban on deleting or rewriting: a seat's own worktree, the temp dir and a target it
/// cannot judge (a variable) stay its own.
#[test]
fn an_ordinary_deletion_still_stays_the_seat_s_own() {
    let seat = fresh_dir("seat");
    let run = |cmd: String| {
        verdict_in(
            &seat,
            "run_command",
            serde_json::json!({ "CommandLine": cmd }),
        )
    };
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
        verdict_with(
            &seat,
            worktrees,
            "write_to_file",
            serde_json::json!({ "TargetFile": path }),
        )
    };
    let list = "/work/wt/dev:/work/wt/dev-client";
    assert_eq!(write_in(list, "/work/wt/dev/a.go".into()), None);
    assert_eq!(
        write_in(list, "/work/wt/dev-client/rust/x.rs".into()),
        None,
        "a seat's second worktree is its own"
    );
    assert_eq!(
        write_in(list, "a.go".into()),
        None,
        "a relative path is the first directory's"
    );
    assert_eq!(write_in(list, "/tmp/scratch.txt".into()), None);
    assert_eq!(
        write_in(list, "/work/wt/other/a.go".into()).as_deref(),
        Some("worktree-scope"),
        "another seat's worktree is not named, so it is not in scope"
    );
    assert_eq!(
        write_in(
            list,
            "/home/daihu/__projects__/4genthub/agenthub-frontend/e2e/x.ts".into()
        )
        .as_deref(),
        Some("worktree-scope"),
        "the checkout every seat on this host shares is not named either"
    );

    // The refusal says where the seat may write, because the directories are the launcher's to name and the seat's
    // to read: a refusal that named only one of them sent a seat to the wrong half of its own work.
    let reason = refusal(
        &seat,
        list,
        "write_to_file",
        serde_json::json!({ "TargetFile": "/work/wt/other/a.go" }),
    )
    .expect("a path outside the list is refused");
    assert!(
        reason.contains("/work/wt/dev-client"),
        "the reason names the whole list: {reason}"
    );
}

/// A guard whose launcher named no directory allows the seat's own directory and the temp dir and nothing else. The
/// session environment decides the workspace, so a list that never arrived must fail CLOSED: the version this
/// replaces answered `return ""` - every write allowed, including into the shared checkout (measured 2026-10-16:
/// five omp seats rooted at the daemon's own directory).
#[test]
fn a_guard_with_no_list_allows_only_the_seat_directory_and_the_temp_dir() {
    let seat = fresh_dir("no-list");
    let write_with = |worktrees: &str, path: String| {
        verdict_with(
            &seat,
            worktrees,
            "write_to_file",
            serde_json::json!({ "TargetFile": path }),
        )
    };
    assert_eq!(
        write_with(
            "",
            "/home/daihu/__projects__/4genthub/agenthub-frontend/e2e/x.ts".into()
        )
        .as_deref(),
        Some("worktree-scope")
    );
    assert_eq!(
        write_with("", "/work/wt/dev/a.go".into()).as_deref(),
        Some("worktree-scope")
    );
    assert_eq!(
        write_with("", seat.join("sessions/x.jsonl").display().to_string()),
        None
    );
    assert_eq!(write_with("", "/tmp/scratch.txt".into()), None);
    // A relative path is still judged, against the seat's own directory - which a seat may always write.
    assert_eq!(
        write_with("", "a.go".into()),
        None,
        "with no list a relative path resolves inside the seat directory, which is always the seat's own"
    );
}

/// A statement that makes or removes a DIRECTORY ENTRY without writing a byte was judged by NO rule: `touch`
/// creates the file, `mkdir` and `rmdir` make and remove the directory, and none of them is a redirect, a `tee`, a
/// copy target, a move, an in-place edit or a `dd`, so the path each of them built was never read. Measured before
/// this change: `touch <seat dir>/config.yml`, `mkdir -p <seat dir>/hooks/pre`, `rmdir <seat dir>/hooks/pre` and
/// `touch <room>/limits.json` were all ALLOWED, as was `mkdir -p /home/other/elsewhere` and `touch` of a file in
/// the checkout every seat on this host shares. The two rules that already read `writeTargets` name them now, so
/// no rule was added: protected-paths for what enforces the rules, worktree-scope for everything else.
#[test]
fn a_seat_cannot_touch_or_mkdir_a_path_no_rule_would_see() {
    let root = fresh_dir("state");
    let seat = root.join("rooms/r/seats/dev");
    std::fs::create_dir_all(seat.parent().unwrap()).unwrap();
    let room = seat.parent().unwrap().parent().unwrap();
    let run = |cmd: String| {
        verdict_in(
            &seat,
            "run_command",
            serde_json::json!({ "CommandLine": cmd }),
        )
    };

    // The paths the guard rests on, built with no byte written: the seat's own config, the directory the guard is
    // installed in, and the room's budget file.
    for cmd in [
        format!("touch {}", seat.join("config.yml").display()),
        format!("mkdir -p {}/hooks/pre", seat.display()),
        format!("rmdir {}/hooks/pre", seat.display()),
        format!("touch {}", room.join("limits.json").display()),
    ] {
        assert_eq!(
            run(cmd.clone()).as_deref(),
            Some("protected-paths"),
            "{cmd}"
        );
    }
    // The protected DIRECTORIES themselves, which a trailing slash never matched: the seat's worktree is in scope,
    // so removing its copy of the hooks, or making the directory they live in, had no rule at all.
    for cmd in [
        format!("rm -rf {WORKTREE}/scripts/git-hooks"),
        format!("mkdir -p {WORKTREE}/.git/hooks"),
    ] {
        assert_eq!(
            run(cmd.clone()).as_deref(),
            Some("protected-paths"),
            "{cmd}"
        );
    }
    // A directory entry outside every directory the launcher named for this seat.
    for cmd in [
        "mkdir -p /home/other/elsewhere".to_string(),
        "touch /home/other/planted.go".to_string(),
        "rmdir /home/other/empty".to_string(),
    ] {
        assert_eq!(run(cmd.clone()).as_deref(), Some("worktree-scope"), "{cmd}");
    }
}

/// The net is not a blanket ban on making directories: the worktree, the seat's own directory, the temp dir and a
/// target it cannot judge (a variable) stay the seat's own, and a segment that merely STARTS with a protected one
/// (`hooks/preflight`) is not protected.
#[test]
fn an_ordinary_touch_or_mkdir_stays_the_seat_s_own() {
    let seat = fresh_dir("seat");
    let run = |cmd: String| {
        verdict_in(
            &seat,
            "run_command",
            serde_json::json!({ "CommandLine": cmd }),
        )
    };
    for cmd in [
        format!("touch {WORKTREE}/new.go"),
        format!("mkdir -p {WORKTREE}/build/probe"),
        format!("rmdir {WORKTREE}/build/probe"),
        format!("mkdir -p {WORKTREE}/hooks/preflight"),
        format!("touch {}", seat.join("notes.txt").display()),
        "mkdir -p $SCRATCH/x".to_string(),
        "touch /tmp/probe-file".to_string(),
    ] {
        assert_eq!(run(cmd.clone()), None, "{cmd}");
    }
}

/// A SEAT DOES NOT RAISE ITS OWN CEILING. The verb writes the room's limits THROUGH the client, so it never names a
/// file and no path rule can see it: measured 2026-10-16 at 6492b77, `4genteam room limits <room> --usd 999` was
/// allowed while every FILE shape against the same ceiling (rm, sed -i, truncate -s 0, dd, the write tool) was
/// refused. Reading the ceilings stays the seat's business.
#[test]
fn a_seat_cannot_raise_its_own_budget() {
    refused(
        "budget-ceiling",
        &[
            "4genteam room limits 4genthub-min --usd 999",
            "4genteam room limits 4genthub-min --calls 1000000",
            "4genteam room limits 4genthub-min --off",
            "4genteam room limits 4genthub-min --on",
            "env -u TMUX 4genteam room limits 4genthub-min --usd 999",
            "cd /tmp && /opt/4gh/bin/4genteam room limits 4genthub-min --usd 999",
        ],
    );
    allowed(&[
        "4genteam room limits 4genthub-min",
        "4genteam room limits 4genthub-min --json",
        "4genteam room status 4genthub-min",
        "echo '4genteam room limits 4genthub-min --usd 999'",
    ]);
}

/// AN EMPTY VALUE IN A PREFIX IS STILL A PREFIX. `callOf` ate the env/sudo noise with `[A-Za-z_]+=\S+`, which asks
/// for at least one character after the `=`, so the three-character head `FOO= ` - what a shell accepts, and how a
/// seat empties a marker it was handed - was not noise, the anchor never reached the command, and EVERY rule built
/// on `callOf` was stepped past. Measured 2026-10-16 at d964b8b: `FOO= 4genteam room limits 4genthub-min --usd 999`,
/// `FOO= git push origin main` and `FOO= rm scripts/git-hooks/pre-commit` were all ALLOWED while their bare
/// spellings were refused. Both spellings are pinned, so the fix cannot buy the prefixed form by losing the bare
/// one, and a prefix that CARRIES a value is pinned too: that one already worked, and it is the line that says the
/// hole was the empty value and nothing else.
#[test]
fn an_empty_value_in_a_prefix_is_still_a_prefix() {
    for (rule, cmd) in [
        ("push", "git push origin main"),
        ("protected-paths", "rm scripts/git-hooks/pre-commit"),
        (
            "protected-paths",
            "sed -i s/x/y/ scripts/git-hooks/pre-commit",
        ),
        (
            "budget-ceiling",
            "4genteam room limits 4genthub-min --usd 999",
        ),
    ] {
        assert_eq!(bash(cmd).as_deref(), Some(rule), "{cmd}");
        for head in ["FOO=", "AGENTHUB_SESSION_NAME=", "env FOO=", "FOO=1"] {
            let prefixed = format!("{head} {cmd}");
            assert_eq!(bash(&prefixed).as_deref(), Some(rule), "{prefixed}");
        }
    }
    // The prefix is noise and not a rule of its own: what the command IS stays what is judged.
    allowed(&[
        "FOO= git status",
        "AGENTHUB_SESSION_NAME= git status",
        "env FOO= ls -la",
    ]);
}

// ---- queue-coalesce ------------------------------------------------------------------------------------------

const COALESCE_ROOM: &str = "rig";
const COALESCE_LEAD: &str = "agenthub--room--lead";
const COALESCE_SEAT: &str = "agenthub--room--dev";

/// A room of this run's own holding the rows a case needs: the store at `<root>/rooms/<room>/queue.sqlite`, the
/// seat dir three levels under it (`rooms/<room>/seats/dev`), and one row per tuple - the id, the state, the source,
/// the destination, the tags as the JSON the store holds, and the row's age in seconds.
fn room_with_rows(tag: &str, rows: &[(&str, &str, &str, &str, &str, u64)]) -> PathBuf {
    let root = fresh_dir(tag);
    let room = root.join("rooms").join(COALESCE_ROOM);
    let seat = room.join("seats").join("dev");
    std::fs::create_dir_all(&seat).unwrap();
    let db = rusqlite::Connection::open(room.join("queue.sqlite")).unwrap();
    db.execute_batch(
        "CREATE TABLE queue_rows (qitem_id TEXT PRIMARY KEY, state TEXT NOT NULL, summary TEXT NOT NULL, body TEXT \
         NOT NULL, source_session TEXT NOT NULL, destination_session TEXT NOT NULL, chain_of_record TEXT NOT NULL, \
         reply_to TEXT, blocked_on TEXT, wake_after_seconds INTEGER, closure_reason TEXT, closure_target TEXT, \
         priority TEXT NOT NULL, tags TEXT NOT NULL, tier TEXT, ts_created TEXT NOT NULL, ts_updated TEXT NOT NULL);",
    )
    .unwrap();
    for (qitem, state, source, destination, tags, age) in rows {
        let ts = (chrono::Utc::now() - chrono::Duration::seconds(*age as i64))
            .to_rfc3339_opts(chrono::SecondsFormat::Micros, true);
        db.execute(
            "INSERT INTO queue_rows (qitem_id, state, summary, body, source_session, destination_session, \
             chain_of_record, priority, tags, ts_created, ts_updated) \
             VALUES (?1, ?2, 'the row a case holds', '', ?3, ?4, '[]', 'normal', ?5, ?6, ?6)",
            rusqlite::params![qitem, state, source, destination, tags, ts],
        )
        .unwrap();
    }
    seat
}

/// A create through the guard as this seat, with `--to` and `--summary` always present (the client requires both)
/// and the case's own extra arguments after them.
fn create_verdict(seat: &Path, extra: &str) -> Option<String> {
    refusal_as(
        seat,
        WORKTREES,
        COALESCE_SEAT,
        "run_command",
        serde_json::json!({
            "CommandLine": format!("4genteam room queue create {COALESCE_ROOM} --to {COALESCE_LEAD} --summary again {extra}")
        }),
    )
}

/// A SEAT DOES NOT SAY THE SAME THING TWICE. A create whose destination and tag an existing PENDING row from this
/// same seat already carries, inside the window, is refused - and the refusal NAMES that row, because the point is
/// to make the seat read and update it, not to make it guess. The row is the store's own shape (the tags column is
/// the JSON array the store holds).
#[test]
fn a_second_pending_row_to_the_same_destination_and_tag_is_refused_by_name() {
    let id = "qitem-20261017070000-aaaaaaaaaaaa";
    let seat = room_with_rows(
        "coalesce",
        &[(
            id,
            "pending",
            COALESCE_SEAT,
            COALESCE_LEAD,
            "[\"finding\"]",
            60,
        )],
    );
    let reason = create_verdict(&seat, "--tag finding").expect("a repeat is refused");
    assert!(
        reason.contains("queue-coalesce"),
        "the rule is named: {reason}"
    );
    assert!(
        reason.contains(id),
        "the refusal names the row it would duplicate: {reason}"
    );
    // A shared tag among several is enough, and the comma list is parsed as the client parses it.
    let shared = room_with_rows(
        "coalesce-shared",
        &[(
            id,
            "pending",
            COALESCE_SEAT,
            COALESCE_LEAD,
            "[\"gate\",\"finding\"]",
            60,
        )],
    );
    assert!(create_verdict(&shared, "--tag other,finding")
        .expect("a shared tag refuses")
        .contains(id));
}

/// The predicate and its window, each edge pinned so the rule cannot buy the refusal by refusing everything: the
/// state must be pending, the source this seat, the destination this create's, the tag sets must INTERSECT, the row
/// must be younger than the window, and the verb must be a create. The window is read with a margin on both sides
/// (nine minutes refuses, eleven does not): the exact second is wall-clock by construction, and a test that raced
/// the clock would be a flake, not a boundary.
#[test]
fn the_coalesce_rule_reads_the_store_s_state_source_destination_tag_and_window() {
    let id = "qitem-20261017070000-bbbbbbbbbbbb";
    let pending = |tag: &str, age: u64| {
        room_with_rows(
            tag,
            &[(
                id,
                "pending",
                COALESCE_SEAT,
                COALESCE_LEAD,
                "[\"finding\"]",
                age,
            )],
        )
    };
    assert!(
        create_verdict(&pending("inside", 540), "--tag finding").is_some(),
        "nine minutes inside the window refuses"
    );
    assert_eq!(
        create_verdict(&pending("outside", 660), "--tag finding"),
        None,
        "eleven minutes outside does not"
    );
    let other = |tag: &str, state: &str, source: &str, destination: &str| {
        room_with_rows(
            tag,
            &[(id, state, source, destination, "[\"finding\"]", 60)],
        )
    };
    assert_eq!(
        create_verdict(
            &other("state", "in-progress", COALESCE_SEAT, COALESCE_LEAD),
            "--tag finding"
        ),
        None
    );
    assert_eq!(
        create_verdict(
            &other(
                "destination",
                "pending",
                COALESCE_SEAT,
                "agenthub--room--reviewer"
            ),
            "--tag finding"
        ),
        None
    );
    assert_eq!(
        create_verdict(
            &other("tag", "pending", COALESCE_SEAT, COALESCE_LEAD),
            "--tag gate"
        ),
        None
    );
    assert_eq!(
        create_verdict(
            &other("source", "pending", "agenthub--room--fe-dev", COALESCE_LEAD),
            "--tag finding"
        ),
        None
    );
    // A seat that names no tag, or only other queue verbs, is not judged: the rule reads creates and tags.
    assert_eq!(create_verdict(&pending("untagged", 60), ""), None);
    let seat = pending("verb", 60);
    for cmd in [
        "4genteam room queue list room".to_string(),
        format!("4genteam room queue show room {id}"),
        format!("4genteam room queue update room {id} --note still owed"),
        "4genteam room queue close room qitem-20261017070000-cccccccccccc --reason no-follow-on"
            .to_string(),
    ] {
        assert_eq!(
            refusal_as(
                &seat,
                WORKTREES,
                COALESCE_SEAT,
                "run_command",
                serde_json::json!({ "CommandLine": cmd })
            ),
            None,
            "{cmd}"
        );
    }
}

// ---- local-frontend-build ------------------------------------------------------------------------------------

/// A SEAT DOES NOT BUILD THE FRONTEND LOCALLY. Two vite builds in /tmp/fe-tip took 3.9 GB while free RAM was 4 of
/// 16 (the owner killed both, 2026-10-17); the deploy builds the frontend and `npx tsc --noEmit` is the local
/// check. Every spelling that BUILDS is refused, however it is reached; a probe that builds nothing passes, and so
/// do the checks that are not builds.
#[test]
fn a_seat_cannot_run_a_local_frontend_build() {
    refused(
        "local-frontend-build",
        &[
            "vite build",
            "npx vite",
            "npm run build",
            "cd agenthub-frontend && npm run build",
            "npx vite build",
            "./node_modules/.bin/vite build",
            "pnpm build",
            "npm run build:prod",
        ],
    );
    allowed(&[
        "npx tsc --noEmit",
        "vite --version",
        "npm run lint",
        "npx vitest run",
    ]);
}

// ---- the lane ------------------------------------------------------------------------------------------------

/// THE LANE IS NAMED BY THE SEAT KEY. With the room's `wt/` handed over as `AGENTHUB_LANES_ROOT`, a worktree the
/// seat opens mid-session at `<wt>/<seat>-<name>` is inside its scope without re-reading the launch list; another
/// seat's lane, a name that merely STARTS with the key (`devv`), and the room root itself stay refused. A launch
/// with no lane root admits no lane, and the reason the seat reads is today's string, byte for byte, because the
/// lane hint is printed only when a lane root arrived.
#[test]
fn a_lane_is_admitted_by_the_seat_key_and_only_that_prefix() {
    let dir = fresh_dir("lane");
    let rule = |lanes: &str, path: &str| {
        refusal_lanes(
            &dir,
            lanes,
            "write_to_file",
            serde_json::json!({ "TargetFile": path }),
        )
        .map(|r| r.split(['(', ')']).nth(1).unwrap_or_default().to_owned())
    };

    assert_eq!(
        rule("/work/wt", "/work/wt/dev-lane6/src/x.go"),
        None,
        "a lane named by the seat key is the seat's own"
    );
    assert_eq!(
        rule("/work/wt", "/work/wt/dev/x.go"),
        None,
        "the seat's own directory stays inside"
    );
    assert_eq!(
        rule("/work/wt", "/work/wt/devv/x.go").as_deref(),
        Some("worktree-scope"),
        "a name that merely STARTS with the key must not pass the prefix test"
    );
    assert_eq!(
        rule("/work/wt", "/work/wt/other-lane/x.go").as_deref(),
        Some("worktree-scope"),
        "another seat's lane is not this seat's"
    );
    assert_eq!(
        rule("/work/wt", "/work/x").as_deref(),
        Some("worktree-scope"),
        "the room root itself is not a lane"
    );

    let refusal = refusal_lanes(
        &dir,
        "",
        "write_to_file",
        serde_json::json!({ "TargetFile": "/work/wt/dev-lane6/src/x.go" }),
    )
    .expect("without a lane root no lane is admitted");
    assert_eq!(
        refusal,
        "blocked by seat guard (worktree-scope): /work/wt/dev-lane6/src/x.go is outside your worktrees /work/wt/dev, /work/wt/dev-client; edit inside them and report the commit to the lead",
        "the unset launch keeps today's refusal byte for byte"
    );
}
