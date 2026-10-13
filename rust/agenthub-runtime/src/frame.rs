//! The runner↔daemon relay frame, and the spill frame (parity 2).
//!
//! Contract: `SPEC-runner-relay-protocol-2026-10-13.md` §1–§2, ratified by the architect. The frozen
//! decisions this module implements, and that must not be re-litigated:
//!
//! - the channel is the per-seat mailbox socket, one JSON object per line, envelope `{"id","type",…}`;
//! - `MAX_FRAME_BYTES` is a limit on the **encoded line**, because JSON escaping inflates a payload up to
//!   6×, so a sender measures `encoded.len()` and never the payload's;
//! - a payload whose encoded line reaches the limit is **spilled**: written under the room, with `bytes`
//!   and `sha256` authoritative, plus `head`/`tail` for a log line and a glance — never the payload;
//! - a reader that buffers a line past the limit **aborts with `framing_error`** rather than growing;
//! - a spilled payload is **never inlined back** into the agent's prompt; ranges are the ceiling.
//!
//! What this module deliberately does not do, because the spec says the direction cannot be fixed here:
//! omp enforces its own cap on the omp↔runner JSON-RPC channel, so the runner cannot spill bytes omp
//! refuses to emit. The spill applies where we own the bytes: daemon→agent and runner→daemon.

use std::fs;
use std::io::{BufRead, Write};
use std::path::{Path, PathBuf};

use serde_json::{json, Value};
use sha2::{Digest, Sha256};

/// The limit on the encoded line. 128 KiB: below the 212 992-byte AF_UNIX default send/receive buffer, so
/// one whole frame is absorbed by a single kernel write; 1/8 of the only peer cap we can measure
/// (the 1 MiB pane-input refusal), which leaves 8× headroom under a worst-case 6× escape.
pub const MAX_FRAME_BYTES: usize = 131_072;

/// Bytes of the payload that ride the frame as a glance-able hint. Not the payload.
pub const HEAD_TAIL_BYTES: usize = 8_192;

/// What a room's spill tree may hold before the oldest payloads are evicted.
pub const SPILL_BUDGET_BYTES: u64 = 256 * 1024 * 1024;

/// The label a consumer must record when a stream or a spill does not check out.
pub const TRUNCATED_STREAM: &str = "truncated_stream";

/// The label a reader records when a peer writes a line it refuses to buffer.
pub const FRAMING_ERROR: &str = "framing_error";

/// Where a spilled payload lives. The two locations are the whole rule: the payload the **daemon** reads
/// sits at the room's own spill dir, and a payload **addressed to the agent** sits under the seat's
/// directory, because that directory is the seat's `cwd` and `PI_CODING_AGENT_DIR` — the agent's own file
/// tools must be able to open it by path, and a spill under a room the seat cannot read is a mechanism that
/// cannot work.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SpillTarget {
    Room { room: String },
    Seat { room: String, seat: String },
}

impl SpillTarget {
    pub fn dir(&self, state_dir: &Path) -> PathBuf {
        match self {
            SpillTarget::Room { room } => state_dir.join("rooms").join(room).join("spill"),
            SpillTarget::Seat { room, seat } => state_dir
                .join("rooms")
                .join(room)
                .join("seats")
                .join(seat)
                .join("spill"),
        }
    }
}

/// What sending one frame produced: the small frame itself, or a spill plus the frame that carries its
/// handle.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Sent {
    Data,
    Spilled { path: PathBuf, bytes: u64, sha256: String },
}

/// One frame, encoded as the line that goes on the wire. Always exactly one line: JSON escapes the tabs and
/// newlines inside a string, which is why the line length is the only length that matters.
pub fn encode_line(frame: &Value) -> Result<String, String> {
    let mut line = serde_json::to_string(frame).map_err(|e| format!("cannot encode frame: {e}"))?;
    line.push('\n');
    Ok(line)
}

/// The name of one spilled payload: fixed-width, so "oldest first" is a lexicographic sort and no mtime is
/// trusted. `<millis since epoch>-<seq>-<pid>`, with the tail keeping two spills in the same millisecond
/// distinct.
pub fn spill_name(seq: u64) -> String {
    let millis = chrono::Utc::now().timestamp_millis();
    format!("{millis:013}-{seq:06}-{}.bin", std::process::id())
}

/// The hex of a digest, spelled once.
fn hex(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut out = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        out.push(HEX[(b >> 4) as usize] as char);
        out.push(HEX[(b & 0x0f) as usize] as char);
    }
    out
}

pub fn sha256_hex(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    hex(&hasher.finalize())
}

/// The head and tail a spill frame carries: at most `HEAD_TAIL_BYTES` each, clamped to a char boundary so
/// the frame stays valid JSON. A binary payload is shown lossily and labelled `binary`; the file is what a
/// consumer trusts.
fn head_and_tail(bytes: &[u8], utf8: bool) -> (String, String) {
    let clamp = |slice: &[u8]| -> String {
        let limit = slice.len().min(HEAD_TAIL_BYTES);
        if utf8 {
            let end = match std::str::from_utf8(&slice[..limit]) {
                Ok(_) => limit,
                Err(e) => e.valid_up_to(),
            };
            String::from_utf8_lossy(&slice[..end]).into_owned()
        } else {
            String::from_utf8_lossy(&slice[..limit]).into_owned()
        }
    };
    let head = clamp(&bytes[..bytes.len().min(HEAD_TAIL_BYTES)]);
    let tail_start = bytes.len().saturating_sub(HEAD_TAIL_BYTES);
    let tail = clamp(&bytes[tail_start..]);
    (head, tail)
}

/// The next name in this process, so two spills in the same millisecond cannot collide.
pub fn next_spill_name() -> String {
    use std::sync::atomic::{AtomicU64, Ordering};
    static SEQ: AtomicU64 = AtomicU64::new(0);
    spill_name(SEQ.fetch_add(1, Ordering::Relaxed))
}

/// Write one spilled payload and return the handle the frame carries.
///
/// The handle is the contract: `bytes` and `sha256` are authoritative, the path is where the reader finds
/// it, and `head`/`tail` are for a log line and a human glance.
pub fn write_spill(dir: &Path, name: &str, bytes: &[u8], utf8: bool) -> Result<Value, String> {
    fs::create_dir_all(dir).map_err(|e| format!("cannot create spill dir {}: {e}", dir.display()))?;
    let path = dir.join(name);
    fs::write(&path, bytes).map_err(|e| format!("cannot write spill {}: {e}", path.display()))?;
    let (head, tail) = head_and_tail(bytes, utf8);
    Ok(json!({
        "bytes": bytes.len() as u64,
        "sha256": sha256_hex(bytes),
        "path": path.to_string_lossy(),
        "head": head,
        "tail": tail,
        "encoding": if utf8 { "utf-8" } else { "binary" },
    }))
}

/// The payload frame, from an already-written handle.
pub fn payload_frame(id: &str, handle: &Value) -> Value {
    let mut frame = json!({"id": id, "type": "payload"});
    frame["handle"] = handle.clone();
    frame
}

/// Encode one frame, spilling the payload when the encoded line reaches `MAX_FRAME_BYTES`.
///
/// The rule is `>=`: no frame this module writes may reach the limit, so the limit is where a frame becomes
/// a handle. The caller names the spill (`spill_name`), because the caller owns the sequence.
pub fn frame_or_spill(
    frame: &Value,
    payload: &[u8],
    utf8: bool,
    spill_dir: &Path,
    spill_name: &str,
) -> Result<(String, Sent), String> {
    let id = frame
        .get("id")
        .and_then(|v| v.as_str())
        .unwrap_or("runner-frame")
        .to_string();
    let line = encode_line(frame)?;
    if line.len() < MAX_FRAME_BYTES {
        return Ok((line, Sent::Data));
    }
    let handle = write_spill(spill_dir, spill_name, payload, utf8)?;
    let spilled = payload_frame(&id, &handle);
    let line = encode_line(&spilled)?;
    Ok((
        line,
        Sent::Spilled {
            path: spill_dir.join(spill_name),
            bytes: payload.len() as u64,
            sha256: handle
                .get("sha256")
                .and_then(|v| v.as_str())
                .unwrap_or_default()
                .to_string(),
        },
    ))
}

/// Read one frame, refusing to buffer a line past the limit.
///
/// Only whole lines are frames. A reader that meets an incomplete line buffers it until `\n`; if that
/// buffer reaches the limit the connection is aborted with `framing_error`, so a hostile or buggy peer
/// cannot make this process allocate without bound.
pub fn read_line_bounded<R: BufRead>(reader: &mut R, max: usize) -> Result<String, String> {
    let mut buf: Vec<u8> = Vec::new();
    loop {
        let available = reader
            .fill_buf()
            .map_err(|e| format!("mailbox read error: {e}"))?;
        if available.is_empty() {
            break; // the peer closed: whatever is buffered is an unterminated partial, discarded
        }
        match available.iter().position(|b| *b == b'\n') {
            Some(end) => {
                buf.extend_from_slice(&available[..=end]);
                reader.consume(end + 1);
                if buf.len() > max {
                    return Err(format!(
                        "{FRAMING_ERROR}: a {}-byte line exceeds the {max}-byte limit",
                        buf.len()
                    ));
                }
                return String::from_utf8(buf)
                    .map_err(|_| format!("{FRAMING_ERROR}: the line is not UTF-8"));
            }
            None => {
                let taken = available.len();
                buf.extend_from_slice(available);
                reader.consume(taken);
                if buf.len() >= max {
                    return Err(format!(
                        "{FRAMING_ERROR}: the peer wrote {} bytes with no line end; refusing to buffer further",
                        buf.len()
                    ));
                }
            }
        }
    }
    if buf.is_empty() {
        return Ok(String::new());
    }
    Err(format!(
        "{FRAMING_ERROR}: the connection ended {} bytes into a line",
        buf.len()
    ))
}

/// Read a spilled payload back and verify it before anyone trusts it.
///
/// A `sha256` mismatch is the same protocol failure as a stream that ended without `eof`: the payload is
/// discarded, never half-applied.
pub fn read_spill(handle: &Value) -> Result<Vec<u8>, String> {
    let path = handle
        .get("path")
        .and_then(|v| v.as_str())
        .ok_or_else(|| "a spill handle without a path".to_string())?;
    let bytes = fs::read(path).map_err(|e| format!("cannot read spill {path}: {e}"))?;
    let want = handle
        .get("sha256")
        .and_then(|v| v.as_str())
        .unwrap_or_default();
    let got = sha256_hex(&bytes);
    if !want.is_empty() && want != got {
        return Err(format!(
            "{TRUNCATED_STREAM}: spill {path} does not match its handle (wanted {want}, got {got})"
        ));
    }
    if let Some(bytes_wanted) = handle.get("bytes").and_then(|v| v.as_u64()) {
        if bytes_wanted != bytes.len() as u64 {
            return Err(format!(
                "{TRUNCATED_STREAM}: spill {path} is {} bytes, the handle says {bytes_wanted}",
                bytes.len()
            ));
        }
    }
    Ok(bytes)
}

/// Keep the newest `budget` bytes of a spill tree, oldest first by name.
///
/// Names are fixed-width and monotonic (`spill_name`), so the sort is a lexicographic one and no mtime is
/// trusted. A payload with no referencing frame at GC time is deleted, which is what "superseded" means in
/// practice. Returns the names removed.
pub fn gc_spills(dir: &Path, budget: u64) -> Result<Vec<String>, String> {
    let entries = match fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return Ok(Vec::new()),
    };
    let mut files: Vec<(String, u64)> = Vec::new();
    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("bin") {
            continue;
        }
        let size = entry.metadata().map(|m| m.len()).unwrap_or(0);
        files.push((
            path.file_name().unwrap_or_default().to_string_lossy().into_owned(),
            size,
        ));
    }
    files.sort();
    let mut total: u64 = files.iter().map(|(_, size)| *size).sum();
    let mut removed = Vec::new();
    for (name, size) in &files {
        if total <= budget {
            break;
        }
        if fs::remove_file(dir.join(name)).is_ok() {
            removed.push(name.clone());
            total = total.saturating_sub(*size);
        }
    }
    Ok(removed)
}

/// Delete a room's whole spill tree. Called when the room is stopped: a spill outlives no room.
pub fn remove_spill_trees(room_dir: &Path) -> Result<(), String> {
    let room_spill = room_dir.join("spill");
    if room_spill.is_dir() {
        fs::remove_dir_all(&room_spill)
            .map_err(|e| format!("cannot remove {}: {e}", room_spill.display()))?;
    }
    let seats = room_dir.join("seats");
    if let Ok(entries) = fs::read_dir(&seats) {
        for entry in entries.flatten() {
            let spill = entry.path().join("spill");
            if spill.is_dir() {
                let _ = fs::remove_dir_all(&spill);
            }
        }
    }
    Ok(())
}

/// Answer a request on the socket the *caller* created, exactly once, then close.
///
/// This is the "no silent success" rule: a local write succeeded is not the same fact as the frame was
/// relayed, and only the peer can say which. No `replyTo` means fire-and-forget, which stays the behaviour
/// of the existing `compact` callers.
pub fn reply(reply_to: &str, id: &str, result: Result<Value, String>) -> Result<(), String> {
    let mut stream = std::os::unix::net::UnixStream::connect(reply_to).map_err(|e| {
        format!("cannot reach the reply socket {reply_to}: {e}")
    })?;
    let line = match result {
        Ok(data) => encode_line(&json!({"id": id, "ok": true, "data": data}))?,
        Err(reason) => encode_line(&json!({"id": id, "ok": false, "error": reason}))?,
    };
    stream
        .write_all(line.as_bytes())
        .map_err(|e| format!("cannot answer on {reply_to}: {e}"))?;
    stream.flush().map_err(|e| format!("cannot flush {reply_to}: {e}"))
}

/// Send one frame and, when a spill was produced, say so in the same voice as every other runner line.
pub fn write_sent<W: Write>(writer: &mut W, line: &str, sent: &Sent) -> Result<(), String> {
    writer
        .write_all(line.as_bytes())
        .and_then(|_| writer.flush())
        .map_err(|e| format!("cannot write a frame: {e}"))?;
    if let Sent::Spilled {
        path,
        bytes,
        sha256,
    } = sent
    {
        eprintln!(
            "[omp-runner] spilled {bytes} bytes to {} (sha256 {})",
            path.display(),
            &sha256[..sha256.len().min(16)]
        );
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A payload whose *encoded line* is exactly `target` bytes, so the threshold is tested on the number
    /// that matters. The line is `{"id":"x","type":"prompt","message":"<pad>"}` + newline, and the pad is
    /// plain ASCII, so payload length and encoded length move together (1:1, 0 escapes).
    fn frame_with_line_len(target: usize) -> (Value, Vec<u8>, usize) {
        let skeleton = r#"{"id":"x","type":"prompt","message":""}"#.len() + 1; // + the newline
        let pad = target.saturating_sub(skeleton);
        let message = "a".repeat(pad);
        let frame = json!({"id": "x", "type": "prompt", "message": message});
        let line = encode_line(&frame).unwrap();
        let payload = line.clone().into_bytes();
        (frame, payload, line.len())
    }

    #[test]
    fn threshold_is_on_the_encoded_line_and_n_minus_1_is_a_data_frame() {
        let dir = std::env::temp_dir().join(format!("frame-test-{}-a", std::process::id()));
        for (target, spills) in [
            (MAX_FRAME_BYTES - 1, false),
            (MAX_FRAME_BYTES, true),
            (MAX_FRAME_BYTES + 1, true),
        ] {
            let (frame, payload, encoded) = frame_with_line_len(target);
            assert_eq!(encoded, target, "the fixture must hit the target exactly");
            let (line, sent) =
                frame_or_spill(&frame, &payload, true, &dir, "x.bin").expect("frame_or_spill");
            match sent {
                Sent::Data => {
                    assert!(!spills, "a {encoded}-byte line must spill");
                    assert!(line.len() < MAX_FRAME_BYTES);
                }
                Sent::Spilled { .. } => {
                    assert!(spills, "a {encoded}-byte line must stay a data frame");
                    let spilled: Value = serde_json::from_str(line.trim()).unwrap();
                    assert_eq!(spilled["type"], "payload");
                    assert_eq!(spilled["handle"]["bytes"], payload.len());
                }
            }
        }
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_spill_frame_names_a_file_whose_sha256_verifies_and_whose_glance_is_exactly_8k() {
        let dir = std::env::temp_dir().join(format!("frame-test-{}-b", std::process::id()));
        let (frame, payload, _) = frame_with_line_len(MAX_FRAME_BYTES + 4096);
        let (line, sent) = frame_or_spill(&frame, &payload, true, &dir, "verify.bin").unwrap();
        let Sent::Spilled { path, bytes, .. } = sent else {
            panic!("a payload above the limit must spill");
        };
        assert_eq!(bytes, payload.len() as u64);
        let spilled: Value = serde_json::from_str(line.trim()).unwrap();
        let handle = &spilled["handle"];
        assert_eq!(handle["encoding"], "utf-8");
        assert_eq!(
            handle["head"].as_str().unwrap().len(),
            HEAD_TAIL_BYTES,
            "head is exactly the glance size"
        );
        assert_eq!(
            handle["tail"].as_str().unwrap().len(),
            HEAD_TAIL_BYTES,
            "tail is exactly the glance size"
        );
        assert_eq!(
            handle["sha256"].as_str().unwrap(),
            sha256_hex(&payload),
            "the handle's digest is the payload's"
        );
        let read_back = read_spill(handle).expect("a written spill verifies");
        assert_eq!(read_back, payload, "the file is the payload, byte for byte");
        assert!(path.exists());

        // A corrupted file is the same protocol failure as a truncated stream: discarded, not half-used.
        fs::write(&path, b"not the payload").unwrap();
        let err = read_spill(handle).unwrap_err();
        assert!(err.starts_with(TRUNCATED_STREAM), "got {err}");
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn an_overlong_line_aborts_the_connection_with_framing_error() {
        let mut no_newline = std::io::BufReader::new(std::io::Cursor::new(vec![
            b'x';
            MAX_FRAME_BYTES + 8
        ]));
        let err = read_line_bounded(&mut no_newline, MAX_FRAME_BYTES).unwrap_err();
        assert!(err.contains(FRAMING_ERROR), "got {err}");

        // A whole line under the limit is still read back whole.
        let mut ok = std::io::BufReader::new(std::io::Cursor::new(b"{\"id\":\"a\"}\n".to_vec()));
        assert_eq!(read_line_bounded(&mut ok, MAX_FRAME_BYTES).unwrap().trim(), "{\"id\":\"a\"}");
    }

    #[test]
    fn gc_keeps_the_newest_within_budget_and_removes_the_oldest_first() {
        let dir = std::env::temp_dir().join(format!("frame-test-{}-gc", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        for name in ["0000000000001-000000-x.bin", "0000000000002-000000-x.bin", "0000000000003-000000-x.bin"] {
            fs::write(dir.join(name), vec![b'z'; 100]).unwrap();
        }
        let removed = gc_spills(&dir, 150).unwrap();
        assert_eq!(removed.len(), 2, "the two oldest go, the newest stays: {removed:?}");
        assert!(dir.join("0000000000003-000000-x.bin").exists());
        assert!(!dir.join("0000000000001-000000-x.bin").exists());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_room_stop_removes_the_whole_spill_tree() {
        let state = std::env::temp_dir().join(format!("frame-test-{}-room", std::process::id()));
        let target = SpillTarget::Seat { room: "r".into(), seat: "s".into() };
        let seat_spill = target.dir(&state);
        write_spill(&seat_spill, "s.bin", b"seat payload", true).unwrap();
        let room_spill = SpillTarget::Room { room: "r".into() }.dir(&state);
        write_spill(&room_spill, "r.bin", b"room payload", true).unwrap();
        remove_spill_trees(&state.join("rooms").join("r")).unwrap();
        assert!(!seat_spill.exists(), "the seat's spill tree is gone with the room");
        assert!(!room_spill.exists(), "the room's spill tree is gone with the room");
        let _ = fs::remove_dir_all(&state);
    }

    #[test]
    fn a_reply_carries_ok_or_a_real_refusal() {
        use std::io::Read;
        use std::os::unix::net::UnixListener;
        let path = std::env::temp_dir().join(format!("frame-test-{}-reply.sock", std::process::id()));
        let _ = fs::remove_file(&path);
        let listener = UnixListener::bind(&path).unwrap();
        let handle = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut text = String::new();
            stream.read_to_string(&mut text).unwrap();
            text
        });
        reply(&path.to_string_lossy(), "abc", Err("unsupported_type".to_string())).unwrap();
        let answered = handle.join().unwrap();
        let frame: Value = serde_json::from_str(answered.trim()).unwrap();
        assert_eq!(frame["id"], "abc");
        assert_eq!(frame["ok"], false);
        assert_eq!(frame["error"], "unsupported_type");
        let _ = fs::remove_file(&path);
    }
}
