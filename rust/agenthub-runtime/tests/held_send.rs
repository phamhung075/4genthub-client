//! THE PROBE FOR HUB TASK `c94cecfc`, and it runs the REAL BINARY: a text sent into a seat's mailbox while
//! that seat holds a turn must be DELIVERED when the turn ends - not dropped, and not dropped in silence.
//!
//! WHY IT SPAWNS THE RUNTIME AND A FAKE AGENT. The unit cases in `runner::tests` drive `deliver_held` directly,
//! so they stay green if the mailbox stops recording the origin, if the Hold arm stops building an entry, or if
//! nothing calls `deliver_held` at a turn end at all. Run against the revision this branch starts from, this
//! case fails on exactly those two: the refusal held the text while the state file went on reading `idle` (a
//! sender told nothing - the shape of the 19 give-up lines counted at 23:14Z in the window that began 22:00Z on
//! 2026-10-16, 13 of them addressed to the lead seat; the window is open-ended, so a 23:41Z recount reads 33),
//! and the delivery line named the refused prompt's id instead of the session that sent it.
//! What the text's own BOUND does - the resume ladder's counter that dropped a text on its first offer once
//! three unrelated attempts had been spent on that seat - is the unit cases' subject, because pinning it needs
//! a counter at its bound rather than a fresh one. This case pins the whole path instead: a real mailbox
//! socket, a real runner, a real child that holds its turn, and the text's own arrival at that child after.
//!
//! The fake agent is the seat's runtime name on PATH (`omp`), which is how the runner resolves its child
//! (`child_program` returns the seat's runtime for anything but claude-code). It speaks the frames the runner
//! classifies: `ready`, `turn_start`, a refusal of a prompt while its turn runs, and `turn_end`.
#![cfg(target_os = "linux")]

use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::fs::PermissionsExt;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

/// The scratch tree this case owns. Nothing outside it is read or written: the state dir, the room, the
/// shim on PATH and the fake agent's record all live under one temp directory.
fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("agenthub-held-{tag}-{}", std::process::id()));
    let _ = fs::remove_dir_all(&dir);
    fs::create_dir_all(&dir).expect("scratch root");
    dir
}

/// The fake seat's agent: it opens a turn on the first prompt, refuses what arrives while that turn runs, and
/// writes every prompt it RECEIVES to `agent-seen.jsonl`, which is the file this case asserts on.
///
/// THE TURN IS ANNOUNCED, because `turn_start` is the fact the runner holds a text on: without it the runner
/// has nothing to know the seat is busy by, and a probe that skipped it would pass on a runner that drops
/// every text. It ends when the probe says (`FAKE_AGENT_END_MARKER`), so the case never races a timer.
const FAKE_AGENT: &str = r#"#!/usr/bin/env python3
import json, os, sys, threading, time

seen = os.environ["FAKE_AGENT_SEEN"]
marker = os.environ["FAKE_AGENT_END_MARKER"]
state = {"holding": False, "opened": False}

def say(frame):
    print(json.dumps(frame), flush=True)

def end_turn_when_told():
    # The probe ends the turn, once the text it wants held is in flight. The deadline is only here so a broken
    # probe cannot leave a runner waiting on a turn that never ends.
    deadline = time.time() + 30
    while time.time() < deadline and not os.path.exists(marker):
        time.sleep(0.05)
    state["holding"] = False
    say({"type": "turn_end"})

say({"type": "ready"})
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        frame = json.loads(line)
    except Exception:
        continue
    kind = frame.get("type")
    if kind == "prompt":
        with open(seen, "a") as fh:
            fh.write(json.dumps({"id": frame.get("id"), "message": frame.get("message")}) + "\n")
        if state["holding"]:
            say({"type": "response", "id": frame.get("id"), "success": False,
                 "error": "Agent is already processing. Use steer() or followUp() or wait for the current turn to finish."})
        else:
            say({"type": "response", "id": frame.get("id"), "success": True})
            if not state["opened"]:
                # The prompt opened the seat's own turn: from here the seat is WORKING, in the frames' own
                # vocabulary, until the probe ends it.
                state["opened"] = True
                state["holding"] = True
                say({"type": "turn_start"})
                threading.Thread(target=end_turn_when_told, daemon=True).start()
    elif kind == "get_state":
        say({"type": "response", "id": frame.get("id"), "success": True, "data": {}})
    elif kind == "abort":
        say({"type": "response", "id": frame.get("id"), "success": True})
        say({"type": "turn_end"})
"#;

/// The room the binary is run with: one seat, `dev`, on the shimmed `omp`, in a directory of its own.
fn write_room(state: &Path, seat_dir: &Path) -> PathBuf {
    let room_dir = state.join("rooms").join("demo");
    fs::create_dir_all(room_dir.join("run")).expect("run dir");
    let spec = serde_json::json!({
        "room": "demo",
        "name": "demo",
        "cwd": seat_dir,
        "seats": [{
            "seat_key": "dev",
            "seat_type": "developer",
            "runtime": "omp",
            "model": null,
            "permission_policy": null,
            "cwd": seat_dir,
            "agent_dir": null,
            "policy_document": null,
            "env": {},
        }],
        "links": [],
        "overlays": {},
    });
    let spec_file = room_dir.join("spec.json");
    fs::write(
        &spec_file,
        serde_json::to_string_pretty(&spec).expect("spec"),
    )
    .expect("spec file");
    room_dir
}

/// Wait for a file to exist, or give up. Polling a socket the runner binds is the only readiness signal a
/// caller has (the same one `spawn_mailbox` leaves behind for the daemon).
fn wait_for(path: &Path, what: &str) -> bool {
    let deadline = Instant::now() + Duration::from_secs(20);
    while Instant::now() < deadline {
        if path.exists() {
            return true;
        }
        std::thread::sleep(Duration::from_millis(25));
    }
    eprintln!("timed out waiting for {what} at {}", path.display());
    false
}

/// Wait for a line to appear in a file, or give up. Used for the seat's own log, which the delivery writes.
fn wait_for_line(path: &Path, needle: &str, seconds: u64) -> Option<String> {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    while Instant::now() < deadline {
        if let Ok(text) = fs::read_to_string(path) {
            if let Some(line) = text.lines().find(|line| line.contains(needle)) {
                return Some(line.to_string());
            }
        }
        std::thread::sleep(Duration::from_millis(25));
    }
    None
}

/// Wait until the runner's own state file reads `want` AND its classification contains `needle`, or give up.
/// That file is the surface an operator reads (`4genteam ps`, the daemon's `status`), so waiting on it proves
/// what the RUNNER published, which is a different fact from what the agent received.
fn wait_for_state(
    path: &Path,
    want: &str,
    needle: &str,
    seconds: u64,
) -> Option<serde_json::Value> {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    while Instant::now() < deadline {
        if let Ok(text) = fs::read_to_string(path) {
            if let Ok(value) = serde_json::from_str::<serde_json::Value>(&text) {
                let classification = value["classification"].as_str().unwrap_or_default();
                if value["state"] == want && classification.contains(needle) {
                    return Some(value);
                }
            }
        }
        std::thread::sleep(Duration::from_millis(25));
    }
    None
}

/// Write one `text` frame into a seat's mailbox and read the runner's answer, exactly as `send_to_seat` does:
/// the frame carries a `replyTo` socket the runner answers on, because "a local write succeeded" is not the
/// fact "the frame was relayed".
fn send_text_frame(mailbox: &Path, id: &str, from: &str, text: &str) -> serde_json::Value {
    let reply_dir =
        std::env::temp_dir().join(format!("agenthub-held-reply-{}", std::process::id()));
    let _ = fs::create_dir_all(&reply_dir);
    let reply_path = reply_dir.join(format!("{id}.sock"));
    let _ = fs::remove_file(&reply_path);
    let listener = UnixListener::bind(&reply_path).expect("reply socket");

    let mut stream = UnixStream::connect(mailbox).expect("the runner's mailbox");
    let frame = serde_json::json!({
        "id": id,
        "type": "text",
        "text": text,
        "from": from,
        "verb": "send",
        "replyTo": reply_path.to_string_lossy(),
    });
    stream
        .write_all(format!("{frame}\n").as_bytes())
        .expect("write the frame");
    stream.flush().expect("flush");

    let (mut answer_stream, _) = listener.accept().expect("the runner's answer");
    let mut line = String::new();
    BufReader::new(&mut answer_stream)
        .read_line(&mut line)
        .expect("read the answer");
    stream.shutdown(std::net::Shutdown::Both).ok();
    let _ = fs::remove_file(&reply_path);
    serde_json::from_str(line.trim()).expect("the answer is a frame")
}

fn stop(mut child: Child) {
    let _ = child.kill();
    let _ = child.wait();
}

#[test]
fn a_text_sent_to_a_seat_that_is_mid_turn_is_delivered_when_the_turn_ends() {
    let root = scratch("mid-turn");
    let state = root.join("state");
    let seat_dir = root.join("seat");
    let bin = root.join("bin");
    fs::create_dir_all(&seat_dir).expect("seat dir");
    fs::create_dir_all(&bin).expect("shim dir");
    let seen = root.join("agent-seen.jsonl");
    let marker = root.join("end-turn");
    let shim = bin.join("omp");
    fs::write(&shim, FAKE_AGENT).expect("fake agent");
    fs::set_permissions(&shim, fs::Permissions::from_mode(0o755))
        .expect("fake agent is executable");

    write_room(&state, &seat_dir);
    let mailbox = state
        .join("rooms")
        .join("demo")
        .join("run")
        .join("dev.sock");
    let seat_log = state
        .join("rooms")
        .join("demo")
        .join("logs")
        .join("dev.jsonl");
    let state_file = state
        .join("rooms")
        .join("demo")
        .join("run")
        .join("dev.state.json");

    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let runner = Command::new(env!("CARGO_BIN_EXE_agenthub-runtime"))
        .arg("-s")
        .arg(&state)
        .args(["runner", "demo/dev"])
        .env("PATH", path)
        .env("FAKE_AGENT_SEEN", &seen)
        .env("FAKE_AGENT_END_MARKER", &marker)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("the runner starts");

    assert!(
        wait_for(&mailbox, "the runner's mailbox"),
        "the runner never bound its mailbox"
    );
    // THE HANDSHAKE IS DONE BEFORE ANYTHING IS SENT. The runner writes this file when its agent answers
    // `get_state`, and that answer's `idle` lands AFTER a `turn_start` the child printed while the handshake
    // was still running - so a prompt fired at the bare mailbox socket races the handshake and can leave the
    // turn's own `working` overwritten. That race is this case's, not the runner's: in a live rig the mailbox
    // is how a text arrives, and the mailbox is served only once the runner is up. Waiting here is what makes
    // the next assertion about a refusal instead of about an ordering the probe happened to lose.
    assert!(
        wait_for_state(&state_file, "idle", "", 20).is_some(),
        "the runner finished its handshake: {}",
        fs::read_to_string(&state_file).unwrap_or_default()
    );

    // The FIRST prompt opens the agent's turn: the agent answers it (the turn is its own work in flight) and
    // the runner publishes `working`. Nothing about this prompt is held.
    let opening = send_text_frame(
        &mailbox,
        "send-probe-1",
        "agenthub--demo--lead",
        "the first prompt, which opens the turn",
    );
    assert_eq!(
        opening["data"]["relayed"], "prompt",
        "the runner relays what it was handed: {opening}"
    );
    assert!(
        wait_for_line(&seen, "the first prompt", 10).is_some(),
        "the agent received the prompt that opened its turn"
    );
    // AND THE RUNNER SAW THAT TURN OPEN. `turn_start` is the frame a text is held on, and this file is where
    // the runner publishes the fact - waiting for it here is what makes the next assertion about a refusal
    // rather than about a race the probe happens to win.
    assert!(
        wait_for_state(&state_file, "working", "", 10).is_some(),
        "the runner published the open turn: {}",
        fs::read_to_string(&state_file).unwrap_or_default()
    );

    // THE DEFECT'S OWN SHAPE: the seat is mid-turn, and this text arrives while it is. It cannot be taken now,
    // so the runner holds it for the next free turn.
    let held_id = "send-probe-2";
    let held_text = "the text sent while the seat was busy";
    let answer = send_text_frame(&mailbox, held_id, "agenthub--demo--lead", held_text);
    assert_eq!(answer["data"]["relayed"], "prompt", "{answer}");

    // THE HOLD IS PUBLISHED WHERE A READER LOOKS, and it names whose text is waiting - the half of the fix a
    // reader sees without the agent's cooperation. Before the fix this file stayed `idle` and the text was
    // nowhere at all.
    let held = wait_for_state(
        &state_file,
        "working",
        "held: the text from agenthub--demo--lead (send) waits for the next free turn",
        10,
    )
    .unwrap_or_else(|| {
        panic!(
            "the refused text is held and published:\n--- agent-seen.jsonl\n{}\n--- seat log\n{}",
            fs::read_to_string(&seen).unwrap_or_default(),
            fs::read_to_string(&seat_log).unwrap_or_default(),
        )
    });
    assert_eq!(held["state"], "working", "{held}");

    // The turn ends on the probe's own signal, not on a timer: the text is already in flight, so what happens
    // next is the runner's delivery rather than a prompt that arrived after the seat went free.
    fs::write(&marker, b"end the turn").expect("the marker that ends the turn");

    // AND THE TEXT COMES BACK. Before the fix it was dropped on the resume ladder's shared counter, and
    // nothing arrived at the agent at all. The agent's record of the ORIGINAL relay is already there - it is
    // the prompt it refused - so the line this case waits for is the runner's own held delivery
    // (`runner-held-<pid>-<n>`), which is the only thing that proves the text returned after the turn ended.
    let delivered = wait_for_line(&seen, "runner-held-", 15);
    assert!(
        delivered.is_some(),
        "the held text never reached the agent:\n--- agent-seen.jsonl\n{}\n--- seat log\n{}",
        fs::read_to_string(&seen).unwrap_or_default(),
        fs::read_to_string(&seat_log).unwrap_or_default(),
    );
    let received: serde_json::Value =
        serde_json::from_str(&delivered.expect("a line")).expect("the agent's record is JSON");
    assert_eq!(
        received["message"], held_text,
        "the text that comes back is the one that was held: {received}"
    );
    assert!(
        received["id"]
            .as_str()
            .unwrap_or_default()
            .starts_with("runner-held-"),
        "the delivery is the runner's held prompt, not a second copy of the original: {received}"
    );

    // ... AND THE PATH SAYS WHO IT WAS FOR. The delivery line names the sender's session and the verb that
    // carried the text, which is what a reader needs when the hand-back is what happened instead.
    let mirror =
        wait_for_line(&seat_log, "delivering the text", 10).expect("the delivery is published");
    assert!(
        mirror.contains("agenthub--demo--lead (send)"),
        "the delivery names the text's origin: {mirror}"
    );
    let published: serde_json::Value =
        serde_json::from_str(&fs::read_to_string(&state_file).expect("the runner's state"))
            .expect("state");
    // A delivered text leaves a seat that is healthy: `working` while the delivery's own prompt is answered,
    // `idle` once the agent took it - never `blocked`, which is the runner saying it could not keep the text.
    assert!(
        matches!(
            published["state"].as_str().unwrap_or_default(),
            "idle" | "working"
        ),
        "a delivered text leaves a healthy seat, not a blocked one: {published}"
    );

    stop(runner);
    let _ = fs::remove_dir_all(&root);
}
