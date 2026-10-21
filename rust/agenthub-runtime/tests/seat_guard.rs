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

/// The rule a refusal names, from the reason a seat reads: the text between the first `(` and the first `)` of
/// `blocked by seat guard (<rule>): ...`.
fn rule_of(reason: &str) -> String {
    reason
        .split(['(', ')'])
        .nth(1)
        .unwrap_or_default()
        .to_owned()
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
        let rule = rule_of(&r);
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
        .env("AGENTHUB_ROOM", "demo")
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

/// The pair the runtime marks a seat's environment with (`supervisor/mod.rs`), inherited by every descendant; the
/// harness exports the seat half as `AGENTHUB_SEAT=dev`, so the room half is `demo`.
const MARK: (&str, &str) = ("demo", "dev");

/// The pid a detached shell wrote into `file`, once it is there.
fn pid_in(file: &Path) -> u32 {
    for _ in 0..500 {
        if let Ok(text) = std::fs::read_to_string(file) {
            if let Ok(pid) = text.trim().parse() {
                return pid;
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    panic!("{} never named a pid", file.display());
}

/// Spawn `script` as the background job of a shell that then exits, carrying `mark`, and return its pid. Its parent
/// is gone, so it is outside the seat's ancestry: only its mark can attribute it (ruling 6c, rows 2-4).
fn orphaned(dir: &Path, tag: &str, mark: (&str, &str), script: &str) -> u32 {
    let file = dir.join(format!("pid-{tag}"));
    let _ = std::fs::remove_file(&file);
    Command::new("sh")
        .arg("-c")
        .arg(format!("{script} & echo $! > {}", file.display()))
        .env("AGENTHUB_ROOM", mark.0)
        .env("AGENTHUB_SEAT", mark.1)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .expect("a shell is needed to detach a target");
    pid_in(&file)
}

/// Ownership is the seat mark (the `AGENTHUB_ROOM`/`AGENTHUB_SEAT` pair) or ancestry, never the session: the eight
/// rows of `RULING-2026-10-21-pid-ownership` §6c. Rows 2-8 are red against rev-9's asset and green once §6b lands.
#[test]
fn a_pid_is_the_seats_when_it_carries_the_seat_mark_or_descends_from_it() {
    let dir = fresh_dir("pid");
    let verdict = |command: String| {
        refusal(
            &dir,
            WORKTREES,
            "run_command",
            serde_json::json!({ "CommandLine": command }),
        )
    };
    let mut spawned: Vec<u32> = Vec::new();

    // 1. A child of this very process: the ancestry branch, unchanged.
    let mut started = Command::new("sleep")
        .arg("60")
        .env("AGENTHUB_ROOM", MARK.0)
        .env("AGENTHUB_SEAT", MARK.1)
        .spawn()
        .expect("sleep");
    spawned.push(started.id());
    for command in [
        format!("kill -9 {}", started.id()),
        format!("kill -TERM {}", started.id()),
        format!("kill -s TERM {}", started.id()),
        format!("kill -0 {}", started.id()),
    ] {
        assert_eq!(verdict(command.clone()), None, "{command}");
    }

    // 2-4. Detached, so the ancestry branch cannot pass them for the wrong reason: a `nohup`'d child whose shell is
    // reaped, a grandchild behind a shell, and a process that made itself a session leader.
    for (tag, script) in [
        ("nohup", "nohup sleep 60"),
        ("grandchild", "sh -c 'sleep 60'"),
        ("setsid", "setsid sleep 60"),
    ] {
        let pid = orphaned(&dir, tag, MARK, script);
        spawned.push(pid);
        let command = format!("kill -9 {pid}");
        assert_eq!(verdict(command.clone()), None, "row {tag}: {command}");
    }

    // 5. Another seat's process: refused, and the reason names the owner seat.
    let other = orphaned(&dir, "other-seat", ("demo", "lead"), "sleep 60");
    spawned.push(other);
    let why = verdict(format!("kill {other}")).expect("another seat's process is not ours");
    assert!(why.contains("is marked"), "{why}");
    assert!(why.contains("demo/lead"), "{why}");

    // 6. A process that cleared its environment carries no mark and is nobody's.
    let bare = orphaned(&dir, "env-i", MARK, "env -i sleep 60");
    spawned.push(bare);
    let why = verdict(format!("kill {bare}")).expect("an unmarked process is not ours");
    assert!(
        why.contains("names no AGENTHUB_ROOM/AGENTHUB_SEAT"),
        "{why}"
    );

    // 7. A pid that is gone has no environment left to attribute.
    let mut reaped = Command::new("true").spawn().expect("true");
    let gone = reaped.id();
    reaped.wait().expect("true exits");
    let why = verdict(format!("kill {gone}")).expect("a reaped pid is not ours");
    assert!(why.contains("no readable environment"), "{why}");

    // 8. A process group signals every process in it, other seats' included.
    for command in [
        "kill -29162",
        "kill -- -29162",
        "kill -- -9",
        "kill 0",
        "kill -65",
    ] {
        let why = verdict(command.to_owned()).expect("a group is not a pid");
        assert!(why.contains("is a process group"), "{command}: {why}");
    }

    for pid in &spawned {
        let _ = Command::new("kill").args(["-9", &pid.to_string()]).status();
    }
    let _ = started.kill();
    let _ = started.wait();
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
/// kind of state, so they are named here too. A glob is judged as the prefix it names (`rm <seat dir>/*` is the
/// seat's directory), and one with no literal prefix is refused by name (`unresolvable-target`). `find -delete`,
/// `xargs rm` and a command inside quotes are still unjudged: the guard is a net for a seat's mistakes, not a sandbox.
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

/// The net is not a blanket ban on deleting or rewriting: a seat's own worktree and the temp dir stay its own. A
/// target the guard cannot resolve (a variable) is REFUSED by name instead - silently dropped was how the metavar
/// hole worked, and a deletion judged as if it named nothing is the one that reaches the asset.
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
        format!("sed -i s/x/y/ {WORKTREE}/main.go"),
        format!("dd if=/dev/zero of={WORKTREE}/big.bin count=1"),
        format!("mv {WORKTREE}/a.go {WORKTREE}/b.go"),
    ] {
        assert_eq!(run(cmd.clone()), None, "{cmd}");
    }
    // A target the guard cannot resolve is refused, not allowed: the same deletion with a variable was an allow
    // while the target readers dropped the metavar, so the call was judged as if it named nothing.
    assert_eq!(
        run("rm -rf $SCRATCH/x".to_string()).as_deref(),
        Some("unresolvable-target")
    );
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

/// The net is not a blanket ban on making directories: the worktree, the seat's own directory and the temp dir stay
/// the seat's own, and a segment that merely STARTS with a protected one (`hooks/preflight`) is not protected. A
/// target the guard cannot resolve (a variable) is REFUSED by name, the same flip the deletion case carries.
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
        "touch /tmp/probe-file".to_string(),
    ] {
        assert_eq!(run(cmd.clone()), None, "{cmd}");
    }
    assert_eq!(
        run("mkdir -p $SCRATCH/x".to_string()).as_deref(),
        Some("unresolvable-target")
    );
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

const COALESCE_ROOM: &str = "demo-room";
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
        .map(|r| rule_of(&r))
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

// ---- git-imports-a-tree -----------------------------------------------------------------------------------------
// A patch NAMES a file to read and a tree to write, so the protected path never appears as an argument a
// write-target rule could see: at the pin, `git apply`, `git am`, `patch`, `git stash pop`, `git checkout --` and
// `git restore` all wrote the guard that refuses them (measured 2026-10-20, task 1c06bcd0). Every case below is a
// row of that task's probe table.

/// The file the guard protects by basename, anywhere in a tree.
const GUARD_PATH: &str = "rust/agenthub-runtime/assets/seat-guard.ts";

/// One hunk that rewrites `path`, in the header form git emits, so a case differs from its neighbour only in the
/// header shape it is testing.
fn patch_doc(path: &str) -> String {
    format!(
        "diff --git a/{path} b/{path}\nindex 1111111..2222222 100644\n--- a/{path}\n+++ b/{path}\n@@ -1,2 +1,2 @@\n-old\n+new\n"
    )
}

/// A directory of patch documents, written the way a seat writes its own: the guard is handed an absolute `cd` in
/// the statement, which is the directory a patch path resolves against.
fn patch_dir(tag: &str, files: &[(&str, String)]) -> PathBuf {
    let dir = fresh_dir(tag);
    std::fs::create_dir_all(&dir).unwrap();
    for (name, body) in files {
        std::fs::write(dir.join(name), body).unwrap();
    }
    dir
}

/// A repository with two stashes: `stash@{1}` rewrites the guard, `stash@{0}` rewrites an ordinary file. Both
/// sides of the stash rule are told apart in the same run instead of in two runs that could age apart.
fn stashed(tag: &str) -> PathBuf {
    let dir = fresh_dir(tag);
    std::fs::create_dir_all(dir.join("rust/agenthub-runtime/assets")).unwrap();
    std::fs::create_dir_all(dir.join("rust/agenthub-runtime/src")).unwrap();
    let git = |args: &[&str], label: &str| {
        let status = Command::new("git")
            .args(args)
            .current_dir(&dir)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .unwrap();
        assert!(status.success(), "git {label} must succeed");
    };
    git(&["init", "-q", "."], "init");
    git(&["config", "user.email", "seat@guard.test"], "config email");
    git(&["config", "user.name", "seat"], "config name");
    std::fs::write(dir.join(GUARD_PATH), "base\n").unwrap();
    std::fs::write(dir.join("rust/agenthub-runtime/src/lib.rs"), "base\n").unwrap();
    git(&["add", "--", "rust/agenthub-runtime"], "add");
    git(&["commit", "-qm", "base"], "commit");
    std::fs::write(dir.join(GUARD_PATH), "changed\n").unwrap();
    git(
        &["stash", "push", "-q", "-m", "guard", "--", GUARD_PATH],
        "stash the guard",
    );
    std::fs::write(dir.join("rust/agenthub-runtime/src/lib.rs"), "changed\n").unwrap();
    git(
        &[
            "stash",
            "push",
            "-q",
            "-m",
            "src",
            "--",
            "rust/agenthub-runtime/src/lib.rs",
        ],
        "stash src",
    );
    dir
}

#[test]
fn a_patch_that_names_the_guard_is_refused() {
    let dir = patch_dir(
        "git-imports-headers",
        &[
            ("guard.patch", patch_doc(GUARD_PATH)),
            (
                "binary.patch",
                format!("diff --git a/{GUARD_PATH} b/{GUARD_PATH}\nnew file mode 100644\nindex 0000000..2222222\nGIT binary patch\nliteral 8\nPc$@XM0T?U00000\n"),
            ),
            (
                "deleted.patch",
                format!("diff --git a/{GUARD_PATH} b/{GUARD_PATH}\ndeleted file mode 100644\nindex 2222222..0000000\n--- a/{GUARD_PATH}\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-old\n-new\n"),
            ),
            (
                "quoted.patch",
                format!("diff --git \"a/{GUARD_PATH}\" \"b/{GUARD_PATH}\"\n--- \"a/{GUARD_PATH}\"\n+++ \"b/{GUARD_PATH}\"\n@@ -1,2 +1,2 @@\n-old\n+new\n"),
            ),
            ("hooks.patch", patch_doc("scripts/git-hooks/pre-commit")),
            ("hooksprefix.patch", patch_doc("hooks/pre/seat-guard-hook.ts")),
            ("gemini.patch", patch_doc(".gemini/config/hooks.json")),
            ("src.patch", patch_doc("rust/agenthub-runtime/src/lib.rs")),
        ],
    );
    let here = dir.display();
    // The header forms: a plain patch, one whose prefix -p2 strips, a binary block, a deletion whose `---` side
    // names the file, a quoted header, and a path inside a protected directory.
    for case in [
        "git apply guard.patch",
        "git apply -p2 guard.patch",
        "git apply binary.patch",
        "git apply deleted.patch",
        "git apply quoted.patch",
        "git apply hooks.patch",
        "git apply hooksprefix.patch",
        "git apply gemini.patch",
        "patch -p1 < guard.patch",
    ] {
        assert_eq!(
            bash(&format!("cd {here} && {case}")).as_deref(),
            Some("git-imports-a-tree"),
            "{case}"
        );
    }
    // `patch` reads the same document through a redirect, and the plain suffix test still lets an ordinary patch
    // through: -p and the include/exclude filters cannot hide a tail, so they stay allowed.
    assert_eq!(
        bash(&format!("cd {here} && git apply src.patch")),
        None,
        "a patch that names no protected path is the room's landing verb"
    );
}

#[test]
fn a_destination_flag_is_refused_on_a_writing_form() {
    let dir = patch_dir(
        "git-imports-destination",
        &[
            ("guard.patch", patch_doc(GUARD_PATH)),
            ("src.patch", patch_doc("rust/agenthub-runtime/src/lib.rs")),
        ],
    );
    let here = dir.display();
    // A header naming no protected path still lands in one when the destination is rewritten, so the flags are
    // refused rather than composed: the workaround is `cd <dir> && patch …`, which the rule already resolves.
    for case in [
        "git apply --directory=rust/agenthub-runtime/assets src.patch",
        "git apply --prefix=hooks/pre/ src.patch",
        "git apply --unsafe-paths src.patch",
        "patch -d out -p1 < src.patch",
        // The attached spellings were the gap the reviewer measured: the short-form test matched a bare `-d` only,
        // so `-dout` and a `-d` tucked behind `-p1` walked past the refusal while the split form tripped it.
        "patch -dout -p1 < src.patch",
        "patch -p1 -dhooks < src.patch",
    ] {
        assert_eq!(
            bash(&format!("cd {here} && {case}")).as_deref(),
            Some("git-imports-a-tree"),
            "{case}"
        );
    }
    // The write-nothing forms are allowed, whatever flags they carry.
    for case in [
        "git apply --check guard.patch",
        "git apply --stat guard.patch",
        "git apply --check --directory=rust/agenthub-runtime/assets src.patch",
        "patch --dry-run -p1 < guard.patch",
    ] {
        assert_eq!(bash(&format!("cd {here} && {case}")), None, "{case}");
    }
}

#[test]
fn a_patch_from_stdin_is_refused() {
    let dir = patch_dir(
        "git-imports-stdin",
        &[("guard.patch", patch_doc(GUARD_PATH))],
    );
    let here = dir.display();
    // There is no document to read, so the guard cannot see what it writes: both spellings are refused, and
    // `git am` with no argument is the same shape.
    for case in [
        "cat guard.patch | git apply",
        "git apply -",
        "git am",
        "patch -p1",
    ] {
        assert_eq!(
            bash(&format!("cd {here} && {case}")).as_deref(),
            Some("git-imports-a-tree"),
            "{case}"
        );
    }
}

#[test]
fn git_am_reads_every_message_of_an_mbox() {
    let dir = patch_dir(
        "git-imports-mbox",
        &[(
            "two.patch",
            format!(
                "From 1111111 Mon Sep 17 00:00:00 2001\nSubject: [PATCH] one\n\ndiff --git a/rust/agenthub-runtime/src/lib.rs b/rust/agenthub-runtime/src/lib.rs\n--- a/rust/agenthub-runtime/src/lib.rs\n+++ b/rust/agenthub-runtime/src/lib.rs\n@@ -1 +1 @@\n-old\n+new\n-- \n2.39.0\n\nFrom 2222222 Mon Sep 17 00:00:00 2001\nSubject: [PATCH] two\n\ndiff --git a/{GUARD_PATH} b/{GUARD_PATH}\n--- a/{GUARD_PATH}\n+++ b/{GUARD_PATH}\n@@ -1 +1 @@\n-old\n+new\n"
            ),
        )],
    );
    let here = dir.display();
    assert_eq!(
        bash(&format!("cd {here} && git am two.patch")).as_deref(),
        Some("git-imports-a-tree"),
        "the second message carries the guard"
    );
    // Its own control verbs import nothing.
    for case in ["git am --continue", "git am --abort", "git am --skip"] {
        assert_eq!(bash(&format!("cd {here} && {case}")), None, "{case}");
    }
}

#[test]
fn checkout_restore_and_stash_are_read_before_they_write() {
    let repo = stashed("git-imports-stash");
    let here = repo.display();
    // The index and a stash entry can hold a modified guard only if a write produced it; a stash the OWNER made is
    // exactly the case that must be read rather than assumed.
    for case in [
        format!("cd {here} && git stash pop stash@{{1}}"),
        format!("cd {here} && git stash apply stash@{{1}}"),
    ] {
        assert_eq!(bash(&case).as_deref(), Some("git-imports-a-tree"), "{case}");
    }
    for case in [
        format!("cd {here} && git stash pop"),
        "git checkout -- rust/agenthub-runtime/src/lib.rs".to_owned(),
        "git restore --source=HEAD~1 rust/agenthub-runtime/src/lib.rs".to_owned(),
    ] {
        assert_eq!(bash(&case), None, "{case}");
    }
    for case in [
        format!("git checkout -- {}", GUARD_PATH),
        format!("git restore --source=HEAD~1 {GUARD_PATH}"),
        format!("git restore {GUARD_PATH}"),
    ] {
        assert_eq!(bash(&case).as_deref(), Some("git-imports-a-tree"), "{case}");
    }
    // A committed tree is sanctioned content, so the verbs that only import one stay allowed.
    assert_eq!(bash("git cherry-pick abc1234"), None);
}

/// The SOURCE of an import, not the verb (revision 6, task 1c06bcd0): a stash entry IS a commit, the owner makes
/// stashes unguarded, and five verbs import one. The rule resolves each operand against `git reflog refs/stash`, so a
/// plain commit stays the sanctioned path while an entry that carries the guard is refused whichever verb reaches it.
#[test]
fn a_stash_entry_is_read_whatever_verb_imports_it() {
    let repo = stashed("git-imports-stash-source");
    let git = |args: &[&str]| {
        let status = Command::new("git")
            .args(args)
            .current_dir(&repo)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .unwrap();
        assert!(status.success(), "git {args:?} must succeed");
    };
    // One branch that differs only outside the protected paths, so the last control below is not vacuous.
    let base = String::from_utf8(
        Command::new("git")
            .args(["rev-parse", "--abbrev-ref", "HEAD"])
            .current_dir(&repo)
            .output()
            .unwrap()
            .stdout,
    )
    .unwrap()
    .trim()
    .to_owned();
    git(&["checkout", "-q", "-b", "side"]);
    std::fs::write(repo.join("rust/agenthub-runtime/src/lib.rs"), "side\n").unwrap();
    git(&["add", "--", "rust/agenthub-runtime/src/lib.rs"]);
    git(&["commit", "-qm", "side"]);
    git(&["checkout", "-q", &base]);
    let here = repo.display();
    // Nine refusals: the stash entry is reached by name in each, and the alias row is one tool call, where the name
    // does not exist yet at hook time - so it is the statement's own memory of the branch that catches it.
    for case in [
        "git stash branch b stash@{1}".to_owned(),
        "git cherry-pick stash@{1}".to_owned(),
        "git merge stash@{1}".to_owned(),
        "git rebase stash@{1}".to_owned(),
        "git checkout stash@{1}".to_owned(),
        "git switch stash@{1}".to_owned(),
        "git switch -c x stash@{1}".to_owned(),
        "git branch sneaky stash@{1} && git cherry-pick sneaky".to_owned(),
        format!("git stash push -- {GUARD_PATH}"),
    ] {
        assert_eq!(
            bash(&format!("cd {here} && {case}")).as_deref(),
            Some("git-imports-a-tree"),
            "{case}"
        );
    }
    // The path-free push reverts whatever the tree changed to HEAD, so the tree itself is read.
    std::fs::write(repo.join(GUARD_PATH), "dirty\n").unwrap();
    assert_eq!(
        bash(&format!("cd {here} && git stash push -m x")).as_deref(),
        Some("git-imports-a-tree"),
        "a path-free stash push over a modified guard"
    );
    std::fs::write(repo.join(GUARD_PATH), "base\n").unwrap();
    // Five controls: a plain commit, a name it creates, a branch that differs only outside the protected paths, and
    // the two read-only views of the stash.
    for case in [
        "git cherry-pick HEAD".to_owned(),
        "git switch -c y HEAD".to_owned(),
        "git checkout side".to_owned(),
        "git stash list".to_owned(),
        "git stash show -p".to_owned(),
    ] {
        assert_eq!(bash(&format!("cd {here} && {case}")), None, "{case}");
    }
}

/// Staging writes by PATH (the index-route addendum of 2026-10-21, task 1c06bcd0): `git update-index --add --cacheinfo` writes the index and not
/// the file, so one `git checkout-index -f` afterwards puts a modified guard on disk without a single refused write -
/// the chain the reviewer measured, which falsified revision 5's premise. The verbs are read as names of what they
/// write; `--index-info` has no name (its rows come from stdin) and `checkout-index --prefix` names its own
/// destination, so both fail closed on this same rule.
#[test]
fn the_index_is_written_by_path_so_the_path_is_read() {
    let repo = stashed("guard-index-writes");
    let here = repo.display();
    let blob = String::from_utf8(
        Command::new("git")
            .args(["rev-parse", &format!("HEAD:{GUARD_PATH}")])
            .current_dir(&repo)
            .output()
            .unwrap()
            .stdout,
    )
    .unwrap()
    .trim()
    .to_owned();
    // Eight refusals: both spellings of --cacheinfo, the stdin form, a named path in three verbs, the
    // destination-rewriting form, and `commit --`.
    for case in [
        format!("git update-index --add --cacheinfo 100644,{blob},{GUARD_PATH}"),
        format!("git update-index --add --cacheinfo=100644,{blob},{GUARD_PATH}"),
        "git update-index --index-info".to_owned(),
        format!("git update-index --assume-unchanged -- {GUARD_PATH}"),
        "git checkout-index --prefix=/tmp/x/ -- rust/agenthub-runtime/src/lib.rs".to_owned(),
        format!("git add -- {GUARD_PATH}"),
        format!("git rm --cached -- {GUARD_PATH}"),
        format!("git commit -m x -- {GUARD_PATH}"),
    ] {
        assert_eq!(
            bash(&format!("cd {here} && {case}")).as_deref(),
            Some("git-imports-a-tree"),
            "{case}"
        );
    }
    // Four controls: the same verbs on a path that is not the owner's, and the no-op refresh.
    for case in [
        "git update-index --refresh".to_owned(),
        "git add -n -- rust/agenthub-runtime/src/lib.rs".to_owned(),
        "git rm -n -- rust/agenthub-runtime/src/lib.rs".to_owned(),
        "git commit -m note -- rust/agenthub-runtime/src/lib.rs".to_owned(),
    ] {
        assert_eq!(bash(&format!("cd {here} && {case}")), None, "{case}");
    }
    // The asset is the owner's (owner ruling, 2026-10-21): the index route is refused to the lead like any seat.
    assert!(
        refusal_for_seat(
            &fresh_dir("lead-index"),
            WORKTREES,
            "agenthub--room--lead",
            "lead",
            "run_command",
            serde_json::json!({ "CommandLine": format!("cd {here} && git add -- {GUARD_PATH}") }),
        )
        .is_some(),
        "the lead does not stage the guard"
    );
}

/// The index is a MEDIUM (the index-route addendum of 2026-10-21): the verbs that name no path - `commit`, and
/// `checkout-index -a` - turn whatever the index holds into a commit or a file, so the index itself is read; a
/// directory operand names the guard inside it without naming it; and `read-tree` of a STASH entry imports one.
/// The asset is the owner's, so every one of these paths is refused to every seat, the lead included.
#[test]
fn the_index_is_read_where_the_verb_names_no_path() {
    let repo = stashed("guard-index-parity");
    let here = repo.display();
    let blob = |spec: &str| {
        String::from_utf8(
            Command::new("git")
                .args(["rev-parse", spec])
                .current_dir(&repo)
                .output()
                .unwrap()
                .stdout,
        )
        .unwrap()
        .trim()
        .to_owned()
    };
    // A blob that DIFFERS from HEAD's: the index route is about replacing the entry, and an entry equal to HEAD's
    // is invisible to the parity read - so the fixture writes the seat's bytes first and hashes them. The file lives
    // in a directory of this run's own, because the temp root is shared across runs and two runs would hash each
    // other's bytes.
    let carried_dir = fresh_dir("guard-index-carried");
    std::fs::create_dir_all(&carried_dir).unwrap();
    let carried = carried_dir.join("carried-guard.ts");
    std::fs::write(&carried, "const victim = 1;\n").unwrap();
    let guard_blob = String::from_utf8(
        Command::new("git")
            .args(["hash-object", "-w", carried.to_str().unwrap()])
            .current_dir(&repo)
            .output()
            .unwrap()
            .stdout,
    )
    .unwrap()
    .trim()
    .to_owned();
    let src_blob = blob("HEAD:rust/agenthub-runtime/src/lib.rs");
    // Six controls first: a commit-relative tree and a HEAD reset, the same verbs on paths that are not the
    // owner's, and a directory rename outside the protected paths.
    for case in [
        "git read-tree -u --reset HEAD".to_owned(),
        // The reset that keeps its meaning: an unstaging and a HEAD reset write nothing protected.
        "git reset".to_owned(),
        "git reset --mixed HEAD".to_owned(),
        // The NAMED form of checkout-index stays useful; the whole-index forms below are refused.
        "git checkout-index -f -- rust/agenthub-runtime/src/lib.rs".to_owned(),
        format!("git update-index --cacheinfo 100644,{src_blob},rust/agenthub-runtime/src/lib.rs"),
        "git mv rust/agenthub-runtime/src/lib.rs rust/agenthub-runtime/src/moved.ts".to_owned(),
    ] {
        assert_eq!(bash(&format!("cd {here} && {case}")), None, "{case}");
    }
    // Eight refusals that need no poisoned index: the stdin form, a directory operand, a rename, a stash tree.
    // (`--index-info` is pinned once, in the block above; a second copy adds no coverage.)
    for case in [
        "git update-index --stdin".to_owned(),
        "git rm -r rust/agenthub-runtime/assets".to_owned(),
        format!("git mv {GUARD_PATH} rust/agenthub-runtime/assets/copy.ts"),
        "git read-tree -u --reset stash@{1}".to_owned(),
        // One step removed from read-tree: `reset --mixed <stash>` puts the entry in the index, which is what
        // `checkout-index -a` or a whole-index commit would then carry (the reviewer's measured chain).
        "git reset --mixed stash@{1}".to_owned(),
        "git reset stash@{1}".to_owned(),
        // Whole-index materialisation, refused outright (lead, 2026-10-21): the pair `reset --mixed <stash>`
        // then `checkout-index -a -f` needs no index the hook can see as dirty at call time.
        "git checkout-index -a -f".to_owned(),
        "git checkout-index --all -f".to_owned(),
        // The bare `checkout-index -f` is NOT in this list: probed, it exits 0 and writes nothing (the
        // materialisation needs `-a` or `--all`), so a refusal pinned on it would be an over-refusal.
    ] {
        assert_eq!(
            bash(&format!("cd {here} && {case}")).as_deref(),
            Some("git-imports-a-tree"),
            "{case}"
        );
    }
    // The two parity refusals need the index poisoned OUT OF BAND, which is what the fixture does: the hook sees
    // only a command line, and `git commit` / `git checkout-index -a` name nothing at all.
    let poison = Command::new("git")
        .args([
            "update-index",
            "--add",
            "--cacheinfo",
            &format!("100644,{guard_blob},{GUARD_PATH}"),
        ])
        .current_dir(&repo)
        .status()
        .unwrap();
    assert!(
        poison.success(),
        "the fixture must be able to poison the index"
    );
    for case in ["git commit -m x", "git checkout-index -a -f"] {
        assert_eq!(
            bash(&format!("cd {here} && {case}")).as_deref(),
            Some("git-imports-a-tree"),
            "{case} over a poisoned index"
        );
    }
    // The control that keeps the parity honest: the same commit over a CLEAN index is the seat's own.
    assert!(Command::new("git")
        .args(["reset", "-q"])
        .current_dir(&repo)
        .status()
        .unwrap()
        .success());
    assert_eq!(
        bash(&format!("cd {here} && git commit -m x")),
        None,
        "git commit -m x over a clean index"
    );
    // The asset is the owner's (owner ruling, 2026-10-21): every route that would write `seat-guard.ts` - a commit by
    // pathspec, a patch reader, a write tool - is refused to the lead exactly as to any other seat.
    let patches = patch_dir(
        "seat-asset-patch",
        &[
            ("guard.patch", patch_doc(GUARD_PATH)),
            (
                "both.patch",
                format!(
                    "{}{}",
                    patch_doc(GUARD_PATH),
                    patch_doc("scripts/git-hooks/pre-commit")
                ),
            ),
        ],
    );
    for (session, key) in [
        ("agenthub--room--lead", "lead"),
        ("agenthub--room--go-dev", "go-dev"),
    ] {
        for refused in [
            serde_json::json!({ "CommandLine": format!("cd {here} && git commit -m z -- {GUARD_PATH}") }),
            serde_json::json!({ "CommandLine": format!("cd {} && git apply guard.patch", patches.display()) }),
            serde_json::json!({ "CommandLine": format!("cd {} && patch -p1 < guard.patch", patches.display()) }),
            serde_json::json!({ "CommandLine": format!("cd {} && git apply both.patch", patches.display()) }),
            serde_json::json!({ "CommandLine": format!("cd {here} && git mv {GUARD_PATH} hooks/pre/seat-guard.ts") }),
            serde_json::json!({ "TargetFile": GUARD_PATH }),
            serde_json::json!({ "TargetFile": "scripts/git-hooks/pre-commit" }),
        ] {
            let name = if refused.get("TargetFile").is_some() {
                "write_to_file"
            } else {
                "run_command"
            };
            let seen = rule_of(
                &refusal_for_seat(
                    &fresh_dir(&format!("{key}-asset")),
                    WORKTREES,
                    session,
                    key,
                    name,
                    refused.clone(),
                )
                .unwrap_or_default(),
            );
            // A `run_command` that names the asset is read by the shell's import rule; a `write_to_file` names its
            // path, so the write-target rule answers it. The pair is pinned, not "either rule".
            let want = if name == "write_to_file" {
                "protected-paths"
            } else {
                "git-imports-a-tree"
            };
            assert_eq!(
                seen, want,
                "no seat lands the guard, {key} included: {refused} answers {seen}"
            );
        }
    }
}

#[test]
fn the_refusals_name_no_deploy_and_no_ownership_claim() {
    // The needle is spelled in two pieces so the file that forbids the claim is not itself a hit for it: the task's
    // check is a repository-wide grep, and an assertion quoting the sentence would fail that grep.
    let forge = concat!("deploys ", "production");
    let asset = std::fs::read_to_string(guard()).unwrap();
    assert!(
        !asset.contains(forge),
        "the guard's own words must not claim that a push to main ships a deploy (task 59a3f1b1)"
    );
    let push = refusal(
        &fresh_dir("guard-push-wording"),
        WORKTREES,
        "run_command",
        serde_json::json!({ "CommandLine": "git push origin main" }),
    )
    .expect("a seat still does not push");
    assert!(
        push.contains("only the lead pushes") && !push.contains("deploy"),
        "the push refusal names who may push and no deploy model: {push}"
    );
    let docker = refusal(
        &fresh_dir("guard-docker-wording"),
        WORKTREES,
        "run_command",
        serde_json::json!({ "CommandLine": "docker ps" }),
    )
    .expect("a seat still does not run docker");
    assert!(
        !docker.contains("deploy"),
        "the docker refusal asserts no deploy model: {docker}"
    );
}

/// A repository whose HEAD tracks the guard beside two ordinary files, one of them the source `git add src/` names.
/// The probes below are the architect's ruling, run for real: every one of the six was ALLOWED at revision 8 and
/// landed the guard's deletion or its bytes in HEAD.
fn tracked(tag: &str) -> PathBuf {
    let dir = fresh_dir(tag);
    std::fs::create_dir_all(dir.join("rust/agenthub-runtime/assets")).unwrap();
    std::fs::create_dir_all(dir.join("rust/agenthub-runtime/src")).unwrap();
    std::fs::create_dir_all(dir.join("other")).unwrap();
    let git = |args: &[&str], label: &str| {
        let status = Command::new("git")
            .args(args)
            .current_dir(&dir)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .unwrap();
        assert!(status.success(), "git {label} must succeed");
    };
    git(&["init", "-q", "."], "init");
    git(&["config", "user.email", "seat@guard.test"], "email");
    git(&["config", "user.name", "seat"], "name");
    std::fs::write(dir.join(GUARD_PATH), "const real = 1;\n").unwrap();
    std::fs::write(dir.join("rust/agenthub-runtime/src/lib.rs"), "a\n").unwrap();
    std::fs::write(dir.join("other/one.txt"), "c\n").unwrap();
    std::fs::write(dir.join("newfile"), "n\n").unwrap();
    git(&["add", "--", "."], "add");
    git(&["commit", "-qm", "base"], "commit");
    dir
}

#[test]
fn the_pathspec_is_read_as_the_set_it_covers() {
    let repo = tracked("guard-pathspec-set");
    let here = repo.display();
    let at = |case: &str| bash(&format!("cd {here} && {case}"));
    // Twelve refusals and one chain (revision 9). A directory operand names the guard inside it, so every one of
    // these is refused before it runs - the shell forms by the set the write target covers, the git forms by the set
    // the pathspec covers, and the five whole-tree forms by naming no path at all.
    for case in [
        "rm -r rust/agenthub-runtime/assets",
        "mv rust/agenthub-runtime/assets /tmp/assets-moved",
    ] {
        assert_eq!(at(case).as_deref(), Some("protected-paths"), "{case}");
    }
    for case in [
        "git add rust/agenthub-runtime/assets",
        "git commit -m x -- rust/agenthub-runtime/assets",
        "git checkout HEAD -- rust/agenthub-runtime/assets",
    ] {
        assert_eq!(at(case).as_deref(), Some("git-imports-a-tree"), "{case}");
    }
    for case in [
        "git add -u",
        "git add -a",
        "git add -p",
        "git commit -am x",
        "git commit -a -m x",
    ] {
        assert_eq!(at(case).as_deref(), Some("git-destructive"), "{case}");
    }
    // The chain the reviewer executed: one call, three calls, same verdict at the first statement that would. The
    // rule that answers is the first one in the list to fire - `git-imports-a-tree` reads the `git add` statement
    // before `protected-paths` is reached - and what matters is that the call does not run.
    assert_eq!(
        at("rm -r rust/agenthub-runtime/assets && git add rust/agenthub-runtime/assets && git commit -m x -- rust/agenthub-runtime/assets")
            .as_deref(),
        Some("git-imports-a-tree")
    );
    // One level up, and a copy INTO the protected directory: a path no rule saw at revision 8.
    for case in [
        "rm -r rust/agenthub-runtime",
        "cp other/one.txt rust/agenthub-runtime/assets/",
    ] {
        assert_eq!(at(case).as_deref(), Some("protected-paths"), "{case}");
    }
    // The controls the ruling pins as allowed: an over-broad refusal would be a finding. `git add src/` and a
    // directory rename outside the protected paths are habits, and both stay allowed.
    for case in [
        "git add -- rust/agenthub-runtime/src/lib.rs",
        "git add -N -- newfile",
        "git add rust/agenthub-runtime/src/",
        "git commit -m x -- rust/agenthub-runtime/src/lib.rs",
        "git commit -m x",
        "git rm -r other",
        "git mv other other2",
        "git checkout HEAD -- other",
        "rm -r other",
        "git read-tree -u --reset HEAD",
        "git diff HEAD -- rust/agenthub-runtime/src/lib.rs",
    ] {
        assert_eq!(at(case), None, "{case}");
    }
}

/// A target is the PATH, not the spelling (the architect's 2026-10-21 quoting and pathspec ruling): a quoted word
/// names the same file as the same word unquoted. Every row is a probe the ruling measured; the controls are the
/// over-refusals it forbids, so a guard that refused every quoted word or every command word would fail here beside
/// the one that judged none of them.
#[test]
fn a_quoted_target_is_judged_by_the_rule_its_unquoted_control_is() {
    let repo = tracked("guard-quoted-target");
    let here = repo.display();
    let at = |case: &str| bash(&format!("cd {here} && {case}"));
    let asset = GUARD_PATH;

    for case in [
        format!("cp evil.ts \"{asset}\""),
        format!("cp evil.ts '{asset}'"),
        format!("printf x > \"{asset}\""),
        format!("printf x >> \"{asset}\""),
        format!("printf x 2> \"{asset}\""),
        format!("dd if=evil.ts of=\"{asset}\""),
        format!("install evil.ts \"{asset}\""),
        format!("mv evil.ts \"{asset}\""),
        format!("rsync -a evil.ts \"{asset}\""),
        format!("echo evil | tee \"{asset}\""),
        format!("sed -i s/o/e/ \"{asset}\""),
        format!("truncate -s 0 \"{asset}\""),
        format!("unlink \"{asset}\""),
        format!("touch \"{asset}\""),
        "mkdir -p \"rust/agenthub-runtime/assets\"".to_owned(),
        "rm -r \"rust/agenthub-runtime/assets\"".to_owned(),
        // The unquoted control each quoted row above is judged by: the same path, the same rule.
        format!("cp evil.ts {asset}"),
        format!("printf x > {asset}"),
        format!("printf x >> {asset}"),
        format!("printf x 2> {asset}"),
        format!("dd if=evil.ts of={asset}"),
        format!("install evil.ts {asset}"),
        format!("mv evil.ts {asset}"),
        format!("rsync -a evil.ts {asset}"),
        format!("echo evil | tee {asset}"),
        format!("sed -i s/o/e/ {asset}"),
        format!("truncate -s 0 {asset}"),
        format!("unlink {asset}"),
        format!("touch {asset}"),
        "mkdir -p rust/agenthub-runtime/assets".to_owned(),
        // The two glob forms of the same directory.
        "rm -r rust/agenthub-runtime/assets/*".to_owned(),
        "rm -r 'rust/agenthub-runtime/assets'/*".to_owned(),
    ] {
        assert_eq!(at(&case).as_deref(), Some("protected-paths"), "{case}");
    }

    // The controls: a glob WITH a literal prefix the rules can read, a quoted word that is not a command, a path with
    // a space that is not protected, and a redirect whose word is quoted rather than a command's. An over-broad
    // refusal is a finding, so these must pass beside the rows above.
    for case in [
        "rm -r 'build'/*".to_owned(),
        "rm -rf dist/*".to_owned(),
        "cp a/*.ts dir/".to_owned(),
        "echo \"git push origin main\"".to_owned(),
        "echo \"a;b\"".to_owned(),
        "echo \"a; git push origin main\"".to_owned(),
        format!("echo \"x > {asset}\""),
        "printf x > \"a b.txt\"".to_owned(),
        "cp evil.ts \"my file.ts\"".to_owned(),
        "cp a b".to_owned(),
    ] {
        assert_eq!(at(&case), None, "{case}");
    }

    // A quoted path keeps its SPACE and loses its quotes: the refusal names one path, not two words.
    let reason = refusal(
        &fresh_dir("guard-quoted-space"),
        WORKTREES,
        "run_command",
        serde_json::json!({ "CommandLine": "printf x > \"/home/other/main/my file.ts\"" }),
    )
    .expect("a redirect outside the worktrees is refused");
    assert!(
        reason.contains("/home/other/main/my file.ts is outside"),
        "the quoted path keeps its space: {reason}"
    );
}

/// The directory a `cd` names is the DIRECTORY, not its spelling. The extractor resolves a relative target against
/// the directory the call runs in, so `cd "rust/agenthub-runtime"` read through the erasure's blanked word resolved
/// to `""`, left the base where it was, and the next statement's `assets` was judged as a path outside the guard
/// (measured by the gate 2026-10-21: `cd "<tree>" && rm -r assets` was ALLOW, and `git -C "<tree>"` the same). Each
/// quoted row stands beside its unquoted control, and the reads stay allowed.
#[test]
fn a_quoted_cd_names_the_directory_the_next_statement_writes_in() {
    let repo = tracked("guard-quoted-cd");
    let here = repo.display();
    let at = |case: &str| bash(&format!("cd {here} && {case}"));

    // The unquoted control first, then the same statement with the directory quoted both ways.
    for case in [
        "cd rust/agenthub-runtime && rm -r assets",
        "cd \"rust/agenthub-runtime\" && rm -r assets",
        "cd 'rust/agenthub-runtime' && rm -r assets",
        "cd rust/agenthub-runtime && rm -r assets/*",
        "cd \"rust/agenthub-runtime\" && rm -r assets/*",
        "cd rust/agenthub-runtime && mv assets /tmp/gone",
        "cd \"rust/agenthub-runtime\" && mv assets /tmp/gone",
        "cd \"rust/agenthub-runtime\" && touch assets/seat-guard.ts",
    ] {
        assert_eq!(at(case).as_deref(), Some("protected-paths"), "{case}");
    }
    for case in [
        "cd rust/agenthub-runtime && git add assets",
        "cd \"rust/agenthub-runtime\" && git add assets",
        "git -C \"rust/agenthub-runtime\" rm -r assets",
        "git -C \"rust/agenthub-runtime\" add assets",
    ] {
        assert_eq!(at(case).as_deref(), Some("git-imports-a-tree"), "{case}");
    }

    // Reading under the same directory stays the seat's own, and so does a directory the rules do not rest on.
    for case in [
        "cd \"rust/agenthub-runtime\" && ls assets",
        "cd \"rust/agenthub-runtime\" && cat assets/seat-guard.ts",
        "git -C \"rust/agenthub-runtime\" status",
        "cd \"a b\" && ls",
        "cd \"build\" && rm -r x",
        // A group is its own scope: the write inside it is judged there, the one after it is judged in the call's
        // own directory, and a group is not a way to hide either.
        "(cd rust/agenthub-runtime) && rm -r assets",
        "(cd build && rm -r x)",
        "(cd /tmp && ls)",
        "(cd \"rust/agenthub-runtime\" && ls assets)",
        "cd rust/agenthub-runtime && (ls assets)",
    ] {
        assert_eq!(at(case), None, "{case}");
    }

    // A group's own `cd` moves the base for the statements inside it (M3', measured by the gate 2026-10-21: both
    // group forms were ALLOWED at rev-9, because the delimiter stayed on the command word).
    for case in [
        "(cd rust/agenthub-runtime && rm -r assets)",
        "{ cd rust/agenthub-runtime && rm -r assets; }",
        "cd rust/agenthub-runtime && (rm -r assets)",
    ] {
        assert_eq!(at(case).as_deref(), Some("protected-paths"), "{case}");
    }
    for case in [
        "(cd rust/agenthub-runtime && git add assets)",
        "{ cd \"rust/agenthub-runtime\" && git add assets; }",
    ] {
        assert_eq!(at(case).as_deref(), Some("git-imports-a-tree"), "{case}");
    }

    // A `cd` inside an `if`/`while`/`for` body is the same shell, not a subshell: the directory it moves to is the
    // one the statements after the compound write in. Measured by the gate 2026-10-21 (VERDICT-2026-10-21-m3p.md):
    // every row below is ALLOW on the 1504-line image `23003fc6`, because the `cd` read stops at `then`/`do`.
    for case in [
        "if true; then cd rust/agenthub-runtime; fi && rm -r assets",
        "if true; then cd rust/agenthub-runtime && rm -r assets; fi",
        "if true; then cd rust/agenthub-runtime; fi; rm -r assets",
        "if true; then cd rust/agenthub-runtime; else true; fi && rm -r assets",
        "while true; do cd rust/agenthub-runtime; break; done && rm -r assets",
        "for f in x; do cd rust/agenthub-runtime; done && rm -r assets",
    ] {
        assert_eq!(at(case).as_deref(), Some("protected-paths"), "{case}");
    }
    assert_eq!(
        at("if true; then cd rust/agenthub-runtime; fi && git add assets").as_deref(),
        Some("git-imports-a-tree")
    );

    // The reads stay the seat's own: a compound whose body only looks at the tree writes nothing.
    for case in [
        "if true; then ls assets; fi",
        "if true; then cd rust/agenthub-runtime; fi && ls assets",
        "while true; do ls assets; done",
        "for f in x; do cd /tmp; done && ls",
    ] {
        assert_eq!(at(case), None, "{case}");
    }

    // A `cd` operand with no literal prefix is refused by name, not resolved as a directory that cannot exist: the
    // base it would leave behind is one the guard only thinks it knows.
    for case in [
        "cd $D && rm -r assets",
        "cd \"$D\" && rm -r assets",
        "cd ${D} && rm -r assets",
        "cd $D && git add assets",
    ] {
        assert_eq!(at(case).as_deref(), Some("unresolvable-target"), "{case}");
    }
}

/// A word the guard cannot resolve is REFUSED, not dropped: a variable (quoted or not), a glob with no literal
/// prefix, and the frozen plan files reached through a glob. Each was silently an allow while the erasure dropped
/// quoted spans and `NOT_A_TARGET` dropped metavariables, so a call was judged as if it named nothing.
#[test]
fn a_word_the_guard_cannot_resolve_is_refused() {
    let repo = tracked("guard-unresolvable-target");
    let here = repo.display();
    let at = |case: &str| bash(&format!("cd {here} && {case}"));

    // A word that names no readable path at all: a variable (quoted or not) and a glob with no literal prefix. Each
    // used to be dropped by the target readers, so the call was judged as if it named nothing.
    for case in [
        "cp evil.ts $TARGET".to_owned(),
        "rm -r \"$DIR/assets\"".to_owned(),
        "rm -r *".to_owned(),
    ] {
        assert_eq!(at(&case).as_deref(), Some("unresolvable-target"), "{case}");
    }

    for case in ["echo x >> NEXT-GEN*.md", "rm -rf ai_docs/reports-status/*"] {
        assert_eq!(at(case).as_deref(), Some("frozen-paths"), "{case}");
    }
}

/// A PATHSPEC read from a file, or from stdin, names a set the guard never sees: the ruling refuses the flag
/// outright on every verb that carries it, in both spellings, and the older allowance for `-`/`/dev/stdin` goes
/// with the rest. A file of paths is a document, the same class as a patch document.
#[test]
fn a_pathspec_file_is_refused_whatever_verb_reads_it() {
    let repo = tracked("guard-pathspec-file");
    let here = repo.display();
    let at = |case: &str| bash(&format!("cd {here} && {case}"));

    // A PATHSPEC read from a file, and one read from stdin: the rows are written by the seat and never appear on the
    // command line, so no rule can read which paths they name. `=-` and `=/dev/stdin` were the only spellings the
    // older rule saw.
    for case in [
        "git add --pathspec-from-file=psf.txt".to_owned(),
        "git add --pathspec-from-file psf.txt".to_owned(),
        "git rm -r --pathspec-from-file=psf.txt".to_owned(),
        "git commit -m x --pathspec-from-file=psf.txt".to_owned(),
        "git checkout --pathspec-from-file=psf.txt".to_owned(),
        "git restore --pathspec-from-file=psf.txt".to_owned(),
        "git reset --pathspec-from-file=psf.txt".to_owned(),
        "git stash push --pathspec-from-file=psf.txt".to_owned(),
        "git add --pathspec-from-file=-".to_owned(),
        "git add --pathspec-from-file=/dev/stdin".to_owned(),
    ] {
        assert_eq!(at(&case).as_deref(), Some("pathspec-from-file"), "{case}");
    }

    for case in ["echo x >> NEXT-GEN*.md", "rm -rf ai_docs/reports-status/*"] {
        assert_eq!(at(case).as_deref(), Some("frozen-paths"), "{case}");
    }
}

/// The redactor through its own seam, `node seat-guard.ts --redact`: the function the post-execution hook applies
/// to every tool result, so the model and the transcript never read the value. `env` is the seat's environment.
fn redacted(env: &[(&str, &str)], text: &str) -> String {
    let mut command = Command::new("node");
    command
        .arg(guard())
        .arg("--redact")
        // The child gets a directory of its own, so a redactor that writes (or fails) into the seat's real agent
        // directory cannot pass on a file the test harness happened to leave there.
        .env("PI_CODING_AGENT_DIR", fresh_dir("redact"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    for (name, value) in env {
        command.env(name, value);
    }
    let mut child = command
        .spawn()
        .expect("node is needed to run the seat guard");
    child
        .stdin
        .take()
        .unwrap()
        .write_all(text.as_bytes())
        .unwrap();
    let out = child.wait_with_output().unwrap();
    assert!(out.status.success(), "the redactor answers and exits 0");
    String::from_utf8(out.stdout).unwrap()
}

/// The measured leak (2026-10-21): a value printed WITHOUT its name - a webhook config, a `printenv` - reached a
/// provider. A value the seat carries is replaced wherever it appears, and the secret shapes are replaced even
/// when no variable names them; the text around them, newlines included, is kept.
#[test]
fn a_tool_result_loses_the_secret_values_and_keeps_its_shape() {
    let carried = "fake0carried0value0for0test";
    let shape =
        format!("url: https://x/triggerbuild?token={carried}\nplain {carried}\nnext line\n");
    let out = redacted(&[("AGENTHUB_TOKEN", carried)], &shape);
    assert!(!out.contains(carried), "{out}");
    assert!(out.contains("<redacted:AGENTHUB_TOKEN>"), "{out}");
    assert_eq!(
        out.matches('\n').count(),
        shape.matches('\n').count(),
        "the result keeps its line count: {out:?}"
    );

    let out = redacted(&[], "AGENTHUB_TOKEN=abc123def456\nkeep this\n");
    assert!(!out.contains("abc123def456"), "{out}");
    assert!(out.contains("keep this\n"), "{out}");

    let jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0LW9ubHkifQ.ZmFrZS1zaWduYXR1cmUtdGVzdA";
    let key = "sk-fake0test0key0only0000000";
    let out = redacted(
        &[],
        &format!("a {jwt} b\nc {key} d\nAuthorization: Bearer fake0bearer0value0x\n"),
    );
    for secret in [jwt, key, "fake0bearer0value0x"] {
        assert!(!out.contains(secret), "{secret} survived: {out}");
    }
    assert!(
        out.contains("<redacted:jwt>") && out.contains("<redacted:key>"),
        "each shape is named in its placeholder: {out}"
    );
    assert!(out.starts_with("a ") && out.contains(" b\nc "), "{out}");

    // The shape reads a NAME this seat does not carry: no environment of the test names DB_PASSWORD, so only the
    // `NAME=value` pattern can take `hunter2` out of the result.
    let pair = redacted(&[], "DB_PASSWORD=hunter2\nkeep me\n");
    assert!(
        !pair.contains("hunter2"),
        "the value must not survive: {pair}"
    );
    assert!(pair.contains("keep me"), "{pair}");

    let clean = "git status\nOn branch main\n";
    assert_eq!(
        redacted(&[], clean),
        clean,
        "a result with no secret is unchanged"
    );
}

/// The handler the LEAK was in, exercised where omp runs it: the asset's default export is handed a fake `pi` that
/// records the handlers it registers, a `tool_call` is fired to open a call, then a `tool_result` carrying a value in
/// its text. A patch that deletes the `tool_result` handler and keeps `--redact` passes every row above, so this is
/// the only pin on the override the model actually reads (`pi.on("tool_result")`).
#[test]
fn the_result_handler_redacts_what_the_model_reads() {
    let dir = fresh_dir("guard-tool-result");
    std::fs::create_dir_all(&dir).unwrap();
    let carried = "fake0carried0value0for0driver";
    let asset = serde_json::to_string(&guard().display().to_string()).unwrap();
    let driver = dir.join("driver.mjs");
    std::fs::write(
        &driver,
        format!(
            r#"import hook from {asset};
const handlers = new Map();
hook({{ on(name, fn) {{ handlers.set(name, fn); }} }});
if (!handlers.has("tool_result")) {{
  process.stdout.write(JSON.stringify({{ handlers: [...handlers.keys()] }}));
}} else {{
  const call = await handlers.get("tool_call")({{
    toolName: "bash",
    toolCallId: "call-1",
    input: {{ command: "git status" }},
  }});
  const out = await handlers.get("tool_result")({{
    toolCallId: "call-1",
    isError: false,
    content: [{{ type: "text", text: "printenv said: " + process.env.AGENTHUB_TOKEN }}],
  }});
  process.stdout.write(JSON.stringify({{ call: call ?? null, out: out ?? null }}));
}}
"#
        ),
    )
    .unwrap();

    let out = Command::new("node")
        .arg(&driver)
        .env("AGENTHUB_SEAT", "dev")
        .env("AGENTHUB_SESSION_NAME", "agenthub--room--dev")
        .env("AGENTHUB_WORKTREES", WORKTREES)
        .env("PI_CODING_AGENT_DIR", &dir)
        .env("AGENTHUB_TOKEN", carried)
        .env_remove("OPENRIG_SESSION_NAME")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output()
        .expect("node is needed to run the seat guard");
    assert!(
        out.status.success(),
        "the driver runs: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let answer: serde_json::Value =
        serde_json::from_slice(&out.stdout).expect("the driver answers JSON");
    assert!(
        answer.get("handlers").is_none(),
        "the asset registers no tool_result handler: it answers {:?}",
        answer["handlers"]
    );
    assert!(
        answer["call"].is_null(),
        "git status is allowed, so the call opens: {:?}",
        answer["call"]
    );
    let text = answer["out"]["content"][0]["text"]
        .as_str()
        .unwrap_or_else(|| panic!("the result handler answers content: {answer}"));
    assert!(
        !text.contains(carried),
        "the value must not reach the model: {text}"
    );
    assert!(
        text.contains("<redacted:AGENTHUB_TOKEN>"),
        "the placeholder names what left the text: {text}"
    );
}

#[test]
fn an_environment_dump_is_refused_and_a_scoped_environment_is_not() {
    refused(
        "env-dump",
        &[
            "printenv",
            "printenv AGENTHUB_TOKEN",
            "env",
            "env | grep -c TOKEN",
            "export -p",
            "declare -p",
            "declare -p AGENTHUB_TOKEN",
            "set",
        ],
    );
    allowed(&[
        "env -u TMUX tmux ls",
        "FOO=1 git status",
        "set -e",
        "compgen -v > /dev/null",
    ]);
}

#[test]
fn a_gh_api_read_of_a_credential_surface_is_refused_and_ci_reads_are_not() {
    refused(
        "gh-api-secret",
        &[
            "gh api repos/o/r/hooks",
            "gh api /repos/o/r/actions/secrets",
            "gh api repos/o/r/keys",
        ],
    );
    allowed(&[
        "gh api repos/o/r/actions/runs",
        "gh pr list",
        "gh api repos/o/r/pulls?state=open",
    ]);
}

/// One claude-code hook call, `node seat-guard.ts --claude-hook <phase>`, with claude's payload on stdin: the
/// answer it printed, parsed.
fn claude_hook(dir: &Path, phase: &str, payload: serde_json::Value) -> serde_json::Value {
    std::fs::create_dir_all(dir).unwrap();
    let mut child = Command::new("node")
        .arg(guard())
        .args(["--claude-hook", phase])
        .env("AGENTHUB_SEAT", "dev")
        .env("AGENTHUB_SESSION_NAME", "agenthub--room--dev")
        .env_remove("OPENRIG_SESSION_NAME")
        .env_remove("AGENTHUB_LANES_ROOT")
        .env("AGENTHUB_WORKTREES", WORKTREES)
        .env("PI_CODING_AGENT_DIR", dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("node is needed to run the seat guard");
    child
        .stdin
        .take()
        .unwrap()
        .write_all(payload.to_string().as_bytes())
        .unwrap();
    serde_json::from_slice(&child.wait_with_output().unwrap().stdout)
        .expect("the guard answers JSON")
}

/// The rule a claude PreToolUse call is refused by, or `None` when claude is told `allow`.
fn claude_rule(tool: &str, input: serde_json::Value) -> Option<String> {
    let answer = claude_hook(
        &fresh_dir("claude"),
        "pre",
        serde_json::json!({"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input}),
    );
    let out = &answer["hookSpecificOutput"];
    assert_eq!(out["hookEventName"], "PreToolUse");
    match out["permissionDecision"].as_str() {
        Some("allow") => None,
        Some("deny") => {
            let reason = out["permissionDecisionReason"].as_str().unwrap();
            Some(rule_of(reason))
        }
        other => panic!("claude reads allow or deny, not {other:?}"),
    }
}

/// claude's tool names reach the rule the same call reaches through the agy path, which maps to the omp names the
/// rules are written in: one table, so a name that stops mapping fails here and not in a seat.
#[test]
fn a_claude_call_reaches_the_rule_its_omp_name_reaches() {
    let outside = "/elsewhere/x.txt";
    let cases: &[(&str, serde_json::Value, &str, serde_json::Value)] = &[
        (
            "Bash",
            serde_json::json!({"command": "cat .env"}),
            "run_command",
            serde_json::json!({"CommandLine": "cat .env"}),
        ),
        (
            "Bash",
            serde_json::json!({"command": "git push origin main"}),
            "run_command",
            serde_json::json!({"CommandLine": "git push origin main"}),
        ),
        (
            "Bash",
            serde_json::json!({"command": "printenv"}),
            "run_command",
            serde_json::json!({"CommandLine": "printenv"}),
        ),
        (
            "Bash",
            serde_json::json!({"command": "env -u TMUX tmux ls"}),
            "run_command",
            serde_json::json!({"CommandLine": "env -u TMUX tmux ls"}),
        ),
        (
            "Read",
            serde_json::json!({"file_path": "/work/wt/dev/.env"}),
            "view_file",
            serde_json::json!({"AbsolutePath": "/work/wt/dev/.env"}),
        ),
        (
            "Read",
            serde_json::json!({"file_path": "/repo/README.md"}),
            "view_file",
            serde_json::json!({"AbsolutePath": "/repo/README.md"}),
        ),
        (
            "Write",
            serde_json::json!({"file_path": outside}),
            "write_to_file",
            serde_json::json!({"TargetFile": outside}),
        ),
        (
            "Edit",
            serde_json::json!({"file_path": outside}),
            "write_to_file",
            serde_json::json!({"TargetFile": outside}),
        ),
        (
            "MultiEdit",
            serde_json::json!({"file_path": outside}),
            "write_to_file",
            serde_json::json!({"TargetFile": outside}),
        ),
        (
            "Write",
            serde_json::json!({"file_path": "/work/wt/dev/src/a.rs"}),
            "write_to_file",
            serde_json::json!({"TargetFile": "/work/wt/dev/src/a.rs"}),
        ),
    ];
    for (tool, input, agy, args) in cases {
        let expected = verdict(agy, args.clone());
        assert_eq!(claude_rule(tool, input.clone()), expected, "{tool} {input}");
    }
    // The rule-bearing cases are refused, so the table cannot pass on a guard that allows everything.
    assert_eq!(
        claude_rule(
            "Bash",
            serde_json::json!({"command": "git push origin main"})
        )
        .as_deref(),
        Some("push")
    );
    assert!(claude_rule("Write", serde_json::json!({"file_path": outside})).is_some());
    assert_eq!(
        claude_rule(
            "Bash",
            serde_json::json!({"command": "env -u TMUX tmux ls"})
        ),
        None
    );
    assert_eq!(
        claude_rule("Read", serde_json::json!({"file_path": "/repo/README.md"})),
        None
    );
    for tool in [
        "Grep",
        "Glob",
        "Task",
        "WebFetch",
        "WebSearch",
        "BashOutput",
    ] {
        assert_eq!(
            claude_rule(
                tool,
                serde_json::json!({"pattern": "x", "path": "/work/wt/dev"})
            ),
            None,
            "{tool}"
        );
    }
}

/// PostToolUse answers `{}` and logs the call's outcome once; a refusal is logged by the pre phase as `blocked`.
#[test]
fn a_claude_post_call_answers_empty_and_logs_one_line() {
    let dir = fresh_dir("claude-post");
    let answer = claude_hook(
        &dir,
        "post",
        serde_json::json!({"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_input": {"command": "git status"}}),
    );
    assert_eq!(answer, serde_json::json!({}));
    let log = std::fs::read_to_string(dir.join("tool-calls.jsonl")).unwrap();
    let lines: Vec<serde_json::Value> = log
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    assert_eq!(lines.len(), 1, "{log}");
    assert_eq!(lines[0]["outcome"], "ok");
    assert_eq!(lines[0]["tool"], "bash");

    let dir = fresh_dir("claude-deny");
    claude_hook(
        &dir,
        "pre",
        serde_json::json!({"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "cat .env"}}),
    );
    let log = std::fs::read_to_string(dir.join("tool-calls.jsonl")).unwrap();
    assert!(log.contains("\"outcome\":\"blocked\""), "{log}");
}
