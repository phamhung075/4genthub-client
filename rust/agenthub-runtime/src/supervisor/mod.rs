use crate::hooks::{HookBus, SeatEvent};
use crate::pty::TerminalManager;
use crate::queue::{ListFilter, QueueStore};
use crate::types::*;
use chrono::{DateTime, Utc};
use parking_lot::Mutex;
use std::collections::HashMap;
use std::fs;
use std::io::{Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;
use tracing::{error, info, warn};

/// How long a seat is given to prove it is actually up before `start_seat` refuses to call it running. The
/// signal is the seat's own state file (`runner::state_file_path`), which the runner publishes only after the
/// agent answered the transport handshake.
///
/// 60 s, not 20: the window has to cover the FIRST-EVER launch of an omp seat in a fresh cache. Measured by the
/// reviewer on a cold start (00:27Z, off-peak): the seat was still coming up at 20.18 s, refused, and handshook
/// in 8.90 s on the retry (6.44 s in a fresh control tree) because that first launch builds the agent's model
/// cache (a 7.5 MB `models.db`) - so `up` on a new box could refuse a seat that was merely slow to publish.
/// Widening this alone would be wrong: it would leave a DEAD seat unreported for longer, which is why the loop
/// below reports a fresh death record the moment the runner writes one, and why the pane check stays where it is.
/// A seat that dies takes its tmux session with it, so either way the loop normally exits in well under a second.
/// Named, because it is the window U1's exit-code contract has to be built on: 0 only when every seat is
/// running, and a seat that never proved itself is not.
const SEAT_READY_WINDOW_SECS: u64 = 60;

/// What the runtimes without a runner get: their signal is only that the session survived, so the window is
/// a grace period, not a handshake. Short, because it proves nothing about the agent inside the pane.
const NON_OMP_READY_GRACE_MS: u64 = 1500;

/// How many launches one start is allowed: the first, and the ONE retry. A third is a loop, which is the
/// shape the reseat report (feedback 3ac8b369) was filed against; the report asked for a retry, not a ladder.
const START_ATTEMPTS: u8 = 2;

/// Why one start attempt failed, split by whether anything was launched.
///
/// The split decides the retry, so it is a type and not a string a later reader has to pattern-match: a
/// `Preparation` refusal (a parked seat, a spec that cannot be read, a directory that cannot be written)
/// answers the caller on the first attempt, because a second one would repeat it verbatim; a `Launch`
/// failure is the one a retry can fix, and it carries the STAGE so the seat's log line and the final refusal
/// name the same fact.
enum StartFailure {
    /// Nothing was launched. Final on the first attempt.
    Preparation(String),
    /// A pane was launched and died, or never reported readiness, at the stage named.
    Launch { stage: &'static str, reason: String },
}

/// The preparation sites above keep returning strings; `?` turns one into a `Preparation` refusal without
/// every call site being rewritten to name the variant.
impl From<String> for StartFailure {
    fn from(reason: String) -> Self {
        StartFailure::Preparation(reason)
    }
}

/// What `Capture` takes when the caller names no line count: the same 200 the client's own agent uses for a
/// session whose transcript is empty (`clientsync.captureLines`). Named here rather than defaulted at the
/// call site so the two sides cannot disagree about how much of a pane a fallback read returns.
const DEFAULT_CAPTURE_LINES: usize = 200;

/// The states a seat's own record publishes when its agent is gone rather than answering. Shared by the
/// readiness check (which must not accept one as a handshake) and by `fresh_death_state` (which reports one
/// immediately, so a longer window cannot delay the news that a seat is dead).
const DEATH_STATES: [&str; 3] = ["agent_socket_closed", "agent_stdio_eof", "agent_exited"];

/// Whether a seat's own word is a death. ONE SPELLING, because three readers ask it - the readiness check, the
/// freshness check and the publisher check below - and a fourth death state added to one of them and forgotten in
/// the others is exactly how a dead seat keeps reading `Running`.
fn is_death_word(word: &str) -> bool {
    DEATH_STATES.contains(&word)
}

/// Whether the process that published a seat's state is still there: `Some(true)`, `Some(false)`, or `None` when
/// this host cannot answer.
///
/// `None` IS NOT `false`, and that distinction is the safety of the whole check: it is read from procfs, so a
/// platform without `/proc` answers `None` and the caller keeps the verdict it would have had - a missing
/// directory must never be read as a death.
///
/// THE LIMIT, named because it is real: pid reuse. A recycled pid makes a stale claim look live, and the cost of
/// that is the behaviour that predates this check - a stale `working` still shown as `Running`. It never goes the
/// other way, where a live seat would be called dead and its work killed.
fn process_is_alive(pid: u32) -> Option<bool> {
    if !Path::new("/proc/self").exists() {
        return None;
    }
    Some(Path::new(&format!("/proc/{pid}")).exists())
}

/// The two facts the status reads OUT of a seat's document: the word it claims, and the process that published it.
/// One reader, so a test of the correction reads the document the same way the status does.
fn published_claim(doc: Option<&serde_json::Value>) -> (Option<String>, Option<u32>) {
    let state = doc
        .and_then(|v| v.get("state").and_then(|s| s.as_str()))
        .map(str::to_string);
    let pid = doc
        .and_then(|v| v.get("pid").and_then(|p| p.as_u64()))
        .map(|p| p as u32);
    (state, pid)
}

/// THE DEATH THE RUNNER COULD NOT PUBLISH, as the sentence a status carries - and `None` when nothing on this side
/// contradicts the seat's own document.
///
/// This is the half of the daemon's own promise that the code did not keep. Its paragraph in `get_room_status`
/// says a claim left behind by a pane's teardown is "replaced with the death it can actually observe", and it did
/// that only when the PANE was gone. The measured case is the other one: the agent is killed (SIGHUP) and the pane
/// outlives it, so the file keeps the last word the runner wrote - `working`, 42 minutes stale - and every reader
/// of that document, this status and the client's watch alike, is told a seat is working whose agent was killed.
///
/// The publisher's pid is what makes it observable: the runner stamps its own pid beside the state it writes, so
/// `Some(false)` says the author of the claim is not there any more, while `None` says this host cannot tell.
fn observed_death(facts: &SeatFacts, publishes_readiness: bool) -> Option<String> {
    let pid = facts.publisher_pid?;
    if !facts.alive || !publishes_readiness {
        return None;
    }
    if facts.publisher_alive != Some(false) {
        return None;
    }
    if facts.published_state.as_deref().is_some_and(is_death_word) {
        return None;
    }
    Some(format!(
        "agent_exited: the runner that publishes this seat's state (pid {pid}) is gone while its pane is up, so \
         its last word `{}` is a claim nobody is left to update",
        facts.published_state.as_deref().unwrap_or("unknown"),
    ))
}

/// The death this generation has already recorded, if any: the seat's own file, written after the launch, and
/// publishing one of `DEATH_STATES`. `None` if there is no such file - which is the ordinary case while a first
/// launch is still building its cache, and is why this cannot be inferred from the file being absent.
fn fresh_death_state(path: &Path, launched_at: std::time::SystemTime) -> Option<String> {
    let modified = fs::metadata(path).and_then(|meta| meta.modified()).ok()?;
    if modified < launched_at {
        return None;
    }
    match published_state(path).as_deref() {
        Some(state) if DEATH_STATES.contains(&state) => Some(state.to_string()),
        _ => None,
    }
}

/// Whether `path` is a readiness signal written by THIS generation's pane: it must exist, be NEWER than the
/// launch, and not be a death record.
///
/// Existence alone was the whole proof, and it is a green that cannot fail. The previous generation's teardown
/// writes a death record INTO the same path - `agent_socket_closed`, by the runner and again by the daemon when
/// it observes a pane end - and `room stop`'s own message invites a restart in the same state dir. Measured
/// twice: that restart returned running in 0.03 s, judged on a file whose own content said `agent_socket_closed`
/// and whose `updated_at` was 0.3 s BEFORE the new pane was launched, while a first start in a fresh state dir
/// took 2.00-2.34 s. Freshness is the load-bearing half; the death check keeps a coarse clock from reading a
/// death as a handshake.
fn readiness_written_by(path: &Path, launched_at: std::time::SystemTime) -> bool {
    let Ok(modified) = fs::metadata(path).and_then(|meta| meta.modified()) else {
        return false;
    };
    if modified < launched_at {
        return false;
    }
    !matches!(
        published_state(path).as_deref(),
        Some(state) if DEATH_STATES.contains(&state)
    )
}

/// The executable a newly launched seat's pane must run: THIS binary, named by a path that still reaches it.
///
/// `std::env::current_exe()` answers through `/proc/self/exe`, and on Linux a binary REPLACED ON DISK while it
/// ran - a rebuild, `cargo install`, a `cp` over `~/.local/bin/agenthub-runtime` - leaves the running process
/// holding `…/agenthub-runtime (deleted)`: a string, not a file. Measured 2026-10-18 on this box: the room's
/// daemon started 10:02, the installed binary was replaced later that day, and every seat started after that
/// died within milliseconds. The pane's whole output, recovered with `remain-on-exit` on, was
/// `bash: -c: line 1: 'env -u TMUX -u TMUX_PANE /home/daihu/.local/bin/agenthub-runtime (deleted) --state-dir
/// …'` with exit status 2, and the supervisor reported `its pane is gone` 20 ms after the launch with nothing
/// naming the cause - three start attempts, all identical.
///
/// The inode is still executable for as long as this process lives, and `/proc/<pid>/exe` names it after the
/// unlink (the kernel resolves the magic link to the inode). So a path that no longer exists is replaced by
/// that one, and a process that has neither is refused HERE, by name, instead of handing a shell a string that
/// cannot exec.
fn launch_executable(resolved: PathBuf, proc_exe: PathBuf) -> Result<PathBuf, String> {
    if resolved.is_file() {
        return Ok(resolved);
    }
    if proc_exe.is_file() {
        return Ok(proc_exe);
    }
    Err(format!(
        "this daemon's own binary no longer exists on disk ({}), and {} names nothing either - a seat launched \
         now would die in its pane, so nothing was launched; restart the daemon so new seats run the installed \
         agenthub-runtime",
        resolved.display(),
        proc_exe.display()
    ))
}

/// The state a state file publishes, or `None` when it is missing or unparseable. The same field
/// `seat_facts` reads for its own report, kept separate here because the readiness loop asks the question
/// before any status exists.
fn published_state(path: &Path) -> Option<String> {
    let text = fs::read_to_string(path).ok()?;
    let value: serde_json::Value = serde_json::from_str(&text).ok()?;
    value
        .get("state")
        .and_then(|s| s.as_str())
        .map(str::to_string)
}

/// What to say when a seat never handed back its proof, and it is two different worlds: no document at all, or
/// a record from an earlier generation that this launch cannot accept as a handshake.
///
/// The refusal used to print only the first world - "(no <path>)" - while a STALE record sat on disk, which
/// tells a reader who can see that file nothing about why the start was refused. Kept as a function so both
/// worlds are pinned by a test rather than by a live run that has to be staged to reach them.
fn readiness_refusal(
    room: &str,
    seat: &str,
    path: &Path,
    launched_at: std::time::SystemTime,
) -> String {
    let stale = fs::metadata(path)
        .and_then(|meta| meta.modified())
        .map(|modified| modified < launched_at)
        .unwrap_or(false);
    match (stale, published_state(path)) {
        (true, Some(state)) => format!(
            "seat {room}/{seat} did not report readiness within {SEAT_READY_WINDOW_SECS}s: the only record on \
             disk is a stale `{state}` from an earlier generation, which this launch cannot accept as its \
             handshake; not reporting it as running"
        ),
        _ => format!(
            "seat {room}/{seat} did not report readiness within {SEAT_READY_WINDOW_SECS}s (no readiness document \
             from this generation at {}); not reporting it as running",
            path.display()
        ),
    }
}

pub struct RoomSupervisor {
    base_state_dir: PathBuf,
    active_rooms: Arc<Mutex<HashMap<String, RoomSpec>>>,
    /// THE SOCKET THIS DAEMON ANSWERS ON, kept because every seat it starts has to know it: a seat's own
    /// `4genteam send`/`room queue` resolves the socket from `$AGENTHUB_RUNTIME_SOCKET` and then this machine's
    /// own path, so a seat that is not told would address whichever daemon owns the default socket.
    runtime_socket: PathBuf,
    hook_bus: HookBus,
}

/// The one read of a seat's own state file and the room's rail that both the status row and the nudge
/// pass are built from (see `RoomSupervisor::seat_facts`).
struct SeatFacts {
    /// The seat's pane has a process behind it (`pane_is_live`: the session exists AND tmux does not report the
    /// pane dead). The session alone is not this fact - a pane kept by `remain-on-exit` outlives its command.
    alive: bool,
    /// The session is still there while `alive` is false: a pane kept by `remain-on-exit` after its command ended.
    /// The status names it, because it is the observation behind a `stopped` seat that a reader may see as a pane.
    pane_kept: bool,
    /// The runner's own word for what it is doing: `idle`, `retrying`, `needs_input`, `blocked`, or a
    /// death (`agent_exited`, …). `None` when the seat published nothing.
    published_state: Option<String>,
    /// THE ACTIVITY WORD the seat published, in the daemon's vocabulary - the fact a quiet wait and a
    /// reseat read. Only PARSED here, never derived: the runner writes it from the same transition as
    /// `published_state`, so this side cannot invent a second mapping. `Unknown` when the seat published
    /// nothing or published no word, which is never evidence.
    activity: ActivityWord,
    /// The seat's freshness stamp: the state file's `updated_at`.
    last_active: Option<DateTime<Utc>>,
    /// The pid the seat's document names as its publisher: the runner that wrote it.
    publisher_pid: Option<u32>,
    /// Whether that publisher is still there. `None` when this host cannot answer (no procfs) - which is NOT
    /// `false`: an unverifiable publisher must never be read as a dead one.
    publisher_alive: Option<bool>,
    /// The active rail rows addressed to this seat, by qitem id.
    waiting_qitem_ids: Vec<String>,
}

/// One seat the periodic nudge pass should act on, and the rows it is being woken for.
pub struct PendingNudge {
    pub room: String,
    pub seat: String,
    pub session_name: String,
    /// Whether this seat's runtime publishes a runner mailbox. It decides the nudge's ROUTE - a runner
    /// seat is reached through the mailbox the agent reads, a seat without one (`agy`, `codex`, a custom
    /// command) through its pane, exactly the split `deliver_text` makes - so the decision is carried from
    /// the status the pass already read rather than re-derived from the spec on the way out.
    pub has_runner: bool,
    pub qitem_ids: Vec<String>,
}

/// The names a rail row might use to address one seat, because the rail's rows have been written with
/// more than one spelling and a spelling the join missed reads as "nothing waiting" - the exact lie the
/// alert exists to prevent.
///
/// The four that exist in this tree's world: the session name this runtime derives
/// (`agenthub--<room>--<seat>`, the same one `SendTextToSeat` and the pane's `AGENTHUB_SESSION_NAME` use),
/// the bare seat key (what the design's own room recipe named and what a row written by hand usually
/// says), the room orchestrator session name the live room's rows carry (`<room>-<seat>@<room>`, where this
/// runtime's room IS the room name), and `seat@room` - the spelling the client stored verbatim before
/// `resolve_queue_destination` existed. New rows are written as the session name; the other three are the
/// backlog this join must still read.
fn addressed_names(room: &str, session: &str, seat: &str) -> [String; 4] {
    [
        session.to_string(),
        seat.to_string(),
        format!("{room}-{seat}@{room}"),
        // `seat@room` is the spelling a row was written in while create stored what it was given (measured
        // 2026-10-17: 32 rows on 4genthub-min, the owner asks among them). Create resolves new rows to the
        // session name, so this entry is the RAIL's backlog, not a second canonical form.
        format!("{seat}@{room}"),
    ]
}

/// The instant of a seat's newest recorded tool call, read from the tail of its own `tool-calls.jsonl`.
///
/// The guard appends one line per tool call for EVERY runtime (`SEAT_TOOL_HOOK`), agy included - and agy
/// is the reason this exists: its launch publishes no readiness channel, so its state document is written
/// once and never rewritten. A freshness stamp taken from that document alone said "quiet for 28 hours"
/// while the seat was closing gate rows (measured 2026-10-18 on 4genthub-min's reviewer: `updated_at`
/// 08:02:52, last logged tool call 08:49:22). The log is a fact about the seat's own work, and its newest
/// line is the only stamp a runnerless seat ever produces after launch.
///
/// The read is the file's TAIL, not the file: the log is append-only and unbounded (1.4 MB after ten days),
/// and a status call must not read it whole. The first line of a mid-file window is a fragment and is
/// dropped; the newest parseable `at` wins. An absent or unreadable log is `None`, never a guessed time.
fn last_tool_call_at(path: &Path) -> Option<DateTime<Utc>> {
    const TAIL_BYTES: u64 = 8 * 1024;
    let mut file = fs::File::open(path).ok()?;
    let len = file.metadata().ok()?.len();
    file.seek(SeekFrom::Start(len.saturating_sub(TAIL_BYTES)))
        .ok()?;
    let mut buf = Vec::new();
    file.take(TAIL_BYTES).read_to_end(&mut buf).ok()?;
    let text = String::from_utf8_lossy(&buf);
    let mut lines = text.lines();
    if len > TAIL_BYTES {
        lines.next();
    }
    lines
        .filter_map(|line| serde_json::from_str::<serde_json::Value>(line).ok())
        .filter_map(|v| {
            v.get("at")?
                .as_str()
                .and_then(|t| DateTime::parse_from_rfc3339(t).ok())
        })
        .map(|t| t.with_timezone(&Utc))
        .next_back()
}

/// The alert rule, on the facts alone: a seat that is up with active rows waiting for it carries one.
///
/// It is a free function so the rule can be tested without a tmux server - the join is what is being
/// pinned here, not the probes that feed it.
fn alert_for(facts: &SeatFacts) -> Option<SeatAlert> {
    if !facts.alive || facts.waiting_qitem_ids.is_empty() {
        return None;
    }
    Some(SeatAlert {
        kind: "seat_up_with_queued_work".to_string(),
        quiet_seconds: facts
            .last_active
            .map(|t| (Utc::now() - t).num_seconds().max(0)),
        waiting_qitem_ids: facts.waiting_qitem_ids.clone(),
    })
}

/// The seat's MCP config, which omp reads from its agent dir. It names the variables, never their values: the
/// URL and the token come from the `.env` that `seat_env` hands every seat, so this file holds no secret.
const SEAT_MCP_CONFIG: &str = r#"{"mcpServers":{"agenthub_http":{"type":"http","url":"${AGENTHUB_URL}/mcp","headers":{"Accept":"application/json, text/event-stream","Authorization":"Bearer ${AGENTHUB_TOKEN}"}}}}
"#;

/// The variable that names the deepseek MCP server's script. THE PATH IS NEVER A LITERAL in this file: it is
/// read from the seat's own environment (the state root's `.env`, then the spec), so a machine that names none
/// writes no entry at all - the absence rule `SEAT_MCP_CONFIG` follows.
const DEEPSEEK_MCP_SERVER_ENV: &str = "DEEPSEEK_MCP_SERVER";

/// The delegation server's default working directory: the seat's own first scope directory, set by `seat_env`
/// and read by the server. A job runs on the seat's files, never on whatever directory the daemon was started
/// in - one daemon starts every seat of every room.
const DEEPSEEK_MCP_CWD_ENV: &str = "DEEPSEEK_MCP_DEFAULT_CWD";

/// The entry that gives a seat `deepseek_agent` and the rest of the delegation server's tools. The script is
/// the one the environment names; the working directory is the seat's scope; the server's own git guard refuses
/// writes to the caller's branch, and its permission prompts take the first allow, which is what an unattended
/// seat can answer.
fn deepseek_mcp_entry(server: &str, cwd: &Path) -> serde_json::Value {
    serde_json::json!({
        "command": "node",
        "args": [server],
        "env": {
            DEEPSEEK_MCP_CWD_ENV: cwd.to_string_lossy(),
            "DEEPSEEK_WORKSPACE_ATTACH": "1",
            "DEEPSEEK_MCP_PERMISSION": "allow",
        }
    })
}

/// The seat's `mcp.json`: the cloud entry every seat has, plus the delegation server when the seat's
/// environment names one. With no `DEEPSEEK_MCP_SERVER` the file is `SEAT_MCP_CONFIG` VERBATIM - a machine that
/// configures nothing writes exactly today's file.
fn seat_mcp_config(env: &HashMap<String, String>, cwd: &Path) -> String {
    let Some(server) = env
        .get(DEEPSEEK_MCP_SERVER_ENV)
        .map(String::as_str)
        .map(str::trim)
        .filter(|server| !server.is_empty())
    else {
        return SEAT_MCP_CONFIG.to_string();
    };
    let mut config: serde_json::Value =
        serde_json::from_str(SEAT_MCP_CONFIG).expect("the seat MCP config is JSON");
    config["mcpServers"]["deepseek"] = deepseek_mcp_entry(server, cwd);
    serde_json::to_string(&config).expect("the seat MCP config is JSON") + "\n"
}

/// The seat's tool hook: omp loads `hooks/pre/*.ts` from its agent dir, and this one refuses the calls the owner's
/// rules forbid a seat (printing a `.env`, Docker, a push) and appends one line per tool call to the seat's
/// `tool-calls.jsonl`. The runtime owns it because the seat dir is the runtime's: a hook a seat or an operator had
/// to remember to install is a hook that is missing on the next seat.
const SEAT_TOOL_HOOK: &str = include_str!("../../assets/seat-guard.ts");

/// The command an agy seat runs. agy takes its model from `--model` and otherwise from the owner's default, which
/// can be out of quota: an agy seat started as bare `agy` took each message and never answered (the reviewer,
/// 2026-10-15). The model is single-quoted because the command is a shell line.
fn agy_command(model: Option<&str>) -> String {
    match model.map(str::trim).filter(|m| !m.is_empty()) {
        Some(model) => format!("agy --model '{}'", model.replace('\'', r"'\''")),
        None => "agy".to_string(),
    }
}

/// The name of the entry the runtime owns in agy's global `hooks.json`.
const AGY_GUARD_HOOK: &str = "seat-guard";

/// The entry agy runs around every tool step. agy reads its hooks from `~/.gemini/config/hooks.json` for every
/// session on the machine, so the command answers for both kinds: a seat (`AGENTHUB_SEAT` is set) runs the seat
/// guard from its own agent dir, and any other session gets agy's own default, "ask" before a tool and `{}` after
/// one. agy treats an answer with no decision as a refusal, which is why the other session cannot simply print `{}`.
fn agy_guard_entry() -> serde_json::Value {
    let command = |phase: &str, otherwise: &str| {
        format!(
            "if [ -n \"$AGENTHUB_SEAT\" ]; then exec node \"$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts\" --agy-hook {phase}; else echo '{otherwise}'; fi"
        )
    };
    serde_json::json!({
        "PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "timeout": 10,
            "command": command("pre", "{\"decision\":\"ask\"}")}]}],
        "PostToolUse": [{"matcher": "*", "hooks": [{"type": "command", "timeout": 10,
            "command": command("post", "{}")}]}],
    })
}

/// The settings file a claude seat is launched with (`--settings`); the runner adds the flag when it is there.
pub(crate) const CLAUDE_SETTINGS_FILE: &str = "settings.json";

/// The settings a claude seat's own file carries: the seat guard on every event that carries a decision or an
/// outcome, the phase as its argument. agy is told the same thing in one global file (`agy_guard_entry`); claude has
/// no global hook file a per-seat entry could live in, so the file is the seat's own and the command resolves the
/// seat's guard from the agent dir the supervisor exports for every seat (`PI_CODING_AGENT_DIR`), the same path
/// agy's entry names.
///
/// `PostToolUseFailure` is its own event and its own entry: `PostToolUse` carries `tool_response` and no error, so a
/// guard registered only there logs `ok` for a call that failed. The three entries share one command because the
/// guard reads `hook_event_name` from the payload.
fn claude_guard_settings() -> String {
    let command = |phase: &str| {
        format!("node \"$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts\" --claude-hook {phase}")
    };
    serde_json::json!({
        "hooks": {
            "PreToolUse": [{"matcher": "*",
                "hooks": [{"type": "command", "timeout": 10, "command": command("pre")}]}],
            "PostToolUse": [{"matcher": "*",
                "hooks": [{"type": "command", "timeout": 10, "command": command("post")}]}],
            "PostToolUseFailure": [{"matcher": "*",
                "hooks": [{"type": "command", "timeout": 10, "command": command("post")}]}],
        }
    })
    .to_string()
}

/// Whether the bundled guard carries the `--claude-hook` mode. It does: the owner landed the mode in
/// `assets/seat-guard.ts` (which `protectedPath` refuses to a seat) together with this flag. This flag, not a
/// substring of the asset, is the gate: the asset's own header already describes `--agy-hook` in prose, so a
/// comment mentioning the claude flag would flip a substring test open while no mode exists and plant a hook that
/// answers nothing. `the_claude_settings_run_the_seat_guard_on_both_phases` fails if it is true while the asset
/// lacks the mode.
const GUARD_HAS_CLAUDE_HOOK: bool = true;

/// Whether `path` is the file this runtime wrote: byte for byte the text `claude_guard_settings` produces. A file
/// that differs was put there by someone else, and a seat's own directory is the runtime's to fill, not to empty -
/// the agy installer beside it refuses a foreign file rather than rewriting it, and so does this one clear only its
/// own.
pub(crate) fn is_claude_guard_settings(path: &Path) -> bool {
    fs::read_to_string(path).is_ok_and(|text| text == claude_guard_settings() + "\n")
}

/// Write the claude seat's settings file, or clear one left by an earlier start. A guard without the
/// `--claude-hook` mode is one that never reads this payload and never answers, so writing the file for it would
/// plant a hook no tool call is checked by; the runner's flag follows this file, so the two cannot disagree.
fn write_claude_guard_settings(seat_dir: &Path) -> Result<(), String> {
    write_claude_guard_settings_when(seat_dir, GUARD_HAS_CLAUDE_HOOK)
}

/// The writer with its gate as an argument, so a test exercises the file it writes while the bundled guard still
/// lacks the mode. Production reaches it only through `write_claude_guard_settings`.
pub(crate) fn write_claude_guard_settings_when(
    seat_dir: &Path,
    guard_carries_mode: bool,
) -> Result<(), String> {
    let path = seat_dir.join(CLAUDE_SETTINGS_FILE);
    if !guard_carries_mode {
        if is_claude_guard_settings(&path) {
            fs::remove_file(&path).map_err(|e| format!("cannot clear {}: {e}", path.display()))?;
        }
        return Ok(());
    }
    fs::write(&path, claude_guard_settings() + "\n")
        .map_err(|e| format!("cannot write {}: {e}", path.display()))
}

/// Make sure agy's global `hooks.json` carries the seat guard, and touch nothing else in it: the file also holds
/// other tools' hooks. A file that is not a JSON object is an error rather than a file to overwrite.
fn install_agy_guard_hook(home: &Path) -> Result<(), String> {
    let path = home.join(".gemini").join("config").join("hooks.json");
    let mut hooks = match fs::read_to_string(&path) {
        Ok(text) => serde_json::from_str::<serde_json::Value>(&text)
            .ok()
            .filter(serde_json::Value::is_object)
            .ok_or_else(|| {
                format!(
                    "{} is not a JSON object; the agy seat guard was not installed",
                    path.display()
                )
            })?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => serde_json::json!({}),
        Err(e) => return Err(format!("cannot read {}: {e}", path.display())),
    };
    let entry = agy_guard_entry();
    if hooks.get(AGY_GUARD_HOOK) == Some(&entry) {
        return Ok(());
    }
    hooks[AGY_GUARD_HOOK] = entry;
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir).map_err(|e| format!("cannot create {}: {e}", dir.display()))?;
    }
    let text = serde_json::to_string_pretty(&hooks).map_err(|e| e.to_string())?;
    fs::write(&path, text).map_err(|e| format!("cannot write {}: {e}", path.display()))
}

/// The name of the entry the runtime owns in agy's global `mcp_config.json`. It is the same name the omp seat
/// dir carries, so a seat's tools are called the same thing whichever runtime hosts it.
const AGY_MCP_SERVER: &str = "agenthub_http";

/// What an agy session runs to reach the cloud. agy spawns its own environment with every server it starts, so
/// the LINE reads the seat's variables at connect time instead of the file holding a copy of them, and a session
/// that is not a seat (`AGENTHUB_SEAT` unset) starts no bridge at all. agy speaks stdio to `mcp-proxy`, which
/// speaks Streamable HTTP to the seat's own daemon: the transport a `url` entry would use, in the one form that
/// needs no credential in agy's shared config file.
///
/// The credential reaches the bridge in its ENVIRONMENT, never in an argument: `mcp-proxy` reads
/// `API_ACCESS_TOKEN` from its own environment and sets `Authorization: Bearer <it>` itself, and the shell
/// hands the seat's token over through a variable assignment that `exec` carries into the child's environment.
/// An expanded `--headers Authorization` argument would sit in `/proc/<pid>/cmdline`, which every local user can
/// read; `/proc/<pid>/environ` is owner-only. The bridge's argv is the transport, the Accept header and the URL.
const AGY_MCP_COMMAND: &str = concat!(
    "if [ -n \"$AGENTHUB_SEAT\" ]; then API_ACCESS_TOKEN=\"$AGENTHUB_TOKEN\" exec mcp-proxy --transport streamablehttp",
    " --headers Accept 'application/json, text/event-stream'",
    " \"$AGENTHUB_URL/mcp\"; fi"
);

/// The entry agy loads for the seat's tools.
fn agy_mcp_entry() -> serde_json::Value {
    serde_json::json!({"command": "sh", "args": ["-c", AGY_MCP_COMMAND], "disabled": false})
}

/// The second entry the runtime owns in agy's global file: the delegation server, under the same name the omp
/// seat file gives it, so a seat's tools are called the same thing whichever runtime hosts it.
const AGY_DEEPSEEK_SERVER: &str = "deepseek";

/// What an agy session runs for the delegation server, in the same form as the cloud bridge: the LINE reads the
/// seat's own variables at connect time, so this shared file holds no path, and both halves of the guard are
/// checked - a session that is not a seat, or one whose environment names no script, starts nothing.
const AGY_DEEPSEEK_COMMAND: &str = concat!(
    "if [ -n \"$AGENTHUB_SEAT\" ] && [ -n \"$DEEPSEEK_MCP_SERVER\" ];",
    " then exec node \"$DEEPSEEK_MCP_SERVER\"; fi"
);

/// The entry agy loads for the delegation server.
fn agy_deepseek_entry() -> serde_json::Value {
    serde_json::json!({"command": "sh", "args": ["-c", AGY_DEEPSEEK_COMMAND], "disabled": false})
}

/// Make sure agy's global `mcp_config.json` carries the seat's MCP server and, when the seat's environment names
/// a script, the delegation server, and touch nothing else in it: the file also holds the owner's own servers. A
/// file that is not a JSON object is an error rather than a file to overwrite, and so is an `mcpServers` that is
/// not an object.
///
/// The delegation name is OURS: a `deepseek` entry without the script in the environment is removed, because a
/// hardcoded path left in a shared file is the defect this install exists to remove. With the script configured
/// the entry is replaced by the guarded line, and every other server is left as it was.
fn install_agy_mcp_server(home: &Path, deepseek: bool) -> Result<(), String> {
    let path = home.join(".gemini").join("config").join("mcp_config.json");
    let mut config = match fs::read_to_string(&path) {
        Ok(text) => serde_json::from_str::<serde_json::Value>(&text)
            .ok()
            .filter(serde_json::Value::is_object)
            .ok_or_else(|| {
                format!(
                    "{} is not a JSON object; the agy MCP server was not installed",
                    path.display()
                )
            })?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => serde_json::json!({}),
        Err(e) => return Err(format!("cannot read {}: {e}", path.display())),
    };
    if config
        .get("mcpServers")
        .is_some_and(|servers| !servers.is_object())
    {
        return Err(format!(
            "{} has an mcpServers that is not an object; the agy MCP server was not installed",
            path.display()
        ));
    }
    if config.get("mcpServers").is_none() {
        config["mcpServers"] = serde_json::json!({});
    }
    let changed = {
        let servers = config["mcpServers"]
            .as_object_mut()
            .expect("mcpServers is an object");
        let mut changed = false;
        let entry = agy_mcp_entry();
        if servers.get(AGY_MCP_SERVER) != Some(&entry) {
            servers.insert(AGY_MCP_SERVER.to_string(), entry);
            changed = true;
        }
        if deepseek {
            let entry = agy_deepseek_entry();
            if servers.get(AGY_DEEPSEEK_SERVER) != Some(&entry) {
                servers.insert(AGY_DEEPSEEK_SERVER.to_string(), entry);
                changed = true;
            }
        } else if servers.remove(AGY_DEEPSEEK_SERVER).is_some() {
            changed = true;
        }
        changed
    };
    if !changed {
        return Ok(());
    }
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir).map_err(|e| format!("cannot create {}: {e}", dir.display()))?;
    }
    let text = serde_json::to_string_pretty(&config).map_err(|e| e.to_string())?;
    fs::write(&path, text).map_err(|e| format!("cannot write {}: {e}", path.display()))
}

/// Everything an agy seat's start installs in agy's OWN files, in one place: agy is the runtime whose tool guard
/// and whose MCP servers both live in its config, and both are inert outside a seat.
fn install_agy_start_files(home: &Path, deepseek: bool) -> Result<(), String> {
    install_agy_guard_hook(home)?;
    install_agy_mcp_server(home, deepseek)
}

/// The variable that names the client's skill tree (`share/`, `rooms/<room>/`, `rooms/<room>/<seat>/`). It is
/// read from the state root's `.env`, else from the daemon's own environment.
const SKILLS_ROOT_ENV: &str = "AGENTHUB_CLIENT_SKILLS";

/// Install the seat's skills into `<seat dir>/skills/<name>`, which is where omp (its agent dir is the seat
/// dir) loads them from: the shared skills, the room's, and the seat's own folder, the more specific one
/// replacing a shared skill of the same name. The directory is runtime-owned, so it is rebuilt every start
/// and a skill removed from the tree does not linger. No tree configured means no skills, which is not an
/// error: a machine with no tree has nothing to install.
///
/// THE SEAT'S WORKFLOW IS ALWAYS IN CONTEXT, NOT LOADED ON DEMAND: a skill reaches the model only when its
/// description matches the turn, and a seat that never asked "how do I work here" never read its own method.
/// The body of the seat's `<seat>-workflow` skill is therefore also written to `<seat dir>/AGENTS.md`, which
/// omp loads at every start from its agent dir and claude receives through `--append-system-prompt-file`.
/// That file is runtime-owned like `skills/`: a seat with no workflow skill has none left behind.
fn install_seat_skills(
    state_dir: &Path,
    room: &str,
    seat: &str,
    seat_dir: &Path,
) -> Result<(), String> {
    let root = env_file_vars(state_dir)
        .remove(SKILLS_ROOT_ENV)
        .or_else(|| std::env::var(SKILLS_ROOT_ENV).ok())
        .filter(|v| !v.is_empty());
    let Some(root) = root else { return Ok(()) };
    let target = seat_dir.join("skills");
    if target.exists() {
        fs::remove_dir_all(&target)
            .map_err(|e| format!("cannot clear {}: {e}", target.display()))?;
    }
    let workflow_name = format!("{seat}-workflow");
    let mut workflow = None;
    for skill in crate::skills::SkillResolver::new(root).resolve_for_seat(room, seat)? {
        let Some(source) = skill.path.parent() else {
            continue;
        };
        copy_dir(source, &target.join(&skill.name))
            .map_err(|e| format!("cannot install the skill {}: {e}", skill.name))?;
        if skill.name == workflow_name {
            workflow = Some(skill.content);
        }
    }
    let agents_md = seat_dir.join(SEAT_WORKFLOW_FILE);
    match workflow {
        Some(body) => fs::write(&agents_md, format!("{body}\n"))
            .map_err(|e| format!("cannot write {}: {e}", agents_md.display()))?,
        None if agents_md.exists() => fs::remove_file(&agents_md)
            .map_err(|e| format!("cannot clear {}: {e}", agents_md.display()))?,
        None => {}
    }
    Ok(())
}

/// The seat's always-loaded workflow, in the seat dir (see `install_seat_skills`).
pub(crate) const SEAT_WORKFLOW_FILE: &str = "AGENTS.md";

/// Copy a directory tree, replacing what is at `to`. Every file under a skill goes with it (a skill may
/// carry baselines and references beside its SKILL.md).
///
/// THE COPY DECIDES THESE MODES, NOT THE PROCESS'S UMASK: `fs::copy` carries the SOURCE's permission bits
/// across, so a skill that ships `0644` in the room landed `0644` in the seat's own tree whatever the umask
/// said - measured on a scratch room 2026-10-16, `rooms/<room>/seats/<seat>/skills/**` was the one part of
/// the state root `restrict_umask` did not reach. The group's and the world's bits are dropped here and the
/// owner's are kept, so an asset that ships executable stays executable for its owner and nothing more.
fn copy_dir(from: &Path, to: &Path) -> std::io::Result<()> {
    if to.exists() {
        fs::remove_dir_all(to)?;
    }
    fs::create_dir_all(to)?;
    for entry in fs::read_dir(from)? {
        let entry = entry?;
        let dest = to.join(entry.file_name());
        if entry.file_type()?.is_dir() {
            copy_dir(&entry.path(), &dest)?;
        } else {
            fs::copy(entry.path(), &dest)?;
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                let mode = fs::metadata(entry.path())?.permissions().mode() & 0o700;
                fs::set_permissions(&dest, fs::Permissions::from_mode(mode))?;
            }
        }
    }
    Ok(())
}

/// The variables of `<state_dir>/.env`: `KEY=VALUE` lines, an optional `export ` prefix, one pair of matching
/// quotes stripped, `#` comment and blank lines skipped. A missing or unreadable file is no variables. Values
/// are never logged: the file holds credentials.
fn env_file_vars(state_dir: &Path) -> HashMap<String, String> {
    let Ok(text) = fs::read_to_string(state_dir.join(".env")) else {
        return HashMap::new();
    };
    let mut vars = HashMap::new();
    for line in text.lines() {
        let line = line.trim();
        let line = line.strip_prefix("export ").unwrap_or(line).trim_start();
        if line.starts_with('#') {
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let key = key.trim();
        if key.is_empty() || !key.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') {
            continue;
        }
        let value = value.trim();
        let value = match value.as_bytes() {
            [q @ (b'"' | b'\''), .., last] if q == last && value.len() >= 2 => {
                &value[1..value.len() - 1]
            }
            _ => value,
        };
        vars.insert(key.to_string(), value.to_string());
    }
    vars
}

/// The environment ONE seat's runner and its agent are launched with.
///
/// A pure function rather than an inline block, so what a seat inherits can be pinned without a tmux server:
/// this map IS the contract between the daemon and the client a seat runs (`4genteam send`, `room queue`), and
/// a field missing here is a seat that answers a different daemon, or none.
///
/// `AGENTHUB_RUNTIME_SOCKET` is the daemon's OWN socket, and it OVERRIDES whatever the room's spec recorded:
/// the socket that matters to a seat is the one the daemon that started it answers on, and a value written into
/// a spec at `up` time can only be the socket of whichever daemon existed then. Measured 2026-10-14 in an
/// isolated run: the run's own seat carried no socket at all, resolved this machine's own (the box's live
/// runtime) and had its message refused for a mailbox under the LIVE state directory.
fn seat_env(
    seat: &SeatSpec,
    room: &str,
    session_name: &str,
    seat_dir: &Path,
    runtime_socket: &Path,
    state_dir: &Path,
    cwd: &Path,
) -> HashMap<String, String> {
    // The state root's own `.env` is the client's one configuration file (the MCP URL and token, the provider
    // keys): every seat starts with it, so no seat is asked to hold a copy. A key the seat's spec names wins
    // over the file, and the runtime-owned keys below win over both.
    let mut env = env_file_vars(state_dir);
    env.extend(seat.env.clone());
    env.insert("AGENTHUB_ROOM".into(), room.into());
    env.insert("AGENTHUB_SEAT".into(), seat.seat_key.clone());
    env.insert("AGENTHUB_ROLE".into(), seat.seat_type.clone());
    env.insert("AGENTHUB_RUNTIME".into(), seat.runtime.clone());
    env.insert("AGENTHUB_SESSION_NAME".into(), session_name.into());
    env.insert(
        "PI_CODING_AGENT_DIR".into(),
        seat_dir.to_string_lossy().to_string(),
    );
    env.insert(
        "AGENTHUB_RUNTIME_SOCKET".into(),
        runtime_socket.to_string_lossy().to_string(),
    );
    if let Some(m) = &seat.model {
        env.insert("AGENTHUB_MODEL".into(), m.clone());
    }
    // THE DELEGATION SERVER'S TWO FACTS, decided here with the rest of a seat's contract. The directory a job
    // runs in is the seat's own first scope, never the daemon's cwd: one daemon starts every seat of every room,
    // so an inherited cwd would hand one room's tree to another room's job. The script is the environment's
    // (`DEEPSEEK_MCP_SERVER`, from the state root's `.env` or the seat's spec) and is carried to the seat even
    // though only mcp.json and agy's line consume it, because those two read the SEAT's environment at connect
    // time - a variable missing here is a server that starts with an empty path.
    env.insert(
        DEEPSEEK_MCP_CWD_ENV.into(),
        cwd.to_string_lossy().to_string(),
    );
    // The connector credential never enters a seat's environment, and the filter sits HERE - the one place a
    // seat's environment is decided - rather than downstream: this map IS the pane's `-e` list, so a key
    // dropped only at that point is a key that had already reached the seat's contract. Nothing a seat does
    // reads the connector token (a seat addresses its own daemon through `AGENTHUB_URL`/`AGENTHUB_TOKEN`).
    env.retain(|k, _| !crate::ENV_DENY_NAMES.contains(&k.as_str()));
    env
}

impl RoomSupervisor {
    pub fn new(
        base_state_dir: impl Into<PathBuf>,
        hook_bus: HookBus,
        runtime_socket: impl Into<PathBuf>,
    ) -> Self {
        let base_state_dir = base_state_dir.into();
        let _ = fs::create_dir_all(&base_state_dir);
        Self {
            base_state_dir,
            active_rooms: Arc::new(Mutex::new(HashMap::new())),
            runtime_socket: runtime_socket.into(),
            hook_bus,
        }
    }

    /// The root every room lives under; the IPC layer needs it to reach a seat's runner mailbox.
    pub fn state_dir(&self) -> &Path {
        &self.base_state_dir
    }

    /// Room state directory: ~/.config/4genthub/rooms/<room>/
    pub fn room_dir(&self, room: &str) -> PathBuf {
        self.base_state_dir.join("rooms").join(room)
    }

    /// The room's durable queue store, OPENED FOR THIS REQUEST.
    ///
    /// The rail is a file the room owns, and it can be replaced while the daemon lives: a rebuilt room, a
    /// restore, or an instrument's own hygiene (`rm -f queue.sqlite`, which the U3 harness does between legs)
    /// all hand the daemon a new file at the same path. A store held from the room's first use would keep
    /// writing into the deleted inode - SQLite answers `attempt to write a readonly database`, naming a
    /// permission on a file that is writable, and keeps serving the OLD rows as fact - and nothing but a
    /// daemon restart would clear it. So nothing is held: every verb opens the file it is about to use, and
    /// the next request cannot see a file that is no longer there. A room's descriptors then live as long as
    /// its request instead of as long as the daemon has ever touched the room.
    ///
    /// A room that was never started has no directory and no rail, so a queue verb against it is a refusal
    /// naming the room — not an empty queue, which is the same untrue answer as a bare `[]` (Rule 2: no
    /// answer where there is no record). A *stopped* room keeps its directory and its rows, and stays
    /// readable.
    pub fn queue_store(&self, room: &str) -> Result<QueueStore, String> {
        let room_dir = self.room_dir(room);
        if !room_dir.is_dir() {
            return Err(format!(
                "no such room '{room}': {} is not a room directory",
                room_dir.display()
            ));
        }
        QueueStore::open(&room_dir)
    }

    /// Derive standard session name: agenthub--<room>--<seat>
    pub fn session_name(room: &str, seat: &str) -> String {
        format!("agenthub--{room}--{seat}")
    }

    /// The CANONICAL session name of a native seat: `<room>-<seat>@<room>`.
    ///
    /// The room IS the room here - `room_dir`, the socket's own rooms and whoami's `roomName` all say so -
    /// which is the same rule the client's `SeatSessionName` doc states from the other side. It is the name
    /// a person passes to `--session`, so it is the first identifier the session list serves.
    pub fn canonical_session_name(room: &str, seat: &str) -> String {
        format!("{room}-{seat}@{room}")
    }

    /// The seat's logical id: `<room>.<seat>`. The client takes the member after the FIRST dot as the seat
    /// key, so the dot has to be there - a bare seat key names no member to that reader.
    pub fn logical_id(room: &str, seat: &str) -> String {
        format!("{room}.{seat}")
    }

    /// Start a room with its seats.
    pub fn start_room(&self, spec: RoomSpec) -> Result<Vec<SeatStatus>, String> {
        let room_name = spec.room.clone();
        info!(
            "starting room {} with {} seats",
            room_name,
            spec.seats.len()
        );
        // THE ROOM'S SEAT KEYS MUST BE TELLABLE APART BY NAME, and that is checked BEFORE the room's directory is
        // made: the guard admits a lane by the seat's own key prefix (`<seat>` or `<seat>-*` under the room's
        // `wt/`), so a room holding `go` and `go-dev` would let `go` write `go-dev`'s directory. A spec that
        // cannot satisfy the invariant is refused by both names rather than started with a hole in its guard.
        if let Some(refusal) = seat_key_collision(&spec) {
            return Err(refusal);
        }

        let room_dir = self.room_dir(&room_name);
        fs::create_dir_all(&room_dir).map_err(|e| format!("cannot create room dir: {e}"))?;
        // THE ROOM'S LAYOUT IS CREATED WITH THE ROOM, not left to whichever writer happens to get there
        // first: `seats/`, `logs/` and `run/` are the room's own shape, read by the acceptance and by an
        // operator, and a `logs/` that appeared only once a runner had a diagnostic to write made "no log
        // yet" and "no such directory" the same answer.
        for dir in ["seats", "logs", "run"] {
            fs::create_dir_all(room_dir.join(dir))
                .map_err(|e| format!("cannot create the room's {dir} dir: {e}"))?;
        }

        let mut final_spec = spec;
        let spec_file = room_dir.join("spec.json");
        if final_spec.seats.is_empty() && spec_file.exists() {
            if let Ok(content) = fs::read_to_string(&spec_file) {
                if let Ok(saved) = serde_json::from_str::<RoomSpec>(&content) {
                    final_spec = saved;
                }
            }
        }

        // Save spec to disk
        let spec_json = serde_json::to_string_pretty(&final_spec)
            .map_err(|e| format!("serialize spec: {e}"))?;
        let _ = fs::write(&spec_file, spec_json);

        let mut statuses = Vec::new();
        let mut failures = Vec::new();

        for seat in &final_spec.seats {
            match self.start_seat(&room_name, seat, &final_spec) {
                Ok(status) => statuses.push(status),
                Err(e) => {
                    // A seat that did not come up is reported as one: a caller must never have to infer a
                    // dead seat from an absent row. Design §6.2 point 5 says a partial start never exits 0,
                    // and this is the status that makes that buildable.
                    error!("failed to start seat {}: {e}", seat.seat_key);
                    failures.push(format!("{}: {e}", seat.seat_key));
                    statuses.push(Self::failed_seat_status(
                        &self.base_state_dir,
                        &room_name,
                        seat,
                        e,
                    ));
                }
            }
        }

        if statuses.iter().all(|s| s.state == SeatLifecycleState::Dead) {
            // Nothing came up, so the room has no rail to answer queue verbs from: it is not made active,
            // and the caller is told which seats failed and why.
            return Err(format!(
                "room {room_name} started no seat ({} of {}): {}",
                failures.len(),
                final_spec.seats.len(),
                failures.join("; ")
            ));
        }

        self.active_rooms.lock().insert(room_name, final_spec);
        Ok(statuses)
    }

    /// The status of a seat that did not come up: named, dead, no pid claimed, and its reason carried so the
    /// caller can say what happened instead of guessing from an absence.
    fn failed_seat_status(
        base_state_dir: &Path,
        room: &str,
        seat: &SeatSpec,
        reason: String,
    ) -> SeatStatus {
        SeatStatus {
            room: room.to_string(),
            seat: seat.seat_key.clone(),
            session_name: Self::session_name(room, &seat.seat_key),
            runtime: seat.runtime.clone(),
            model: seat.model.clone(),
            pid: None,
            state: SeatLifecycleState::Dead,
            // A seat that never came up published nothing, so there is no word to report: `Unknown` is
            // that answer, and it refuses by definition.
            activity: ActivityWord::Unknown,
            activity_source: None,
            detail: Some(reason),
            started_at: None,
            token_estimate: 0,
            last_active: None,
            transcript_path: seat_transcript_path(
                &base_state_dir.join("rooms").join(room),
                &seat.seat_key,
            )
            .map(|p| p.to_string_lossy().to_string()),
            // A seat that never came up has nothing waiting *for it* - it is not the seat the rail's rows
            // name - so there is no join to report.
            alert: None,
        }
    }

    /// Start a single seat, with the ONE retry the runtime promises when its pane dies.
    ///
    /// Measured 2026-10-18 (feedback 3ac8b369, three reproductions): a reseat killed the seat's pane, the
    /// start failed, and nothing in the seat's own log or state said what had happened - the runner never
    /// got far enough to write either. Reseat is the only route a landed skill takes to a running seat, so
    /// the failure left the seat stranded with a reason nobody could read.
    ///
    /// Two things are fixed here and they are separate: the failure is WRITTEN into the seat's log
    /// (`logs/<seat>.jsonl`) with the stage that failed, and a launch-stage failure is retried exactly
    /// once. A preparation refusal - a park, a spec that cannot be read, a directory that cannot be
    /// written - is NOT retried: a second attempt would repeat the same refusal and dress it up as a
    /// transient fault.
    pub fn start_seat(
        &self,
        room: &str,
        seat: &SeatSpec,
        spec: &RoomSpec,
    ) -> Result<SeatStatus, String> {
        let mut attempt = 0;
        loop {
            attempt += 1;
            match self.start_seat_once(room, seat, spec) {
                Ok(status) => return Ok(status),
                // Nothing was launched, so there is no pane for a retry to fix.
                Err(StartFailure::Preparation(reason)) => return Err(reason),
                Err(StartFailure::Launch { stage, reason }) => {
                    crate::runner::append_seat_log(
                        &self.base_state_dir,
                        room,
                        &seat.seat_key,
                        &format!(
                            "start attempt {attempt}/{START_ATTEMPTS} failed at {stage}: {reason}"
                        ),
                    );
                    if attempt >= START_ATTEMPTS {
                        // FINAL, and it says so: the retry is the last one, and a reader must not have to
                        // count attempts to learn that. No third attempt, no loop.
                        crate::runner::append_seat_log(
                            &self.base_state_dir,
                            room,
                            &seat.seat_key,
                            &format!("start final: {attempt} attempts failed, the last at {stage}: {reason}"),
                        );
                        return Err(format!(
                            "seat {room}/{} did not start after {attempt} attempts (final): the {stage} stage \
                             failed and was retried once - {reason}",
                            seat.seat_key
                        ));
                    }
                    // The retry clears whatever the first attempt left behind, so the second launch is not a
                    // collision with a half-started session. A stop that finds no session is not a failure.
                    let _ = self.stop_seat(room, &seat.seat_key);
                }
            }
        }
    }

    /// Start a single seat: preparation, one launch, and the verdict, all in ONE attempt.
    ///
    /// The retry lives in `start_seat`, which calls this at most twice and writes the seat's own log line
    /// for each failed launch. Split because the two halves answer different questions: a preparation
    /// refusal (a park, a spec that cannot be read, a directory that cannot be written) is final on the
    /// first attempt - retrying it would repeat the same refusal - while a pane that died is the one
    /// failure a second attempt can fix.
    fn start_seat_once(
        &self,
        room: &str,
        seat: &SeatSpec,
        spec: &RoomSpec,
    ) -> Result<SeatStatus, StartFailure> {
        // A PARKED SEAT IS NOT STARTED, and it is refused BEFORE anything is prepared for it: no seat dir
        // written, no mcp.json, no pane. Both routes that start a seat - `RestartSeat` and a room start -
        // funnel here, so a room start obeys the park through the room's own existing rule for a seat that did
        // not come up: it is reported by name with the park as the reason, and a room whose every seat is
        // parked starts no seat and says why.
        crate::park::refuse(
            self.state_dir(),
            room,
            &seat.seat_key,
            "nothing was started",
        )?;
        let session_name = Self::session_name(room, &seat.seat_key);
        let room_dir = self.room_dir(room);
        // WHERE THE SEAT RUNS AND WHAT IT MAY WRITE, decided before anything of it is written: a seat with no
        // directory of its own is refused here, so a room that names none cannot quietly hand its seats the
        // daemon's own directory again.
        let scope = seat_scope(seat, spec, &room_dir)?;
        let cwd = scope[0].clone();
        let seat_dir = room_dir.join("seats").join(&seat.seat_key);
        fs::create_dir_all(&seat_dir).map_err(|e| format!("cannot create seat dir: {e}"))?;
        // The seat's environment is decided BEFORE its config files, because two of them read it: mcp.json
        // carries the delegation server only when the environment names one, and agy's global file follows the
        // same presence rule. One map, computed once, is also the pane's `-e` list further down.
        let mut env = seat_env(
            seat,
            room,
            &session_name,
            &seat_dir,
            &self.runtime_socket,
            &self.base_state_dir,
            &cwd,
        );
        fs::write(seat_dir.join("mcp.json"), seat_mcp_config(&env, &cwd))
            .map_err(|e| format!("cannot write the seat's mcp.json: {e}"))?;
        // THE SEAT'S POLICY FILE LANDS HERE, in the same window as mcp.json and for the same reason: omp reads
        // `<PI_CODING_AGENT_DIR>/config.yml` when the agent starts, so a file written after the pane exists is a
        // file nothing read. The text is the HUB's render, delivered with the spec (the client fetched it); the
        // runtime does not derive it, which is what keeps the file and the render from disagreeing. A seat whose
        // document is absent gets NO file - the absence rule mcp.json follows - and only an omp seat is written
        // for: another runtime's config is not this document's destination.
        if matches!(
            AgentRuntime::from_str_loose(&seat.runtime),
            AgentRuntime::Omp
        ) {
            if let Some(document) = seat.policy_document.as_deref() {
                fs::write(seat_dir.join("config.yml"), document)
                    .map_err(|e| format!("cannot write the seat's config.yml: {e}"))?;
            }
        }
        let hook_dir = seat_dir.join("hooks").join("pre");
        fs::create_dir_all(&hook_dir)
            .map_err(|e| format!("cannot create the seat's hook dir: {e}"))?;
        fs::write(hook_dir.join("seat-guard.ts"), SEAT_TOOL_HOOK)
            .map_err(|e| format!("cannot write the seat's tool hook: {e}"))?;
        // THE CLAUDE SEAT'S HOOK FILE, in the same window as the guard it runs. claude runs no hook it is not
        // handed, and this is the only way the seat's rules reach it (the guard is the file omp loads); the runner
        // adds `--settings` while this file exists, so the argv never names a file that is not there.
        if matches!(
            AgentRuntime::from_str_loose(&seat.runtime),
            AgentRuntime::ClaudeCode
        ) {
            write_claude_guard_settings(&seat_dir)?;
        }
        install_seat_skills(&self.base_state_dir, room, &seat.seat_key, &seat_dir)?;
        if matches!(
            AgentRuntime::from_str_loose(&seat.runtime),
            AgentRuntime::Agy
        ) {
            let home = std::env::var_os("HOME").ok_or_else(|| {
                "HOME is not set, so agy's own config cannot be found".to_string()
            })?;
            install_agy_start_files(Path::new(&home), env.contains_key(DEEPSEEK_MCP_SERVER_ENV))?;
        }

        // Build command line according to runtime. An omp seat's pane runs THIS binary as a runner, which
        // spawns the agent and owns its channel (design §1: the runner replaces `pi-runner.js`); the other
        // runtimes keep their own command until their native rpc paths exist.
        let runtime = AgentRuntime::from_str_loose(&seat.runtime);
        let cmd = match &runtime {
            AgentRuntime::Omp | AgentRuntime::ClaudeCode => {
                let self_exe = launch_executable(
                    std::env::current_exe()
                        .map_err(|e| format!("cannot resolve this binary's path: {e}"))?,
                    Path::new("/proc")
                        .join(std::process::id().to_string())
                        .join("exe"),
                )?;
                format!(
                    "{} --state-dir {} runner {}/{}",
                    self_exe.display(),
                    self.base_state_dir.display(),
                    room,
                    seat.seat_key
                )
            }
            AgentRuntime::Agy => agy_command(seat.model.as_deref()),
            AgentRuntime::Codex => "codex".to_string(),
            AgentRuntime::Custom(c) => c.clone(),
        };

        // Build environment variables
        // The guard refuses writes outside the seat's own directories, and it takes them from HERE: the runtime
        // owns the room layout, so the list is decided once, at the launch, and never re-derived where it could
        // disagree. The first entry is the directory the pane starts in.
        env.insert(
            "AGENTHUB_WORKTREES".into(),
            scope
                .iter()
                .map(|dir| dir.to_string_lossy().to_string())
                .collect::<Vec<_>>()
                .join(":"),
        );
        // A LANE THE SEAT OPENS LATER HAS A NAME THE GUARD CAN TEST, instead of a list it would have to re-read:
        // the room's own `wt/`, handed beside the list, so a worktree created mid-session at
        // `<wt>/<seat>-<name>` is inside the scope without a directory scan on every write. The naming rule is
        // the one `own_worktrees` already applies at launch, and a launch that carries no value (an older
        // runtime) refuses exactly as before.
        env.insert(
            "AGENTHUB_LANES_ROOT".into(),
            room_dir.join("wt").to_string_lossy().to_string(),
        );

        // WHEN THIS GENERATION'S PANE WAS CREATED. The readiness proof below is a file the previous
        // generation's teardown also writes into, so existence alone cannot separate a handshake from an
        // inherited death record: this instant is what does. Taken BEFORE the launch, so a state file written
        // by this generation's runner is necessarily newer than it.
        let launched_at = std::time::SystemTime::now();
        // The wait the death message reports is MEASURED, so it needs a clock that cannot fail to subtract:
        // the message used to claim the window it was given, which the reader could not tell from a wait that
        // never happened.
        let launched = std::time::Instant::now();

        // Launch tmux session
        let pane =
            TerminalManager::launch_session(&session_name, &cwd, &cmd, &env).map_err(|e| {
                StartFailure::Launch {
                    stage: "tmux launch",
                    reason: e,
                }
            })?;

        // A launch is not liveness. What tmux just handed back - a pid, or the read's OWN failure, and never a
        // bare 0 manufactured from a parse failure - is a **record** of the session it created,
        // and the pane's process may already be gone - a seat whose runtime command does not exist came back
        // `running` with a pid and no session seconds later. So wait for proof that the seat actually got up
        // before saying so. An omp seat proves it with its state file
        // (`runner::state_file_path`), which its runner publishes only after the agent answered the transport
        // handshake; the other runtimes have no runner and prove less - their signal is the session still
        // existing after a grace period, which is stated here rather than passed off as handshake proof.
        let (signal, window) = if runtime.has_runner() {
            (
                Some(crate::runner::state_file_path(
                    &self.base_state_dir,
                    room,
                    &seat.seat_key,
                )),
                Duration::from_secs(SEAT_READY_WINDOW_SECS),
            )
        } else {
            (None, Duration::from_millis(NON_OMP_READY_GRACE_MS))
        };
        // Which proof this seat can give decides what the status may CLAIM. An omp seat proves itself with its
        // state file, which its runner publishes only once the agent answered the transport handshake, so RUNNING
        // there means an agent is talking. The other runtimes publish no readiness channel at all: the grace period
        // shows the pane is still there and nothing more, so a live process whose agent is unconfigured looks
        // exactly like one that is working. The status says which of the two it is, below.
        let proven_by_state_file = signal.is_some();
        let deadline = std::time::Instant::now() + window;
        loop {
            if signal
                .as_ref()
                .is_some_and(|path| readiness_written_by(path, launched_at))
            {
                break;
            }
            if !TerminalManager::is_session_alive(&session_name) {
                // A DEATH VERDICT IS NOT BUILT ON A READ THAT FAILED. `is_session_alive` answers false for a
                // session that is gone AND for a tmux that would not answer, and the pane-pid read above can
                // fail the same way; the message that followed used to report both as `its pane is gone` with
                // a pid of 0, so a read that failed and a pane that died were one sentence. Ask again - the
                // SAME read, immediately, not after a sleep: a delay would hide the race instead of asking a
                // second time - and refuse only when the second answer agrees.
                if TerminalManager::is_session_alive(&session_name) {
                    warn!(
                        "seat {room}/{}: one session read said the pane was gone and the next said it is there; \
                         keeping the wait ({})",
                        seat.seat_key,
                        pane.describe()
                    );
                } else {
                    return Err(StartFailure::Launch {
                        stage: "pane exit before readiness",
                        reason: format!(
                            "seat {room}/{} died before it reported readiness: two reads of the session agree its \
                             pane is gone, {} after the launch, and no ready frame ever arrived (the readiness \
                             window was {}; {})",
                            seat.seat_key,
                            launched.elapsed().as_millis(),
                            if signal.is_some() {
                                format!("{SEAT_READY_WINDOW_SECS}s")
                            } else {
                                format!("{NON_OMP_READY_GRACE_MS}ms")
                            },
                            pane.describe()
                        ),
                    });
                }
            }
            // A DEATH THE SEAT ITSELF RECORDED ENDS THE WAIT NOW, not at the deadline. The runner publishes
            // `agent_socket_closed` and friends when it survives to see the agent go; a pane that outlives its
            // agent is exactly the case liveness cannot see. Waiting the window out for a seat that has already
            // said it is dead is what would make a longer window cost a real verdict, so this clause is the
            // reason `SEAT_READY_WINDOW_SECS` can cover a cold first launch.
            if let Some(path) = signal.as_ref() {
                if let Some(state) = fresh_death_state(path, launched_at) {
                    return Err(StartFailure::Launch {
                        stage: "runner death record",
                        reason: format!(
                            "seat {room}/{} died before it reported readiness: its own record says `{state}`, \
                             written after this launch; not reporting it as running",
                            seat.seat_key
                        ),
                    });
                }
            }
            if std::time::Instant::now() >= deadline {
                match signal {
                    Some(path) => {
                        return Err(StartFailure::Launch {
                            stage: "readiness window",
                            reason: readiness_refusal(room, &seat.seat_key, &path, launched_at),
                        })
                    }
                    // The other runtimes have no runner that could hand a readiness word back, so the proof they
                    // can give is the one taken above: the session is STILL ALIVE after the grace period. Reaching
                    // this point means that check passed on this very iteration, so the seat is up - and the grace
                    // exists to catch the seat that dies inside it, which the pane check above already refuses by
                    // name. This arm used to return a survival verdict here; it now ends the wait instead, which is
                    // what the paragraph above always said the non-omp signal was.
                    None => break,
                }
            }
            std::thread::sleep(Duration::from_millis(100));
        }

        // A RUNNER-LESS SEAT'S STATE DOCUMENT BELONGS TO THIS LAUNCH, because nothing else will write one:
        // the previous generation's word - the reviewer's `agent_socket_closed`, written 04:00:36Z, three
        // hours before the 07:02:33Z pane that carried the live seat - would otherwise outlive the pane and
        // be read by every reader of the file (the watch, the room report, a status read). Written with the
        // PANE's pid, so a claim left behind names the process that can still be checked, and with
        // `activity: unknown`, because a pane that is up has not said what it is doing. Publishing runtimes
        // are left alone: their runner writes the document, and a launch record would claim a handshake it
        // has not made (the `Starting` arm of `get_room_status` exists for exactly that).
        if !AgentRuntime::from_str_loose(&seat.runtime).has_runner() {
            if let Some(pid) = pane.pid {
                let state_path = room_dir
                    .join("run")
                    .join(format!("{}.state.json", seat.seat_key));
                if let Err(e) = crate::runner::write_state_with_pid(
                    &state_path,
                    "running",
                    "the pane is up; this runtime publishes no readiness channel",
                    0,
                    pid,
                ) {
                    warn!(
                        "cannot publish the launch state for {room}/{}: {e}",
                        seat.seat_key
                    );
                }
            }
        }

        let status = SeatStatus {
            room: room.to_string(),
            seat: seat.seat_key.clone(),
            session_name: session_name.clone(),
            runtime: seat.runtime.clone(),
            model: seat.model.clone(),
            pid: pane.pid,
            state: SeatLifecycleState::Running,
            // The pane is up; the agent has not published a turn yet, so there is no activity word to
            // report and `Unknown` says exactly that. It is not `idle`: a seat that has not spoken has not
            // said it is at its prompt.
            activity: ActivityWord::Unknown,
            activity_source: None,
            // RUNNING means the seat is up, and the detail says up ON WHAT EVIDENCE: absent for an omp seat, whose
            // state file is the agent's own word, and stated for the others, where the only fact taken was that the
            // pane outlived the grace and no agent has confirmed anything.
            detail: if proven_by_state_file {
                None
            } else {
                Some(format!(
                    "pane alive after {NON_OMP_READY_GRACE_MS}ms; this runtime publishes no readiness channel, so a \
                     live but unconfigured agent is indistinguishable from a working one"
                ))
            },
            started_at: Some(Utc::now()),
            token_estimate: 0,
            last_active: Some(Utc::now()),
            // The agent has only just been launched, so it has usually written no session file yet.
            transcript_path: seat_transcript_path(&room_dir, &seat.seat_key)
                .map(|p| p.to_string_lossy().to_string()),
            // A seat that just launched has not had time to be owed anything; the join is read when the
            // status is read, not remembered from the launch.
            alert: None,
        };

        // Emit Hook event
        self.hook_bus.emit(SeatEvent::SessionStarted {
            room: room.to_string(),
            seat: seat.seat_key.clone(),
            session_name,
            pid: pane.pid,
            timestamp: Utc::now(),
        });

        Ok(status)
    }

    /// Stop all seats in a room.
    pub fn stop_room(&self, room: &str) -> Result<(), String> {
        let room_dir = self.room_dir(room);
        let spec_file = room_dir.join("spec.json");

        if let Ok(content) = fs::read_to_string(&spec_file) {
            if let Ok(spec) = serde_json::from_str::<RoomSpec>(&content) {
                for seat in spec.seats {
                    let session_name = Self::session_name(room, &seat.seat_key);
                    let _ = TerminalManager::stop_session(&session_name);
                    self.emit_terminated(room, &seat.seat_key);
                }
            }
        }

        self.active_rooms.lock().remove(room);
        // Nothing else is dropped with the room: the rail is opened per request and holds no state here, so
        // a stopped room's rows are durable-but-not-cached without the daemon having to remember anything.
        // A spill outlives no room (`SPEC-runner-relay-protocol-2026-10-13.md` §2): its payloads are the
        // room's bytes, and a tree left behind would be a payload no handle can name again.
        let _ = crate::frame::remove_spill_trees(&room_dir);
        Ok(())
    }

    /// Stop one seat.
    pub fn stop_seat(&self, room: &str, seat: &str) -> Result<(), String> {
        let session = Self::session_name(room, seat);
        TerminalManager::stop_session(&session)?;
        self.emit_terminated(room, seat);
        Ok(())
    }

    /// A stop the runtime was asked for. A seat that dies on its own is not seen here: the runtime learns of
    /// it only when a status read finds the death state, so no exit code is known and none is claimed.
    fn emit_terminated(&self, room: &str, seat: &str) {
        self.hook_bus.emit(SeatEvent::SessionTerminated {
            room: room.to_string(),
            seat: seat.to_string(),
            exit_code: None,
            timestamp: Utc::now(),
        });
    }

    /// Record the model one seat starts with, in the room's own spec. A running agent keeps the model it
    /// was spawned with, so this takes effect at the seat's next start.
    pub fn set_seat_model(&self, room: &str, seat: &str, model: &str) -> Result<(), String> {
        let model = model.trim();
        if model.is_empty() {
            return Err("a seat model must not be empty".into());
        }
        let spec_file = self.room_dir(room).join("spec.json");
        let content =
            fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec: {e}"))?;
        let mut spec: RoomSpec =
            serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        let seat_spec = spec
            .seats
            .iter_mut()
            .find(|s| s.seat_key == seat)
            .ok_or_else(|| format!("seat {seat} not found in room {room}"))?;
        seat_spec.model = Some(model.to_string());
        // Compute the document before the file is opened for writing: a failed serialize must not leave
        // the room's only spec truncated.
        let spec_json =
            serde_json::to_string_pretty(&spec).map_err(|e| format!("serialize spec: {e}"))?;
        fs::write(&spec_file, spec_json).map_err(|e| format!("cannot write room spec: {e}"))
    }

    /// Drop the agent sessions a seat saved, so its next agent starts without them. Nothing saved is not a
    /// failure.
    fn clear_saved_sessions(&self, room: &str, seat: &str) -> Result<(), String> {
        let sessions = seat_sessions_dir(&self.room_dir(room), seat);
        match fs::remove_dir_all(&sessions) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(format!("cannot clear the seat's saved sessions: {e}")),
        }
    }

    /// Restart one seat. `fresh` removes the seat's saved agent sessions after the stop, so the agent that
    /// starts has none of the previous one's context.
    ///
    /// `policy_document` is the document the caller has for this seat, if any (the client fetches the hub's
    /// render before a reseat, which is the act that installs a policy change). It is written into the room's
    /// own spec BEFORE the start, so the record every later route starts from carries it too - a heal, a
    /// restore or a restart from a caller with no document then installs the same file instead of a stale one.
    /// `None` leaves the record as it is.
    pub fn restart_seat(
        &self,
        room: &str,
        seat: &str,
        fresh: bool,
        policy_document: Option<&str>,
    ) -> Result<SeatStatus, String> {
        // The refusal comes BEFORE the stop: a parked seat is not brought back, and a restart that stopped
        // first would have changed the seat's state on its way to refusing.
        crate::park::refuse(self.state_dir(), room, seat, "nothing was restarted")?;
        self.stop_seat(room, seat)?;
        if fresh {
            self.clear_saved_sessions(room, seat)?;
        }
        let room_dir = self.room_dir(room);
        let spec_file = room_dir.join("spec.json");
        let content =
            fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec: {e}"))?;
        let mut spec: RoomSpec =
            serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        if let Some(document) = policy_document {
            let entry = spec
                .seats
                .iter_mut()
                .find(|s| s.seat_key == seat)
                .ok_or_else(|| format!("seat {seat} not found in room {room}"))?;
            entry.policy_document = Some(document.to_string());
            // Same shape as set_seat_model: the document is computed before the file is opened, so a failed
            // serialize cannot leave the room's only spec truncated.
            let spec_json =
                serde_json::to_string_pretty(&spec).map_err(|e| format!("serialize spec: {e}"))?;
            fs::write(&spec_file, spec_json).map_err(|e| format!("cannot write room spec: {e}"))?;
        }
        let seat_spec = spec
            .seats
            .iter()
            .find(|s| s.seat_key == seat)
            .ok_or_else(|| format!("seat {seat} not found in room {room}"))?;
        self.start_seat(room, seat_spec, &spec)
    }

    /// Get all seat statuses for a room.
    /// The links the room's spec declares between its seats. A room with none answers an empty list.
    pub fn get_room_links(&self, room: &str) -> Result<Vec<SeatLink>, String> {
        let content = fs::read_to_string(self.room_dir(room).join("spec.json"))
            .map_err(|e| format!("cannot read room spec: {e}"))?;
        let spec: RoomSpec =
            serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        Ok(spec.links)
    }

    /// Resolve the address a row's destination was written as into the session name the rail keys it by.
    ///
    /// THE ALERT AND THE NUDGE PASSES COMPARE `destination_session` WITH THE NAMES OF A SEAT (see
    /// `addressed_names`), so a row stored under a spelling they do not read wakes nobody. Measured
    /// 2026-10-17: `--to reviewer@4genthub-min` sat for ten minutes with no nudge while its session-name
    /// twin woke the seat in a second. Every accepted spelling therefore resolves to the session name AT
    /// WRITE TIME, so the row, the alert and the nudge read one key; a value naming no seat of THIS room is
    /// refused by name, with the seats that would have worked, because storing it is the silent loss.
    pub fn resolve_queue_destination(&self, room: &str, typed: &str) -> Result<String, String> {
        let content = fs::read_to_string(self.room_dir(room).join("spec.json"))
            .map_err(|e| format!("cannot read room spec: {e}"))?;
        let spec: RoomSpec =
            serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        let value = typed.trim();
        let (key, named_room) = match value.split_once('@') {
            Some((key, named)) => (key, Some(named)),
            None => (value, None),
        };
        if let Some(named) = named_room {
            if named != room {
                return Err(format!(
                    "--to {value:?} names room {named:?}, and this row is on {room}: a destination resolves only against the seats of the room the row is on. Nothing was written."
                ));
            }
        }
        for seat in &spec.seats {
            let session = Self::session_name(room, &seat.seat_key);
            let orchestrator = format!("{room}-{}@{room}", seat.seat_key);
            let keyed = format!("{room}-{}", seat.seat_key);
            if value == session || value == orchestrator || key == seat.seat_key || key == keyed {
                return Ok(session);
            }
        }
        let listed: Vec<String> = spec
            .seats
            .iter()
            .map(|seat| {
                format!(
                    "{} ({})",
                    seat.seat_key,
                    Self::session_name(room, &seat.seat_key)
                )
            })
            .collect();
        Err(format!(
            "--to {value:?} names no seat of {room} and no session of it, so the row would be stored under a destination no nudge can match; the room's seats are {}, and a row is addressed by a seat's session name, <seat>@{room}, or the bare seat key. Nothing was written.",
            listed.join(", ")
        ))
    }

    pub fn get_room_status(&self, room: &str) -> Result<Vec<SeatStatus>, String> {
        let room_dir = self.room_dir(room);
        let spec_file = room_dir.join("spec.json");
        let content =
            fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec: {e}"))?;
        let spec: RoomSpec =
            serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;

        let mut statuses = Vec::new();
        for seat in spec.seats {
            let session = Self::session_name(room, &seat.seat_key);
            // ONE read of the seat's own state file and one read of the rail, shared by the death reason,
            // the freshness stamp and the alert below: the runner publishes its state and its `updated_at`
            // in the same document, so reading it twice would be reading the same fact twice. This cannot
            // fail (the alert is advisory), so nothing here can turn the status into an outage.
            let facts = self.seat_facts(room, &seat.seat_key);
            // WHICH PROOF THIS SEAT CAN GIVE DECIDES WHAT THE STATUS MAY CLAIM, and it must be the same answer
            // `up` gave about the same seat in the same breath: a seat whose `up` refused and whose `room
            // status` then read `running` is the disagreement the lead measured. LIVENESS ALONE IS NOT
            // "RUNNING" where the runtime publishes a readiness document - a pane whose agent died stays alive,
            // and its own file names the death.
            let seat_runtime = AgentRuntime::from_str_loose(&seat.runtime);
            let publishes_readiness = seat_runtime.has_runner();
            let died = facts.published_state.as_deref().is_some_and(is_death_word);
            let state_path = room_dir
                .join("run")
                .join(format!("{}.state.json", seat.seat_key));
            // THE DEATH THIS SIDE CAN OBSERVE, which is not the same as the death the runner published. A pane that
            // outlived its agent leaves a claim behind, and the daemon owns this file's path - so the claim is
            // corrected WHERE THE SEAT'S OWN WORDS LIVE, because the readers of that document are not all this
            // function: the client's watch reads it, and so does anyone with `cat`. Written ONCE, not on every
            // status call: after the correction the file names a death and a live publisher, so `observed_death`
            // is silent from then on.
            let observed = observed_death(&facts, publishes_readiness);
            if let Some(why) = observed.as_deref() {
                let _ = crate::runner::write_state(&state_path, "agent_exited", why, 0);
            }
            let state = match (facts.alive, publishes_readiness, died, observed.is_some()) {
                (false, ..) => SeatLifecycleState::Stopped,
                (true, true, true, _) => SeatLifecycleState::Dead,
                // A pane that is up with no readiness document at all has not handshaked YET; calling that
                // `running` is what let a restart inherit the previous generation's green in 0.03 s.
                (true, true, false, false) if facts.published_state.is_none() => {
                    SeatLifecycleState::Starting
                }
                // AND A CLAIM WHOSE PUBLISHER IS GONE IS A DEATH THE PANE CANNOT SHOW: the runner is the process
                // that writes this file, and a seat whose agent was killed out from under its pane keeps the last
                // word it published - `working`, 42 minutes stale, measured.
                (true, true, false, true) => SeatLifecycleState::Dead,
                _ => SeatLifecycleState::Running,
            };

            // The reason, because "not running" alone is exactly the shape that let a dead turn look healthy
            // (`SPEC` §4). The runner publishes the three death facts when it survives to see them; when the
            // pane's teardown kills it first, the state file still claims `idle`/`running` — so the daemon,
            // which owns this file's path too, replaces that claim with the death it can actually observe.
            // AND THE PROOF, because a state name alone does not say what it rests on. The lead's honesty
            // requirement: the client's own output must show which of the two evidences produced the verdict -
            // the agent's own handshake, or a pane that outlived a grace.
            let evidence = match state {
                SeatLifecycleState::Running if seat_runtime == AgentRuntime::ClaudeCode => Some(
                    "the seat's own state file; claude-code announces nothing before its first turn, so this \
                     proves the runner and the agent's pipe are up, not that the model answered"
                        .to_string(),
                ),
                SeatLifecycleState::Running if publishes_readiness => Some(
                    "the seat's own state file, published after its agent answered the transport handshake"
                        .to_string(),
                ),
                SeatLifecycleState::Starting => Some(
                    "the pane is up and no handshake has been published for it yet".to_string(),
                ),
                // THE OBSERVED DEATH COMES FIRST: after a correction, the word still held in `published_state` is
                // the OLD one (`working`), and printing it as the name of the death would be the same lie in a new
                // place. `observed` is the sentence that says what actually happened.
                SeatLifecycleState::Dead => Some(
                    observed
                        .clone()
                        .or_else(|| facts.published_state.clone())
                        .unwrap_or_else(|| "agent_socket_closed".to_string()),
                ),
                // A STOPPED SEAT WHOSE PANE WAS KEPT SAYS WHAT IT OBSERVED: `remain-on-exit` outlives a command, so the
                // session is up and nothing is behind it - the exact shape that read `running`. The rest of the
                // stopped seats have no pane at all and fall through to the sentence below, which is what a seat that
                // never ran already reads.
                SeatLifecycleState::Stopped if facts.pane_kept => Some(
                    "tmux kept the seat's pane after its process ended: a pane is up with no live process behind it, \
                     and the seat published no death"
                        .to_string(),
                ),
                _ if facts.alive => Some(format!(
                    "pane alive after {NON_OMP_READY_GRACE_MS}ms; this runtime publishes no readiness channel, so a \
                     live but unconfigured agent is indistinguishable from a working one"
                )),
                _ => None,
            };
            let detail = if evidence.is_some() {
                evidence
            } else {
                match facts.published_state.clone() {
                    Some(s)
                        if matches!(
                            s.as_str(),
                            "agent_socket_closed" | "agent_stdio_eof" | "agent_exited"
                        ) =>
                    {
                        Some(s)
                    }
                    // AND NOTHING IS WRITTEN HERE. The document at `state_path` is the seat's own word,
                    // and this branch observed no death: a seat that was never started and one whose pane
                    // is gone are indistinguishable from here, and a reader that manufactures a claim
                    // stamps it with its own pid (`write_state` records the writer), so the next reader
                    // cannot tell the seat's evidence from the reader's assertion. Measured 2026-10-17
                    // 06:16:38Z: one status read over a seat that had never run left `agent_socket_closed`
                    // on disk, signed by the daemon. The only writers stay the runner (its real death) and
                    // the daemon through `observed_death` below, which has an observation to stand on.
                    _ => Some(
                        "agent_socket_closed: the pane ended without publishing a death"
                            .to_string(),
                    ),
                }
            };

            let transcript_path = seat_transcript_path(&room_dir, &seat.seat_key);

            statuses.push(SeatStatus {
                room: room.to_string(),
                seat: seat.seat_key,
                session_name: session,
                runtime: seat.runtime,
                model: seat.model,
                pid: None,
                state,
                activity: facts.activity,
                activity_source: facts.published_state.clone(),
                detail,
                started_at: None,
                token_estimate: 0,
                last_active: facts.last_active,
                transcript_path: transcript_path.map(|p| p.to_string_lossy().to_string()),
                alert: alert_for(&facts),
            });
        }
        Ok(statuses)
    }

    /// Every room this daemon hosts: the subdirectories of `rooms/` that carry a spec.
    ///
    /// A directory without one is not a room - it is a half-created or hand-made directory - and listing it
    /// would turn every caller of the session list into a refusal about somebody else's junk.
    pub fn hosted_rooms(&self) -> Vec<String> {
        let rooms_dir = self.base_state_dir.join("rooms");
        let mut rooms = Vec::new();
        if let Ok(entries) = fs::read_dir(&rooms_dir) {
            for entry in entries.flatten() {
                if entry.path().join("spec.json").is_file() {
                    rooms.push(entry.file_name().to_string_lossy().to_string());
                }
            }
        }
        rooms.sort();
        rooms
    }

    /// The session list: one entry per seat of `room`, or of every room this daemon hosts when `room` is
    /// `None`.
    ///
    /// IT PROJECTS `get_room_status` rather than reading the seat facts again, for the reason the activity
    /// producer exists: two readers of one fact must be one derivation, or they drift while both look
    /// right. Every field here is copied from the status or reduced by the single rule the client already
    /// applies (`running` iff running), so there is no second place for a seat's liveness to be decided.
    pub fn list_sessions(&self, room: Option<&str>) -> Result<Vec<SessionEntry>, String> {
        let rooms = match room {
            Some(named) => vec![named.to_string()],
            None => self.hosted_rooms(),
        };
        let mut out = Vec::new();
        for name in rooms {
            // EVERY REFUSAL FROM THIS SURFACE NAMES ITS ROOM. `get_room_status`'s own sentence is a contract
            // other readers already pin ("cannot read room spec: ..."), so it stays as it is - but a caller of
            // the SESSION LIST that named a room this daemon does not host has to be told which room it was,
            // or the answer is unusable to the operator who typed it.
            if !self.room_dir(&name).join("spec.json").is_file() {
                return Err(format!(
                    "no room named {name:?} in this daemon (it hosts {})",
                    if self.hosted_rooms().is_empty() {
                        "no rooms".to_string()
                    } else {
                        self.hosted_rooms().join(", ")
                    }
                ));
            }
            let statuses = self
                .get_room_status(&name)
                .map_err(|e| format!("room {name:?}: {e}"))?;
            for status in statuses {
                out.push(SessionEntry {
                    canonical_session_name: Self::canonical_session_name(&name, &status.seat),
                    session_name: status.session_name.clone(),
                    room_name: name.clone(),
                    logical_id: Self::logical_id(&name, &status.seat),
                    session_status: match status.state {
                        SeatLifecycleState::Running => "running".to_string(),
                        _ => "stopped".to_string(),
                    },
                    agent_activity: SessionActivity {
                        state: status.activity.as_str().to_string(),
                        source: status.activity_source.clone(),
                    },
                    runtime: status.runtime.clone(),
                    model: status.model.clone(),
                    transcript_path: status.transcript_path.clone(),
                    detail: status.detail.clone(),
                });
            }
        }
        Ok(out)
    }

    /// Resolve a session name to the room and seat it names, accepting EVERY name the session list serves:
    /// the canonical `<room>-<seat>@<room>`, the runtime's `agenthub--<room>--<seat>`, the `<room>.<seat>`
    /// logical id, and a bare seat key.
    ///
    /// A bare key that names seats in more than one room is refused BY NAME rather than resolved to
    /// whichever room happened to be read first: the caller asked for one seat, and guessing which one
    /// would answer a different question than the one asked.
    pub fn resolve_seat(&self, session: &str) -> Result<(String, String), String> {
        let query = session.trim();
        if query.is_empty() {
            return Err("no session name was given".to_string());
        }
        let rooms = self.hosted_rooms();
        let mut hits: Vec<(String, String)> = Vec::new();
        for room in &rooms {
            let spec_file = self.room_dir(room).join("spec.json");
            let content = fs::read_to_string(&spec_file)
                .map_err(|e| format!("cannot read room spec for {room}: {e}"))?;
            let spec: RoomSpec = serde_json::from_str(&content)
                .map_err(|e| format!("parse spec for {room}: {e}"))?;
            for seat in spec.seats {
                let names = [
                    Self::canonical_session_name(room, &seat.seat_key),
                    Self::session_name(room, &seat.seat_key),
                    Self::logical_id(room, &seat.seat_key),
                    seat.seat_key.clone(),
                ];
                if names.iter().any(|name| name == query) {
                    hits.push((room.clone(), seat.seat_key));
                }
            }
        }
        match hits.len() {
            0 => Err(format!(
                "no seat named {query:?} in any room this daemon hosts ({})",
                if rooms.is_empty() {
                    "it hosts no rooms".to_string()
                } else {
                    rooms.join(", ")
                }
            )),
            1 => Ok(hits.remove(0)),
            _ => Err(format!(
                "{query:?} names a seat in more than one room ({}); use a name that carries its room",
                hits.iter()
                    .map(|(room, seat)| format!("{room}/{seat}"))
                    .collect::<Vec<_>>()
                    .join(", ")
            )),
        }
    }

    /// Deliver one message to a seat as ONE message.
    ///
    /// A runner seat's pane input is the runner's own line reader, so a multi-line paste would arrive as one
    /// prompt per line: the envelope's `From:` line alone would be the first thing the agent is asked. Its
    /// mailbox `text` verb carries the whole text in one frame and answers once the runner relayed it. The
    /// runtimes with no runner own their pane's input (a TUI), where the bracketed tmux paste is the delivery.
    ///
    /// THE CALLER'S ORIGIN TRAVELS WITH THE FRAME (`from`, `verb`), because the runner is where a text can end
    /// up held or refused, and the response that reports that carries an id and nothing else: without the
    /// origin here a seat's runner cannot name whose text it could not deliver, and the sender reads `relayed`
    /// for a message that died (hub task `c94cecfc`). A `from` the runner can address is also what lets it hand
    /// the text back rather than drop it.
    pub fn deliver_text(
        &self,
        session: &str,
        text: &str,
        origin: &crate::runner::Origin,
    ) -> Result<serde_json::Value, String> {
        let (room, seat) = self.resolve_seat(session)?;
        // A PARKED SEAT IS NOT HANDED WORK, and this is the tier every caller crosses: `SendText`, this
        // daemon's own `send` command, and any future caller. The client refuses its own routes first, with a
        // friendlier sentence; this one is the enforcement, and it cannot be forgotten by a caller above it.
        crate::park::refuse(self.state_dir(), &room, &seat, "Nothing was sent")?;
        let canonical = Self::session_name(&room, &seat);
        if !self.seat_runtime(&room, &seat)?.has_runner() {
            let outcome = TerminalManager::send_text(&canonical, text)?;
            return Ok(serde_json::json!({"delivered": canonical, "outcome": outcome}));
        }
        let id = format!(
            "send-{}-{}",
            std::process::id(),
            Utc::now().timestamp_millis()
        );
        let mut frame = serde_json::json!({"id": id, "type": "text", "text": text});
        if let Some(from) = &origin.from {
            frame["from"] = serde_json::Value::String(from.clone());
        }
        if let Some(verb) = &origin.verb {
            frame["verb"] = serde_json::Value::String(verb.clone());
        }
        let answer = crate::runner::send_to_seat(
            &self.base_state_dir,
            &canonical,
            &frame,
            Some((text.as_bytes(), true)),
            true,
        )?;
        Ok(serde_json::json!({"delivered": canonical, "outcome": "relayed", "runner": answer}))
    }

    fn seat_runtime(&self, room: &str, seat: &str) -> Result<AgentRuntime, String> {
        let spec_file = self.room_dir(room).join("spec.json");
        let content = fs::read_to_string(&spec_file)
            .map_err(|e| format!("cannot read room spec for {room}: {e}"))?;
        let spec: RoomSpec =
            serde_json::from_str(&content).map_err(|e| format!("parse spec for {room}: {e}"))?;
        spec.seats
            .iter()
            .find(|s| s.seat_key == seat)
            .map(|s| AgentRuntime::from_str_loose(&s.runtime))
            .ok_or_else(|| format!("room {room} has no seat {seat}"))
    }

    /// A seat's transcript: the file its runner publishes, read whole or as its newest `lines` lines.
    ///
    /// IT REFUSES BY NAME rather than answering with an empty string. A seat whose runner has not created
    /// its log yet is a DIFFERENT fact from a seat whose log is empty, and a caller that cannot tell them
    /// apart uploads "no transcript" as "nothing happened".
    pub fn read_transcript(
        &self,
        session: &str,
        lines: Option<usize>,
    ) -> Result<TranscriptAnswer, String> {
        let (room, seat) = self.resolve_seat(session)?;
        let canonical = Self::canonical_session_name(&room, &seat);
        let Some(path) = seat_transcript_path(&self.room_dir(&room), &seat) else {
            return Err(format!(
                "no transcript file for {canonical:?}: the agent has written no session file under {}",
                seat_sessions_dir(&self.room_dir(&room), &seat).display()
            ));
        };
        let content = fs::read_to_string(&path)
            .map_err(|e| format!("cannot read transcript {}: {e}", path.display()))?;
        let content = match lines {
            Some(n) => tail_lines(&content, n),
            None => content,
        };
        Ok(TranscriptAnswer {
            session: canonical,
            path: path.to_string_lossy().to_string(),
            content,
        })
    }

    /// A seat's pane, captured through the same `TerminalManager` every other tmux call in this crate uses.
    ///
    /// A pane that is not up is refused by name BEFORE tmux is asked, so the caller is told the seat has no
    /// live pane - which is the fact - rather than tmux's own message about a session it cannot find.
    pub fn capture_session(
        &self,
        session: &str,
        lines: Option<usize>,
    ) -> Result<CaptureAnswer, String> {
        let (room, seat) = self.resolve_seat(session)?;
        let canonical = Self::canonical_session_name(&room, &seat);
        let vm_session = Self::session_name(&room, &seat);
        if !TerminalManager::is_session_alive(&vm_session) {
            return Err(format!(
                "no live pane named {canonical:?}: this daemon has no running session {vm_session:?}"
            ));
        }
        let content =
            TerminalManager::capture_pane(&vm_session, lines.unwrap_or(DEFAULT_CAPTURE_LINES))?;
        Ok(CaptureAnswer {
            session: canonical,
            content,
        })
    }

    /// Everything one seat's status row is built from, read once: whether its pane has a process behind it
    /// (`pane_is_live`, not the session's existence), the runner's
    /// own last published word and freshness stamp, and the active rail rows addressed to it.
    ///
    /// It exists so the two readers that need this join - `get_room_status` (the human-facing alert) and
    /// `pending_nudges` (the nudge pass) - share one implementation of it. A second copy of the join is
    /// how a status that says "work is waiting" and a nudge pass that says "nothing to do" come to
    /// disagree while both look right.
    ///
    /// It cannot fail, and that is the point: the alert is ADVISORY, so a rail that cannot be read costs
    /// the alert and nothing else. A status that refused to answer because an advisory about a *different*
    /// fact could not be computed would trade the freshness signal for an outage - and the seat's own
    /// liveness and stamp are read from a local file that has nothing to do with the rail. The failure is
    /// not swallowed silently: it is logged, and the seat carries no alert rather than a false empty one.
    /// (`get_room_status` can still fail on the one thing it cannot answer without: an unreadable spec.)
    fn seat_facts(&self, room: &str, seat: &str) -> SeatFacts {
        let session = Self::session_name(room, seat);
        // THE SESSION IS NOT THE SEAT, and `has-session` was the wrong half of the pane to ask. A pane kept by
        // `remain-on-exit` after its command exited still answers `has-session` 0 - measured 2026-10-17 in
        // `pane_is_live`'s own comment - so a seat with NO PROCESS BEHIND IT read `running` (measured again
        // 2026-10-20T23:09: `running ... quiet_seconds=1127`, and `heal --check` printed `clean` on it), while
        // the client's nudge pass had to keep its own `pane_is_live` call to notice. The status row is the one
        // answer every reader shares, so the death is observed HERE, once, and both readers inherit it. A probe
        // that FAILS is still not a death: `pane_is_live` only excludes a pane tmux reports dead.
        let alive = TerminalManager::pane_is_live(&session);
        // THE OTHER HALF OF THE SAME READ: a session tmux still has while nothing is behind its pane. It is kept
        // separately because a status names the evidence it rested on, and "the pane outlived its process" and "no
        // pane at all" are two different observations (a seat that never ran is the second).
        let pane_kept = !alive && TerminalManager::is_session_alive(&session);
        let state_path = self
            .room_dir(room)
            .join("run")
            .join(format!("{seat}.state.json"));
        let published = fs::read_to_string(&state_path)
            .ok()
            .and_then(|t| serde_json::from_str::<serde_json::Value>(&t).ok());
        // THE CLAIM AND ITS AUTHOR, read together out of one document: the word decides what the status may say,
        // and the pid decides whether anything is left standing behind it (see `observed_death` below).
        let (published_state, publisher_pid) = published_claim(published.as_ref());
        let publisher_alive = publisher_pid.and_then(process_is_alive);
        // THE WORD, PARSED NOT DERIVED: the runner publishes it beside `state`, so a document without one
        // (an old-format document, or a seat that never published) is `Unknown` - never a re-derivation
        // from `state`, which would be a second mapping, and never `idle`, which would authorise a
        // reseat on the strength of a document nobody could read.
        let activity = ActivityWord::from_state_document(published.as_ref());
        // The runner's `updated_at` IS the seat's freshness: it is stamped every time the seat publishes
        // (turn ended, retry started, death seen), so it answers "when did this seat last do something".
        // The launch instant `start_seat` records is a different fact - when the pane was created - and it
        // was the only thing `last_active` ever held, which is why a seat that had been idle for hours read
        // as freshly active. A missing or unparseable stamp is `None`, not a guessed time.
        //
        // A RUNTIME THAT PUBLISHES NOTHING AFTER LAUNCH (agy) has only its own tool log: the guard appends
        // a line per tool call, so the newest one is the seat's real freshness. Reading it here rather than
        // teaching the guard to rewrite the state document keeps the word honest: the activity stays
        // `Unknown` (which never authorises a reseat), and only the stamp moves. The newer of the two wins;
        // neither log is allowed to move the stamp backwards.
        let state_stamp = published
            .as_ref()
            .and_then(|v| v.get("updated_at").and_then(|t| t.as_str()))
            .and_then(|t| DateTime::parse_from_rfc3339(t).ok())
            .map(|t| t.with_timezone(&Utc));
        let tool_stamp = last_tool_call_at(
            &self
                .room_dir(room)
                .join("seats")
                .join(seat)
                .join("tool-calls.jsonl"),
        );
        let last_active = match (state_stamp, tool_stamp) {
            (Some(state), Some(tool)) => Some(state.max(tool)),
            (state, tool) => state.or(tool),
        };

        let waiting_qitem_ids = match self.waiting_rows(room, &session, seat) {
            Ok(ids) => ids,
            Err(e) => {
                // Loud, and the row still answers: an advisory that cannot be computed is a missing
                // advisory, never a failed status.
                warn!("rail unreadable for {room}/{seat}; status carries no alert: {e}");
                Vec::new()
            }
        };

        SeatFacts {
            alive,
            pane_kept,
            published_state,
            activity,
            last_active,
            publisher_pid,
            publisher_alive,
            waiting_qitem_ids,
        }
    }

    /// The active rows the room's rail is holding for one seat, by qitem id.
    ///
    /// `blocked` is filtered out: a row in that state waits on the row its `blocked_on` names, not on its
    /// destination, so it is part of the rail's active set (listable, claimable, closable) but never part
    /// of "work waiting for this seat". Its wake is its blocker's close.
    ///
    /// The rail's rows are written by the client's `room queue` verbs, whose `--to` is a free session
    /// string, and the two spellings in use disagree: the live room's session name is
    /// `<room>-<seat>@<room>` while this runtime derives `agenthub--<room>--<seat>`, and a row a human wrote
    /// by hand usually names the bare seat key. All three are matched, because a spelling gap here would
    /// be read as "nothing is waiting" - the one lie this join exists to prevent. The rows are selected
    /// from the room's active set rather than by an equality filter in SQL for that reason; a room's
    /// active set is small, and correctness of the match beats pushing one clause into SQLite.
    fn waiting_rows(&self, room: &str, session: &str, seat: &str) -> Result<Vec<String>, String> {
        let store = self.queue_store(room)?;
        let active = store.list(&ListFilter {
            active_only: true,
            ..Default::default()
        })?;
        let names = addressed_names(room, session, seat);
        Ok(active
            .into_iter()
            // A BLOCKED ROW IS WAITING ON ITS BLOCKER, NOT ON ITS DESTINATION. `blocked` is in the rail's
            // `ACTIVE_STATES` - it can still be listed, claimed and closed - but it is not work the seat
            // can pick up, so neither the alert nor the nudge pass may read it as such. Measured
            // 2026-10-17 (task 005e0aad): a row set blocked on a still-open gate woke its own destination
            // twice (go-dev.nudge.jsonl attempts 2 and 3 at 06:37:06Z/06:37:13Z) while nothing was
            // callable. The wake for a blocked row is its blocker's close, which the store performs
            // (`queue::unblock_dependents` returns the row to `pending` in the close's own transaction).
            .filter(|row| row.state != "blocked")
            .filter(|row| names.iter().any(|name| name == &row.destination_session))
            // A ROW THAT NAMED WHEN TO ASK AGAIN IS NOT ASKED BEFORE THEN. `wake_after_seconds` counted from
            // the row's last transition is the row's own word; the alert and the nudge pass both stand on
            // this list, so reading it here is what keeps an owner-decision row that named hours from being
            // raised (and ladder-spent) seconds after it was written. Measured 2026-10-22 (lead's review item
            // 5): three attempts inside 19s on a row only the owner could answer.
            .filter(|row| row.wake_due(Utc::now()))
            .map(|row| row.qitem_id)
            .collect())
    }

    /// Every seat the periodic nudge pass should act on: it is up, the rail is holding active rows for it,
    /// and it has not said that a turn is in flight.
    ///
    /// THE WORD DECIDES, NOT THE DOCUMENT'S EXISTENCE, and only a word its runtime could publish counts.
    /// `Idle` is the runner's "come in"; `Unknown` is "nobody has said anything", which is not occupancy -
    /// no document at all, a document without the word, or a document left by an earlier runtime (a seat
    /// reseated from omp to agy keeps the old runner's death record on disk forever). `Working` and
    /// `NeedsInput` are the two states a nudge must not interrupt. Measured 2026-10-17 (task 022666d6): a
    /// gate row addressed to an idle agy seat earned NO attempt for six minutes, because the old gate
    /// compared the raw `state` to `idle` and the only document was the previous omp generation's
    /// `agent_socket_closed` - a seat silenced by its predecessor's gravestone.
    ///
    /// A runtime with no runner (`agy`, `codex`, a custom command) never publishes this document, so for
    /// those seats no word is authoritative: the pane is the only channel, and its liveness (below) is the
    /// proof the status already rests on.
    ///
    /// The rooms are read from disk rather than from `active_rooms`, and that is the load-bearing choice:
    /// the rail is disk-scoped (a queue verb answers for any room directory), and a daemon that has just
    /// restarted owns no active room while its seats are still up in tmux with rows waiting for them.
    /// Reading the disk is what makes the nudge survive a daemon restart; `active_rooms` would make it a
    /// one-shot that dies with the process that started the room.
    pub fn pending_nudges(&self) -> Vec<PendingNudge> {
        let rooms_dir = self.base_state_dir.join("rooms");
        let mut out = Vec::new();
        let Ok(entries) = fs::read_dir(&rooms_dir) else {
            return out;
        };
        for entry in entries.flatten() {
            if !entry.path().is_dir() {
                continue;
            }
            let room = entry.file_name().to_string_lossy().to_string();
            // A room whose spec or rail cannot be read is skipped, not guessed at: this pass only *acts*,
            // and an action taken on an unread room would be an action on unknown facts.
            let Ok(statuses) = self.get_room_status(&room) else {
                continue;
            };
            for status in statuses {
                let Some(alert) = status.alert else {
                    continue;
                };
                // ONE DERIVATION: the status row carries the word the runner published beside its state
                // (`ActivityWord`), so this pass does not read the file a second time. A second reader is
                // how the status came to say "work is waiting" while the pass found "nothing to do".
                let publishes = AgentRuntime::from_str_loose(&status.runtime).has_runner();
                if publishes {
                    // A PUBLISHING SEAT'S OWN WORD IS THE AUTHORITY: a state that is not `Running` means the
                    // generation that wrote it is done, and a nudge would talk past a dead pipe.
                    if status.state != SeatLifecycleState::Running {
                        continue;
                    }
                    let busy = matches!(
                        status.activity,
                        ActivityWord::Working | ActivityWord::NeedsInput
                    );
                    if busy {
                        continue;
                    }
                } else if !TerminalManager::pane_is_live(&status.session_name) {
                    // A SEAT WITH NO RUNNER PUBLISHES NOTHING, so its state document is either absent or a
                    // PREVIOUS generation's word. Measured 2026-10-17: the agy reviewer's
                    // `agent_socket_closed` was written at 04:00:36Z, hours before the 07:02:33Z pane that
                    // carried the live seat, and a gate on that word silenced the seat for good while a
                    // direct send (which types into the pane) worked. The pane is the proof this runtime can
                    // take: the session exists and a process is attached to it. The alert above is still the
                    // trigger, so a seat that is owed nothing stays quiet, and the ladder below still bounds
                    // a pane that exists with a dead process.
                    continue;
                }
                out.push(PendingNudge {
                    room: room.clone(),
                    seat: status.seat,
                    session_name: status.session_name,
                    has_runner: publishes,
                    qitem_ids: alert.waiting_qitem_ids,
                });
            }
        }
        out
    }

    /// Generate the exact `whoami` payload for an active seat session (Rule 1 parity).
    pub fn resolve_whoami(
        &self,
        query_session: Option<&str>,
        _pid: Option<u32>,
    ) -> Result<WhoamiResponse, String> {
        let rooms_dir = self.base_state_dir.join("rooms");
        let _ = fs::create_dir_all(&rooms_dir);

        // First, check active rooms on disk
        if let Ok(entries) = fs::read_dir(&rooms_dir) {
            for entry in entries.flatten() {
                let spec_path = entry.path().join("spec.json");
                if let Ok(content) = fs::read_to_string(&spec_path) {
                    if let Ok(spec) = serde_json::from_str::<RoomSpec>(&content) {
                        for seat in &spec.seats {
                            let session = Self::session_name(&spec.room, &seat.seat_key);
                            let matches = match query_session {
                                Some(s) => s == session || s == seat.seat_key,
                                None => true, // default to first match if only 1 seat
                            };

                            if matches {
                                // Found seat! Construct WhoamiResponse
                                let peers: Vec<WhoamiPeer> = spec
                                    .seats
                                    .iter()
                                    .filter(|s| s.seat_key != seat.seat_key)
                                    .map(|s| WhoamiPeer {
                                        logical_id: s.seat_key.clone(),
                                        session_name: Self::session_name(&spec.room, &s.seat_key),
                                        runtime: s.runtime.clone(),
                                    })
                                    .collect();

                                let mut outgoing = Vec::new();
                                let mut incoming = Vec::new();

                                for link in &spec.links {
                                    if link.from == seat.seat_key {
                                        outgoing.push(WhoamiEdgeTarget {
                                            kind: link.kind.clone(),
                                            to: Some(WhoamiEndpoint {
                                                logical_id: link.to.clone(),
                                                session_name: Self::session_name(
                                                    &spec.room, &link.to,
                                                ),
                                            }),
                                            from: None,
                                        });
                                    }
                                    if link.to == seat.seat_key {
                                        incoming.push(WhoamiEdgeTarget {
                                            kind: link.kind.clone(),
                                            to: None,
                                            from: Some(WhoamiEndpoint {
                                                logical_id: link.from.clone(),
                                                session_name: Self::session_name(
                                                    &spec.room, &link.from,
                                                ),
                                            }),
                                        });
                                    }
                                }

                                let transcript_str =
                                    seat_transcript_path(&entry.path(), &seat.seat_key)
                                        .map(|p| p.to_string_lossy().to_string());

                                return Ok(WhoamiResponse {
                                    resolved_by: "session_name".into(),
                                    identity: WhoamiIdentity {
                                        room_name: spec.room.clone(),
                                        member_id: seat.seat_key.clone(),
                                        runtime: seat.runtime.clone(),
                                        session_name: session.clone(),
                                        logical_id: seat.seat_key.clone(),
                                        role: seat.seat_type.clone(),
                                        model: seat.model.clone(),
                                    },
                                    peers,
                                    edges: WhoamiEdges { outgoing, incoming },
                                    transcript: WhoamiTranscript {
                                        enabled: true,
                                        tail_command: transcript_str
                                            .as_ref()
                                            .map(|p| format!("tail -f {p}")),
                                        path: transcript_str,
                                    },
                                });
                            }
                        }
                    }
                }
            }
        }

        // Fallback to environment variables
        let room = std::env::var("AGENTHUB_ROOM").ok();
        let seat = std::env::var("AGENTHUB_SEAT").ok();
        let role = std::env::var("AGENTHUB_ROLE").unwrap_or_else(|_| "seat".into());
        let runtime = std::env::var("AGENTHUB_RUNTIME").unwrap_or_else(|_| "omp".into());
        let model = std::env::var("AGENTHUB_MODEL").ok();

        if let (Some(r), Some(s)) = (room, seat) {
            let session = format!("agenthub--{r}--{s}");
            return Ok(WhoamiResponse {
                resolved_by: "environment".into(),
                identity: WhoamiIdentity {
                    room_name: r,
                    member_id: s.clone(),
                    runtime,
                    session_name: session,
                    logical_id: s,
                    role,
                    model,
                },
                peers: vec![],
                edges: WhoamiEdges {
                    outgoing: vec![],
                    incoming: vec![],
                },
                transcript: WhoamiTranscript {
                    enabled: true,
                    path: None,
                    tail_command: None,
                },
            });
        }

        Err("seat identity could not be resolved from active rooms".into())
    }
}

/// The directory the agent is launched with (`--session-dir`) and writes its session files into.
fn seat_sessions_dir(room_dir: &Path, seat: &str) -> PathBuf {
    room_dir.join("seats").join(seat).join("sessions")
}

/// The directories a seat may write: the one the room spec names for it, then its own worktrees under the room's
/// `wt/`. The FIRST entry is where the seat starts and what a relative path in a tool call resolves against, and
/// the whole list is what the guard is handed (`AGENTHUB_WORKTREES`).
///
/// The runtime owns the room layout - `seats/`, `logs/`, `run/` - so it owns this too: a seat holds one worktree
/// per repo it edits, `<room>/wt/<seat>` and `<room>/wt/<seat>-<repo>`, and another seat's worktree and the
/// checkout every seat on this host shares are not among them.
///
/// A seat with no directory of its own is REFUSED rather than started somewhere convenient. The resolution this
/// replaces ended in `.` - the daemon's own directory, the checkout every seat on this host shares - so a room
/// that named no `cwd` gave each of its seats write scope over it (the architect's finding, 2026-10-16: five omp
/// seats rooted there, and a seat's file landed in the shared checkout while its own worktree stayed clean).
/// Naming the seat's directory is the room spec's job; a spec that does not is a room that cannot start a seat,
/// and the refusal says which spec to write.
pub fn seat_scope(
    seat: &SeatSpec,
    spec: &RoomSpec,
    room_dir: &Path,
) -> Result<Vec<PathBuf>, String> {
    let mut scope: Vec<PathBuf> = Vec::new();
    if let Some(dir) = seat.cwd.as_deref().or(spec.cwd.as_deref()) {
        let path = PathBuf::from(dir);
        // A relative directory resolves against the daemon's own, which is exactly the base this fix removes:
        // the room would be naming a directory only the machine that started the daemon can predict.
        if !path.is_absolute() {
            return Err(format!(
                "the directory '{dir}' named for seat '{}' is not absolute: a relative one resolves against the \
                 daemon's own directory, which is not what a room means",
                seat.seat_key
            ));
        }
        scope.push(canonical_dir(&path)?);
    }
    for worktree in own_worktrees(room_dir, &seat.seat_key) {
        if !scope.contains(&worktree) {
            scope.push(worktree);
        }
    }
    if scope.is_empty() {
        return Err(format!(
            "seat '{}' has no directory of its own: name one for it in the room's spec (`cwd`, in team.json or \
             spec.json) or give the room its worktree at {}",
            seat.seat_key,
            room_dir.join("wt").join(&seat.seat_key).display()
        ));
    }
    Ok(scope)
}

/// A room's seat keys, checked for the one thing the lane rule cannot tolerate: a key that EQUALS another or is
/// its `-`-prefix (`go` and `go-dev`). The guard admits `<wt>/<seat>` and `<wt>/<seat>-*` by name, so a prefix
/// pair would give one seat the other's directory; the refusal is the only place both names are known.
fn seat_key_collision(spec: &RoomSpec) -> Option<String> {
    for (i, a) in spec.seats.iter().enumerate() {
        for b in spec.seats.iter().skip(i + 1) {
            if a.seat_key == b.seat_key {
                return Some(format!(
                    "the room names seat '{}' twice: one seat key, one directory, one guard scope - rename one",
                    a.seat_key
                ));
            }
            let (short, long) = if a.seat_key.len() <= b.seat_key.len() {
                (&a.seat_key, &b.seat_key)
            } else {
                (&b.seat_key, &a.seat_key)
            };
            if long.starts_with(&format!("{short}-")) {
                return Some(format!(
                    "seat key '{short}' is a prefix of '{long}', and a lane is admitted by that prefix under the \
                     room's wt/: '<wt>/{short}-<name>' and '<wt>/{long}' would name one directory. Rename one of \
                     the two seats"
                ));
            }
        }
    }
    None
}

/// A seat's own worktrees under the room's `wt/`: `<seat>` and `<seat>-<repo>`, the exact name first, then the
/// rest by name. A room that holds no `wt/` yet (or none for this seat) has none, and says so with an empty list.
fn own_worktrees(room_dir: &Path, seat: &str) -> Vec<PathBuf> {
    let Ok(entries) = fs::read_dir(room_dir.join("wt")) else {
        return Vec::new();
    };
    let mut named: Vec<String> = entries
        .flatten()
        .filter(|e| e.path().is_dir())
        .filter_map(|e| e.file_name().into_string().ok())
        .filter(|name| name == seat || name.starts_with(&format!("{seat}-")))
        .collect();
    // `<seat>` sorts before every `<seat>-<repo>`: `-` (0x2D) is below every name character a repo key can
    // start with, so the sort IS the precedence - the seat's own room worktree first, its other repos after.
    named.sort();
    named
        .into_iter()
        .filter_map(|name| canonical_dir(&room_dir.join("wt").join(name)).ok())
        .collect()
}

/// The absolute, symlink-free form of a directory that must already exist: every path a seat is given is one the
/// runtime hands to tmux, to `/proc/<pid>/cwd` and to the guard, and all three must name the same directory.
fn canonical_dir(path: &Path) -> Result<PathBuf, String> {
    path.canonicalize().map_err(|e| {
        format!(
            "cannot resolve the seat's directory {}: {e}",
            path.display()
        )
    })
}

/// THE ONE DEFINITION OF A SEAT'S TRANSCRIPT: the newest `*.jsonl` the agent wrote into its sessions dir
/// (`<UTC-timestamp>_<uuid>.jsonl`, so the newest sorts last by name). Resolved when asked, so a restart
/// that starts a new session is followed; `None` while the agent has written no session file.
fn seat_transcript_path(room_dir: &Path, seat: &str) -> Option<PathBuf> {
    fs::read_dir(seat_sessions_dir(room_dir, seat))
        .ok()?
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.is_file() && p.extension().is_some_and(|x| x == "jsonl"))
        .max_by(|a, b| a.file_name().cmp(&b.file_name()))
}

/// The newest `lines` lines of `text`, terminators kept, or all of it when it has fewer. A trailing newline
/// is not an extra line, and each returned line keeps its own terminator so a reader that appends does not
/// glue two lines together. A JSONL transcript is a file of lines, so this is a LINE window and not a byte
/// window: a reader that counted bytes would split a record.
fn tail_lines(text: &str, lines: usize) -> String {
    if lines == 0 {
        return String::new();
    }
    let all: Vec<&str> = text.split_inclusive('\n').collect();
    if all.len() <= lines {
        return text.to_string();
    }
    all[all.len() - lines..].concat()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::queue::{CloseInput, CreateInput};

    /// The readiness proof is a file from THIS generation, not an artifact. The measured false green: a restart
    /// in the same state dir returned running in 0.03 s on the previous generation's `agent_socket_closed`
    /// record, which was older than the new pane. Each arm states one fact, so a failure names which half broke.
    #[test]
    fn readiness_needs_a_file_from_this_generation_and_not_a_death() {
        let dir = scratch("readiness");
        let path = dir.join("solo.state.json");

        let write = |state: &str| {
            let payload = serde_json::json!({
                "state": state,
                "classification": "",
                "resume_attempts": 0,
                "updated_at": chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
            });
            fs::write(&path, serde_json::to_string(&payload).unwrap()).unwrap();
        };

        // The file's age is set by the launch instant handed in, not by touching the file: a launch AFTER the
        // file is the inherited case, a launch BEFORE it is this generation's.
        let after_the_file = std::time::SystemTime::now() + Duration::from_secs(60);
        let before_the_file = std::time::SystemTime::now() - Duration::from_secs(60);

        let _ = fs::remove_file(&path);
        assert!(
            !readiness_written_by(&path, before_the_file),
            "a missing file is not a handshake"
        );

        write("agent_socket_closed");
        assert!(
            !readiness_written_by(&path, after_the_file),
            "an inherited death record is not a handshake, however fresh the rename was"
        );
        write("agent_exited");
        assert!(
            !readiness_written_by(&path, after_the_file),
            "another death state is not a handshake"
        );
        write("idle");
        assert!(
            !readiness_written_by(&path, after_the_file),
            "a live state older than the launch is the previous generation's word"
        );
        assert!(
            readiness_written_by(&path, before_the_file),
            "a live state written after the launch IS the handshake"
        );

        write("running");
        assert!(
            readiness_written_by(&path, before_the_file),
            "running is a handshake too"
        );

        let _ = fs::remove_dir_all(&dir);
    }

    /// THE FAST-FAIL THAT MAKES A LONGER WINDOW SAFE: a death the seat itself recorded, after this launch, ends
    /// the wait where it is written instead of at the deadline.
    ///
    /// The window grew to 60 s to cover a first-ever launch in a fresh cache. Widening alone would have bought
    /// that with false patience: a pane that outlives its agent - the case liveness cannot see - would have sat
    /// unreported for a full minute. Measured live before this clause, on the 20 s build: rc=3 at 20.14 s with
    /// the staged death record untouched. Each arm is one fact.
    #[test]
    fn a_death_recorded_by_this_generation_is_reported_where_it_is_written() {
        let dir = scratch("fresh-death");
        let path = dir.join("solo.state.json");

        let write = |state: &str| {
            let payload = serde_json::json!({
                "state": state,
                "classification": "",
                "resume_attempts": 0,
                "updated_at": chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
            });
            fs::write(&path, serde_json::to_string(&payload).unwrap()).unwrap();
        };

        // The launch instant handed in decides the generation, exactly as in the readiness test above.
        let after_the_file = std::time::SystemTime::now() + Duration::from_secs(60);
        let before_the_file = std::time::SystemTime::now() - Duration::from_secs(60);

        let _ = fs::remove_file(&path);
        assert_eq!(
            fresh_death_state(&path, before_the_file),
            None,
            "no file is not a recorded death"
        );

        write("agent_socket_closed");
        assert_eq!(
            fresh_death_state(&path, after_the_file),
            None,
            "a death from an earlier generation is not this launch's news"
        );
        assert_eq!(
            fresh_death_state(&path, before_the_file),
            Some("agent_socket_closed".to_string()),
            "a death written after the launch IS this launch's news"
        );

        write("agent_exited");
        assert_eq!(
            fresh_death_state(&path, before_the_file),
            Some("agent_exited".to_string()),
            "every state in the shared list counts"
        );

        write("idle");
        assert_eq!(
            fresh_death_state(&path, before_the_file),
            None,
            "a live state is not a death, however fresh"
        );

        let _ = fs::remove_dir_all(&dir);
    }

    /// THE REFUSAL MUST SAY WHICH WORLD THE SEAT IS IN. Two different worlds - nothing on disk, or a record
    /// from an earlier generation - and the message used to describe only the first, printing "(no <path>)"
    /// while a stale record sat there. A live run cannot reach the second world reliably (the runner tears the
    /// pane down before the window ends), which is exactly why this is pinned here.
    #[test]
    fn the_deadline_refusal_names_the_document_it_is_missing() {
        let dir = scratch("refusal");
        let path = dir.join("solo.state.json");
        let write = |state: &str| {
            let payload = serde_json::json!({
                "state": state,
                "classification": "",
                "resume_attempts": 0,
                "updated_at": chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
            });
            fs::write(&path, serde_json::to_string(&payload).unwrap()).unwrap();
        };
        let after_the_file = std::time::SystemTime::now() + Duration::from_secs(60);
        let before_the_file = std::time::SystemTime::now() - Duration::from_secs(60);

        // World one: nothing on disk. The refusal must not claim a record exists.
        let _ = fs::remove_file(&path);
        let absent = readiness_refusal("leg", "solo", &path, before_the_file);
        assert!(
            absent.contains("no readiness document from this generation"),
            "the absent case must say the document is missing, got: {absent}"
        );

        // World two: a stale record is RIGHT THERE. The refusal must name it, not pretend the path is empty.
        write("agent_socket_closed");
        let stale = readiness_refusal("leg", "solo", &path, after_the_file);
        assert!(
            stale.contains("stale `agent_socket_closed` from an earlier generation"),
            "the stale case must name the record it ignored, got: {stale}"
        );
        assert!(
            !stale.contains("no readiness document"),
            "and must not tell a reader looking at that file that nothing is there, got: {stale}"
        );

        // A live record written after the launch is not a refusal case at all: the loop breaks before this.
        write("idle");
        let fresh = readiness_refusal("leg", "solo", &path, before_the_file);
        assert!(
            fresh.contains("no readiness document from this generation"),
            "a fresh record is 'waiting', not 'stale', got: {fresh}"
        );

        let _ = fs::remove_dir_all(&dir);
    }

    /// THE RUNTIME REPORTS THE ACTIVITY WORD - the producer's whole purpose. A seat's published state document
    /// is served as `activity` in the daemon's vocabulary, with the runner's own word beside it as
    /// `activity_source` so a reader sees the evidence instead of trusting the derivation; and a seat that
    /// published nothing is `unknown`, never `idle`, because a document nobody can read must not authorise a
    /// reseat. One document per word, written by the helper the other status tests use.
    #[tokio::test]
    async fn the_status_serves_the_activity_word_the_seat_published() {
        let state_dir = scratch("activity-status");
        let room = "activity-room";
        let seat = seat_key("activity");
        write_spec(&state_dir, room, &seat);
        let supervisor = supervisor_at(&state_dir);

        for (state, word) in [
            ("idle", "idle"),
            ("needs_input", "needs_input"),
            ("retrying", "working"),
            ("blocked", "working"),
            ("agent_exited", "unknown"),
        ] {
            write_state_file(&state_dir, room, &seat, state, "2026-10-14T01:00:00.000Z");

            let statuses = supervisor.get_room_status(room).expect("status");
            let row = statuses.iter().find(|s| s.seat == seat).expect("the seat");

            assert_eq!(
                row.activity.as_str(),
                word,
                "the runner published {state:?}"
            );
            assert_eq!(
                row.activity_source.as_deref(),
                Some(state),
                "the source word travels beside the word, so a reader sees the evidence"
            );
        }
    }

    /// THE SESSION LIST IS THE CLIENT'S SHAPE, not the runtime's own. Every key the client's one parser
    /// reads (`clientsync.parseRoomPsSessions`) is present and spelled as the CLI spells it - an entry that
    /// parser cannot read is a session the cloud never sees while the runtime still looks healthy.
    #[tokio::test]
    async fn the_session_list_serves_the_keys_the_clients_parser_reads() {
        let state_dir = scratch("session-list");
        let room = "list-room";
        let seat = seat_key("list");
        write_spec(&state_dir, room, &seat);
        write_state_file(&state_dir, room, &seat, "idle", "2026-10-14T01:00:00.000Z");
        let supervisor = supervisor_at(&state_dir);

        let entries = supervisor.list_sessions(Some(room)).expect("list");
        assert_eq!(entries.len(), 1, "one entry per seat of the room");
        let entry = &entries[0];

        assert_eq!(
            entry.canonical_session_name,
            RoomSupervisor::canonical_session_name(room, &seat)
        );
        assert_eq!(
            entry.session_name,
            RoomSupervisor::session_name(room, &seat)
        );
        assert_eq!(
            entry.room_name, room,
            "the room IS the room for a native room"
        );
        assert_eq!(
            entry.logical_id,
            format!("{room}.{seat}"),
            "the client takes the member after the FIRST dot, so the dot has to be there"
        );
        assert_eq!(
            entry.agent_activity.state, "idle",
            "the word the seat published"
        );
        assert_eq!(
            entry.agent_activity.source.as_deref(),
            Some("idle"),
            "and the word it was derived from, so a reader sees the evidence"
        );
        assert_eq!(
            entry.session_status, "stopped",
            "this test creates no pane, and the reduction the client applies is the one served"
        );

        // THE CONTRACT IS THE WIRE SPELLING, not the struct's field names: renaming a Rust field while its
        // serde rename holds is correct, and renaming the rename is the defect this test exists for.
        let encoded = serde_json::to_value(entry).expect("encode");
        for key in [
            "canonicalSessionName",
            "sessionName",
            "roomName",
            "logicalId",
            "sessionStatus",
            "agentActivity",
        ] {
            assert!(
                encoded.get(key).is_some(),
                "the wire must carry {key}: {encoded}"
            );
        }
        assert!(
            encoded["agentActivity"].get("state").is_some(),
            "the activity nests under agentActivity.state, where the node parser looks: {encoded}"
        );

        let _ = fs::remove_dir_all(&state_dir);
    }

    /// AN EMPTY ROOM MEANS EVERY ROOM THIS DAEMON HOSTS, and a room it does not host is refused by name.
    /// The two are different answers; an empty list for an unknown room would erase the difference.
    #[tokio::test]
    async fn the_session_list_covers_every_hosted_room_and_refuses_one_it_does_not_host() {
        let state_dir = scratch("session-rooms");
        let room_a = "rooms-a";
        let room_b = "rooms-b";
        write_spec(&state_dir, room_a, &seat_key("a"));
        write_spec(&state_dir, room_b, &seat_key("b"));
        let supervisor = supervisor_at(&state_dir);

        let all = supervisor.list_sessions(None).expect("every hosted room");
        assert_eq!(all.len(), 2, "one entry per hosted seat: {all:?}");
        assert!(
            all.iter().any(|e| e.room_name == room_a) && all.iter().any(|e| e.room_name == room_b),
            "both hosted rooms are listed: {all:?}"
        );

        let one = supervisor.list_sessions(Some(room_a)).expect("one room");
        assert_eq!(one.len(), 1, "a named room narrows the list: {one:?}");
        assert_eq!(one[0].room_name, room_a);

        let refused = supervisor
            .list_sessions(Some("ghost-room"))
            .expect_err("an unhosted room");
        assert!(
            refused.contains("ghost-room"),
            "the refusal names the room it could not read: {refused}"
        );
        assert!(
            refused.contains(room_a) && refused.contains(room_b),
            "and names the rooms it does host, so the caller sees the alternative: {refused}"
        );

        // A directory without a spec is not a room. Listing it would answer a refusal about somebody else's
        // junk inside a call that asked for sessions.
        fs::create_dir_all(state_dir.join("rooms").join("not-a-room")).expect("stray dir");
        let still = supervisor.list_sessions(None).expect("hosted rooms");
        assert_eq!(
            still.len(),
            2,
            "a spec-less directory is not a room: {still:?}"
        );

        let _ = fs::remove_dir_all(&state_dir);
    }

    /// EVERY NAME THE LIST SERVES ALSO RESOLVES, and a name that is not a seat is refused BY NAME rather
    /// than answered emptily: "there is no such seat" and "that seat's log is missing" are different facts.
    #[tokio::test]
    async fn every_served_name_resolves_and_an_unknown_one_is_refused_by_name() {
        let state_dir = scratch("session-resolve");
        let room = "resolve-room";
        let seat = seat_key("resolve");
        write_spec(&state_dir, room, &seat);
        let supervisor = supervisor_at(&state_dir);

        for name in [
            RoomSupervisor::canonical_session_name(room, &seat),
            RoomSupervisor::session_name(room, &seat),
            RoomSupervisor::logical_id(room, &seat),
            seat.clone(),
        ] {
            let (got_room, got_seat) = supervisor
                .resolve_seat(&name)
                .unwrap_or_else(|e| panic!("{name}: {e}"));
            assert_eq!(
                (got_room.as_str(), got_seat.as_str()),
                (room, seat.as_str()),
                "every name the list serves must resolve: {name}"
            );
        }

        let unknown = supervisor
            .resolve_seat("ghost-seat@nowhere")
            .expect_err("no such seat");
        assert!(
            unknown.contains("ghost-seat@nowhere"),
            "the refusal quotes what was asked for: {unknown}"
        );
        assert!(
            unknown.contains(room),
            "and names the rooms it looked in, so a reader can see the search space: {unknown}"
        );

        let empty = supervisor.resolve_seat("   ").expect_err("no name");
        assert!(
            empty.contains("no session name"),
            "an empty name is refused as such, not looked up: {empty}"
        );

        let _ = fs::remove_dir_all(&state_dir);
    }

    /// A BARE SEAT KEY IN TWO ROOMS IS REFUSED rather than resolved to whichever room was read first: the two
    /// candidates are different seats, and answering with one of them answers a different question.
    #[tokio::test]
    async fn a_bare_seat_key_in_two_rooms_is_refused_rather_than_guessed() {
        let state_dir = scratch("session-ambiguous");
        let shared = seat_key("shared");
        write_spec(&state_dir, "amb-a", &shared);
        write_spec(&state_dir, "amb-b", &shared);
        let supervisor = supervisor_at(&state_dir);

        let refused = supervisor.resolve_seat(&shared).expect_err("ambiguous");
        assert!(refused.contains("more than one room"), "{refused}");
        assert!(
            refused.contains("amb-a") && refused.contains("amb-b"),
            "both candidates are named, so the caller can qualify: {refused}"
        );

        // The qualified names stay unambiguous, which is the way out the refusal points at.
        let (room, seat) = supervisor
            .resolve_seat(&RoomSupervisor::canonical_session_name("amb-b", &shared))
            .expect("qualified");
        assert_eq!((room.as_str(), seat.as_str()), ("amb-b", shared.as_str()));

        let _ = fs::remove_dir_all(&state_dir);
    }

    /// THE TRANSCRIPT IS THE NEWEST SESSION FILE THE AGENT WROTE, and a seat with none is refused BY NAME: an
    /// empty string here would be uploaded as "nothing happened", which is the opposite fact.
    #[tokio::test]
    async fn the_transcript_reads_the_newest_agent_session_and_refuses_when_there_is_none() {
        let state_dir = scratch("session-transcript");
        let room = "transcript-room";
        let seat = seat_key("transcript");
        write_spec(&state_dir, room, &seat);
        let supervisor = supervisor_at(&state_dir);

        let absent = supervisor
            .read_transcript(&RoomSupervisor::canonical_session_name(room, &seat), None)
            .expect_err("the agent has written no session file");
        assert!(absent.contains("no transcript file"), "{absent}");
        assert!(
            absent.contains("sessions"),
            "the refusal names the directory it looked in, so a reader can check it: {absent}"
        );
        let status = supervisor.get_room_status(room).expect("status");
        assert_eq!(status[0].transcript_path, None, "no session file, no path");

        let sessions = state_dir
            .join("rooms")
            .join(room)
            .join("seats")
            .join(&seat)
            .join("sessions");
        fs::create_dir_all(&sessions).expect("sessions dir");
        fs::write(sessions.join("2026-10-14T15-43-38-600Z_aaa.jsonl"), "old\n")
            .expect("old session");
        fs::write(
            sessions.join("2026-10-14T16-00-00-000Z_bbb.jsonl"),
            "one\ntwo\nthree\n",
        )
        .expect("new session");
        fs::write(sessions.join("notes.txt"), "not a session").expect("noise");

        let whole = supervisor.read_transcript(&seat, None).expect("whole log");
        assert_eq!(whole.content, "one\ntwo\nthree\n");
        assert!(
            whole.path.ends_with("2026-10-14T16-00-00-000Z_bbb.jsonl"),
            "{}",
            whole.path
        );
        let status = supervisor.get_room_status(room).expect("status");
        assert_eq!(
            status[0].transcript_path.as_deref(),
            Some(whole.path.as_str())
        );
        assert_eq!(
            whole.session,
            RoomSupervisor::canonical_session_name(room, &seat),
            "the answer says which seat it is about"
        );

        let tail = supervisor
            .read_transcript(&seat, Some(2))
            .expect("newest two");
        assert_eq!(
            tail.content, "two\nthree\n",
            "a line window keeps whole lines and their terminators"
        );

        let _ = fs::remove_dir_all(&state_dir);
    }

    /// `lines` IS A LINE WINDOW ON A JSONL FILE: a byte window would split a record, and a reader handed
    /// half a record uploads half a message.
    #[test]
    fn tail_lines_keeps_whole_lines() {
        assert_eq!(tail_lines("a\nb\nc\n", 2), "b\nc\n");
        assert_eq!(
            tail_lines("a\nb\nc\n", 9),
            "a\nb\nc\n",
            "fewer lines than asked for is the whole file"
        );
        assert_eq!(
            tail_lines("a\nb\nc", 1),
            "c",
            "a file whose last line has no terminator still ends there"
        );
        assert_eq!(
            tail_lines("a\nb\n", 0),
            "",
            "zero is a window of nothing, not the whole file"
        );
        assert_eq!(tail_lines("", 3), "");
    }

    /// The launch names a binary that EXISTS, or it names the running inode - and when it can name neither it
    /// refuses by name. The measured failure this pins: a daemon whose installed binary was replaced while it
    /// ran handed every new pane the string `…/agenthub-runtime (deleted)`, which the pane's shell could not
    /// exec (exit 2), and the seat was reported `its pane is gone` with nothing naming the cause.
    #[test]
    fn a_launch_runs_the_path_that_exists_and_the_running_inode_when_it_does_not() {
        let dir = scratch("launch-executable");
        let installed = dir.join("agenthub-runtime");
        fs::write(&installed, b"binary").unwrap();
        let gone = dir.join("agenthub-runtime (deleted)");
        let through_proc = dir.join("proc-exe");
        fs::write(&through_proc, b"binary").unwrap();

        assert_eq!(
            launch_executable(installed.clone(), through_proc.clone()).unwrap(),
            installed,
            "a path that exists is the binary the daemon is running and is used as it is"
        );
        assert_eq!(
            launch_executable(gone.clone(), through_proc.clone()).unwrap(),
            through_proc,
            "a replaced binary's own path is a string, not a file: the running inode names it"
        );

        let missing = dir.join("also-missing");
        let err = launch_executable(gone.clone(), missing.clone())
            .expect_err("two paths that name nothing are a refusal, not a launch");
        assert!(
            err.contains(&gone.display().to_string())
                && err.contains(&missing.display().to_string()),
            "the refusal names both paths so a reader can check them: {err}"
        );
        assert!(
            err.contains("restart the daemon"),
            "and it names the remedy: {err}"
        );

        let _ = fs::remove_dir_all(&dir);
    }

    /// A scratch state dir under the system temp dir, unique per test, removed by the caller.
    fn scratch(name: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("agenthub-supervisor-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).expect("scratch dir");
        // THE SKILLS ROOT IS ISOLATED HERE, because a seat's skills tree is named in the state root's `.env`
        // and the process environment is the documented fallback - so without this the suite resolves the
        // HOST's tree (measured 2026-10-17: `AGENTHUB_CLIENT_SKILLS` from the seat's environment pointed at
        // the main checkout, and a malformed skill in it failed every start this suite performs). A test
        // names its own tree; the path need not exist, since no tree means no skills.
        fs::write(
            dir.join(".env"),
            format!("{SKILLS_ROOT_ENV}={}\n", dir.join("no-skills").display()),
        )
        .expect("isolated .env");
        dir
    }

    /// A supervisor needs a `HookBus`, whose constructor spawns its audit task, so these tests run on a
    /// tokio runtime (`#[tokio::test]`) even where nothing is awaited.
    fn supervisor_at(state_dir: &Path) -> RoomSupervisor {
        RoomSupervisor::new(
            state_dir,
            HookBus::new(state_dir.join("audit"), 8),
            state_dir.join("run").join("agenthub.sock"),
        )
    }

    /// THE ROOM'S LAYOUT IS PART OF STARTING THE ROOM, not something its first writer creates: the
    /// acceptance asserts `seats/`, `logs/` and `run/` immediately after `up`, and run 4 was starved by
    /// that single assertion because `logs/` existed nowhere until a writer happened to need it.
    ///
    /// The room here has NO SEATS, so `start_room` refuses - "started no seat (0 of 0)" is its own answer
    /// and not a silent success - and the point of the test is that the layout is already in place when it
    /// answers, exactly as the room's directory and `spec.json` are.
    #[tokio::test]
    async fn starting_a_room_creates_its_seats_logs_and_run_directories() {
        let state_dir = scratch("room-layout");
        let spec: RoomSpec = serde_json::from_value(serde_json::json!({
            "room": "layout",
            "seats": [],
            "links": []
        }))
        .expect("spec");

        let refused = supervisor_at(&state_dir)
            .start_room(spec)
            .expect_err("a room with no seats is refused rather than reported as started");
        assert!(
            refused.contains("started no seat"),
            "the refusal names what happened: {refused}"
        );

        let room_dir = state_dir.join("rooms").join("layout");
        for dir in ["seats", "logs", "run"] {
            assert!(
                room_dir.join(dir).is_dir(),
                "{dir}/ is part of the room's shape, not something its first writer creates: {}",
                room_dir.display()
            );
        }
        let _ = fs::remove_dir_all(&state_dir);
    }

    /// A MULTI-LINE MESSAGE TO A RUNNER SEAT IS ONE `text` FRAME IN ITS MAILBOX, not one pane line per line:
    /// a pane paste is read by the runner's line reader as one prompt per line, so the agent's first prompt
    /// was the envelope's `From:` line alone. Fails if delivery goes back through the pane.
    #[tokio::test]
    async fn a_multi_line_message_reaches_a_runner_seat_as_one_text_frame() {
        use std::io::{BufRead, BufReader, Write};
        use std::os::unix::net::{UnixListener, UnixStream};

        let state_dir = scratch("deliver");
        let seat = seat_key("deliver");
        write_spec(&state_dir, "deliver", &seat);
        let run_dir = state_dir.join("rooms").join("deliver").join("run");
        fs::create_dir_all(&run_dir).expect("run dir");
        let listener = UnixListener::bind(run_dir.join(format!("{seat}.sock"))).expect("mailbox");
        let runner = std::thread::spawn(move || {
            let (stream, _) = listener.accept().expect("a frame");
            let mut line = String::new();
            BufReader::new(stream)
                .read_line(&mut line)
                .expect("one line");
            let frame: serde_json::Value = serde_json::from_str(line.trim()).expect("a frame");
            let reply = frame["replyTo"].as_str().expect("replyTo").to_string();
            let mut back = UnixStream::connect(reply).expect("reply socket");
            writeln!(back, "{}", serde_json::json!({"relayed": "prompt"})).expect("reply");
            frame
        });

        let text = "From: principal@demo\nTo: dev\n---\nthe body";
        let answer = supervisor_at(&state_dir)
            .deliver_text(
                &seat,
                text,
                &crate::runner::Origin {
                    from: Some("agenthub--demo--principal".to_string()),
                    verb: Some("send".to_string()),
                },
            )
            .expect("delivered");
        let frame = runner.join().expect("runner thread");
        assert_eq!(frame["type"], "text");
        assert_eq!(frame["text"], text, "the whole message is one frame");
        // THE ORIGIN IS WHAT A HELD TEXT IS HANDED BACK WITH, and it rides the frame: the daemon stamps
        // `from`/`verb` here, and a frame that reached a runner without them would be a text a mid-turn seat
        // can be holding with nothing naming who sent it. The reviewer's mutation (2026-10-17) deleted the
        // two stamping lines and the whole suite stayed green, so these two assertions are the pin.
        assert_eq!(
            frame["from"], "agenthub--demo--principal",
            "the frame names the sender the daemon was given"
        );
        assert_eq!(
            frame["verb"], "send",
            "the frame names the verb that carried it"
        );
        assert_eq!(answer["outcome"], "relayed");
        assert_eq!(answer["runner"]["relayed"], "prompt");

        let _ = fs::remove_dir_all(&state_dir);
    }

    #[tokio::test]
    async fn stopping_a_room_announces_each_seat_it_stopped() {
        let state_dir = scratch("terminated");
        write_spec(&state_dir, "demo", "dev");
        let supervisor = supervisor_at(&state_dir);
        let mut events = supervisor.hook_bus.subscribe();
        supervisor.stop_room("demo").expect("stopped");
        match events.recv().await.expect("an event") {
            SeatEvent::SessionTerminated {
                room,
                seat,
                exit_code,
                ..
            } => {
                assert_eq!(
                    (room.as_str(), seat.as_str(), exit_code),
                    ("demo", "dev", None)
                );
            }
            other => panic!("expected SessionTerminated, got {other:?}"),
        }
        let _ = fs::remove_dir_all(&state_dir);
    }

    fn write_spec(state_dir: &Path, room: &str, seat: &str) {
        write_spec_with_runtime(state_dir, room, seat, "omp");
    }

    /// The same room, with the seat's runtime chosen: the nudge pass reads the runtime to decide the
    /// seat's delivery route and whether its word can exist at all, so a case about agy or codex must be
    /// able to say so in the spec.
    fn write_spec_with_runtime(state_dir: &Path, room: &str, seat: &str, runtime: &str) {
        let room_dir = state_dir.join("rooms").join(room);
        fs::create_dir_all(&room_dir).expect("room dir");
        let spec = serde_json::json!({
            "room": room,
            "seats": [{"seat_key": seat, "seat_type": "developer", "runtime": runtime}],
            "links": []
        });
        fs::write(
            room_dir.join("spec.json"),
            serde_json::to_string_pretty(&spec).unwrap(),
        )
        .expect("spec");
    }

    /// A seat key no tmux session on this host can bear. The session name is `agenthub--<room>--<seat>`,
    /// and a live scratch session (or a developer's own forcing run) with the same name would make a
    /// test's "not running" seat read as RUNNING - measured: a leftover `agenthub--demo--dev` from the
    /// forcing run turned two of these tests red. The process id in the key makes the session name unique
    /// to this test binary, so no session can exist unless this test created one (and it does not).
    fn seat_key(tag: &str) -> String {
        format!("dev-{tag}-{}", std::process::id())
    }

    fn write_state_file(state_dir: &Path, room: &str, seat: &str, state: &str, updated_at: &str) {
        let run_dir = state_dir.join("rooms").join(room).join("run");
        fs::create_dir_all(&run_dir).expect("run dir");
        fs::write(
            run_dir.join(format!("{seat}.state.json")),
            serde_json::json!({
                "state": state,
                // THE WORD THE RUNNER PUBLISHES BESIDE IT, so a fixture document is shaped like a real one
                // instead of leaving every status test's seat reading as `unknown` by accident.
                "activity": ActivityWord::from_runner_state(state).as_str(),
                "classification": "",
                "resume_attempts": 0,
                "updated_at": updated_at,
            })
            .to_string(),
        )
        .expect("state file");
    }

    /// THE VERDICT AND THE MESSAGE, pinned where the lead measured them: LIVENESS ALONE IS NOT "RUNNING" for a
    /// runtime that publishes readiness. A pane whose agent died stays alive, and the verb the operator checks
    /// after `up` refused read it as `running` - so the state now comes from the proof, and the detail says
    /// which proof it was.
    #[tokio::test]
    async fn an_omp_seat_reports_the_proof_its_state_rests_on() {
        let _server = crate::pty::test_servers::own("omp-proof");
        let dir = scratch("omp-proof");
        let room = format!("omp-proof-{}", std::process::id());
        let key = seat_key("omp-proof");
        write_spec(&dir, &room, &key);
        let supervisor = supervisor_at(&dir);

        // A real pane, so the liveness half is genuinely true and only the readiness document varies.
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(&session, Path::new("."), "sleep 30", &HashMap::new())
            .expect("scratch pane");

        let now = Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true);
        let read = |supervisor: &RoomSupervisor| {
            supervisor
                .get_room_status(&room)
                .expect("status")
                .into_iter()
                .next()
                .expect("one seat")
        };

        // No readiness document at all: the pane is up and nothing has handshaked. Calling this `running` is
        // what let a restart inherit the previous generation's green.
        let none = read(&supervisor);
        assert_eq!(
            none.state,
            SeatLifecycleState::Starting,
            "a pane with no handshake is not running, got {:?}",
            none.detail
        );
        assert!(
            none.detail
                .as_deref()
                .is_some_and(|d| d.contains("no handshake has been published")),
            "the status must say the handshake is missing, got {:?}",
            none.detail
        );

        // A live state: running, and the detail names the evidence rather than leaving it implied.
        write_state_file(&dir, &room, &key, "idle", &now);
        let live = read(&supervisor);
        assert_eq!(live.state, SeatLifecycleState::Running);
        assert!(
            live.detail
                .as_deref()
                .is_some_and(|d| d.contains("answered the transport handshake")),
            "RUNNING must say which proof it rests on, got {:?}",
            live.detail
        );

        // The pane outlived its agent: not running, whatever the session still says.
        write_state_file(&dir, &room, &key, "agent_socket_closed", &now);
        let dead = read(&supervisor);
        assert_eq!(
            dead.state,
            SeatLifecycleState::Dead,
            "a pane whose agent closed its socket is not a running seat"
        );
        assert_eq!(dead.detail.as_deref(), Some("agent_socket_closed"));

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    /// A room spec whose single seat runs a shell command, so the probe is driven by a REAL tmux pane and not an
    /// omp runner: the runtime string is what selects the branch under test.
    fn startable_spec(room: &str, seat: &str, runtime: &str) -> RoomSpec {
        serde_json::from_value(serde_json::json!({
            "room": room,
            "seats": [{"seat_key": seat, "seat_type": "developer", "runtime": runtime}],
            "links": []
        }))
        .expect("room spec")
    }

    /// `<state dir>/rooms/<room>`, the room's own directory, spelled the way the launcher computes it.
    fn room_dir_of(state_dir: &Path, room: &str) -> PathBuf {
        state_dir.join("rooms").join(room)
    }

    /// The room's own directory for a seat: `<state dir>/rooms/<room>/wt/<seat>`. The launcher gives it to a seat
    /// whose spec names no `cwd`, and a seat with no directory of its own does not start at all - so a case that
    /// starts one of those leaves the room this directory, the way a room that holds a worktree does.
    fn seat_worktree(state_dir: &Path, room: &str, seat: &str) -> PathBuf {
        let dir = room_dir_of(state_dir, room).join("wt").join(seat);
        fs::create_dir_all(&dir).expect("the seat's worktree");
        dir
    }

    /// The LIVE direction of the non-omp readiness proof. Those runtimes have no runner to publish a readiness
    /// word, so their proof is the session STILL existing after the grace period - the paragraph in `start_seat`
    /// says exactly that, and the code used to contradict it by returning a survival refusal at the deadline.
    ///
    /// RED without the fix: the old arm returned `Err(.. its session did not survive 1500ms)` for a pane it had
    /// just watched, alive, on the iteration before.
    #[tokio::test]
    async fn a_live_non_omp_seat_is_running_once_its_grace_has_passed() {
        let _server = crate::pty::test_servers::own("probe-live");
        let dir = scratch("probe-live");
        let room = format!("probe-live-{}", std::process::id());
        let key = seat_key("probe-live");
        seat_worktree(&dir, &room, &key);
        let spec = startable_spec(&room, &key, "sleep 30");
        let supervisor = supervisor_at(&dir);

        let status = supervisor
            .start_seat(&room, &spec.seats[0], &spec)
            .expect("a live non-omp pane must be called running once its grace has passed");
        assert_eq!(status.state, SeatLifecycleState::Running);
        assert!(TerminalManager::is_session_alive(
            &RoomSupervisor::session_name(&room, &key)
        ));
        // And RUNNING must not read as more than it is: no readiness channel was consulted for this runtime, so
        // the status has to say so rather than let a live-but-unconfigured agent pass as a working one.
        assert!(
            status
                .detail
                .as_deref()
                .is_some_and(|d| d.contains("no readiness channel")),
            "a non-omp seat must not be reported as simply running, got detail {:?}",
            status.detail
        );

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    /// The DEAD direction, and the honesty half of the same fix: a non-omp seat whose pane is gone must STILL be
    /// refused, and the refusal must name what actually happened rather than a survival verdict nothing measured.
    /// This stays GREEN across the fix, which is the point - direction (ii) must not be traded away for (i).
    #[tokio::test]
    async fn a_dead_non_omp_seat_is_refused_by_its_real_cause() {
        let dir = scratch("probe-dead");
        let room = format!("probe-dead-{}", std::process::id());
        let key = seat_key("probe-dead");
        seat_worktree(&dir, &room, &key);
        let spec = startable_spec(&room, &key, "no-such-program-agenthub-probe");
        let supervisor = supervisor_at(&dir);

        let err = supervisor
            .start_seat(&room, &spec.seats[0], &spec)
            .expect_err("a pane that is gone must not be reported as running");
        assert!(
            err.contains("died before it reported readiness"),
            "the refusal must name the real cause, got: {err}"
        );
        assert!(
            !err.contains("did not survive"),
            "a survival verdict is the defect this fix removes, got: {err}"
        );
        assert!(
            err.contains("no ready frame ever arrived"),
            "the refusal must say what happened - the pane went away and no ready frame arrived - got: {err}"
        );
        assert!(
            !err.contains("pane is gone within"),
            "naming the window as an elapsed time reads as though the pane survived it and then went, when the \
             window is the bound the wait was judged against, got: {err}"
        );
        assert!(
            err.contains("two reads of the session agree"),
            "a death verdict is two agreeing reads, not one read that failed - the measured defect reported a \
             pid of 0 manufactured from a parse failure, got: {err}"
        );
        assert!(
            !err.contains("waited up to"),
            "the message must report the wait PERFORMED, not the window it was given: the measured defect read \
             `waited up to 60s` 2.09ms after the launch, got: {err}"
        );

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    /// THE RESEAT REPORT (feedback 3ac8b369): a reseat killed the seat's pane, the start failed, and the seat's
    /// own log and state said nothing at all - three reproductions, no reason anywhere. Reseat is the only route
    /// a landed skill has to a running seat, so the silence stranded the seat.
    ///
    /// Two facts are asserted, and they are the two the report asked for: the failure NAMES THE STAGE in the
    /// seat's own log, and the retry is EXACTLY ONE - two attempt lines, one final line, and a refusal that says
    /// it is final. A third attempt would be the loop the report ruled out.
    #[tokio::test]
    async fn a_dying_start_names_the_stage_in_the_seat_log_and_retries_exactly_once() {
        // Two launches of a pane, so this case takes the process's tmux root for its own scratch server the way
        // every other pane-opening case here does: `TMUX_TMPDIR` is process-wide, and without the guard this
        // case's second launch can land in a parallel test's root.
        let _server = crate::pty::test_servers::own("start-retry");
        let dir = scratch("start-retry");
        let room = format!("start-retry-{}", std::process::id());
        let key = seat_key("start-retry");
        seat_worktree(&dir, &room, &key);
        let spec = startable_spec(&room, &key, "no-such-program-agenthub-probe");
        let supervisor = supervisor_at(&dir);

        let err = supervisor
            .start_seat(&room, &spec.seats[0], &spec)
            .expect_err("a pane that is gone must not be reported as running");
        assert!(
            err.contains("(final)") && err.contains("retried once"),
            "the refusal must say the retry was spent and this is final, got: {err}"
        );
        assert!(
            err.contains("did not start after 2 attempts"),
            "the refusal must count the attempts it made, got: {err}"
        );

        let log_path = crate::runner::seat_log_path(&dir, &room, &key);
        let log = fs::read_to_string(&log_path)
            .unwrap_or_else(|e| panic!("the seat's own log {}: {e}", log_path.display()));
        let attempts = log
            .lines()
            .filter(|line| line.contains("start attempt "))
            .count();
        assert_eq!(
            attempts, 2,
            "the start must retry exactly once - two attempt lines, never a loop; log: {log}"
        );
        assert!(
            log.contains("pane exit before readiness"),
            "the log line must name the stage that failed, got: {log}"
        );
        assert!(
            log.lines()
                .any(|line| line.contains("start final: 2 attempts failed")),
            "the second failure must be recorded as final, not as another attempt, got: {log}"
        );

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    /// THE RE-READ, FALSIFIED WITH A TRANSIENT SESSION READ - the C4 run's race, forced.
    ///
    /// A `tmux` ahead of the real one on PATH lets `new-session` through and then fails the FIRST `has-session`
    /// for THIS test's session: that is the read which said `its pane is gone` 2.09ms after the launch, with the
    /// 60s budget unused. The seat must still come up, which can only happen if the supervisor asked a second
    /// time, and the shim's own refusal is the one the log names.
    ///
    /// The shim acts only on a session whose name carries this test's pid, so every other test in the suite -
    /// which runs in parallel - passes through it to the real tmux unchanged. PATH is restored before the
    /// assertions, so a failure below cannot leave the suite on a shim.
    #[tokio::test]
    async fn a_transient_session_read_does_not_kill_the_seat() {
        #[cfg(unix)]
        use std::os::unix::fs::PermissionsExt;

        let dir = scratch("probe-flake");
        // This case needs a LIVE session for the whole read window, so it holds the process's tmux root for a
        // scratch server of its own the way every other pane-opening case in this file does: `TMUX_TMPDIR` is
        // process-wide, and without the guard a parallel test's own root lands on this process mid-wait and the
        // session this case is watching reads as gone.
        let _server = crate::pty::test_servers::own("probe-flake");
        let room = format!("probe-flake-{}", std::process::id());
        let key = seat_key("probe-flake");
        // Main's rule (the seat-cwd scope): a seat whose spec names no `cwd` starts in the room's own
        // `wt/<seat>` and does not start AT ALL without it, so a case that launches one leaves the room that
        // directory, the way a room that holds a worktree does.
        seat_worktree(&dir, &room, &key);
        let spec = startable_spec(&room, &key, "sleep 30");
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);

        let real = String::from_utf8(
            std::process::Command::new("sh")
                .args(["-c", "command -v tmux"])
                .output()
                .expect("this test needs a shell")
                .stdout,
        )
        .unwrap()
        .trim()
        .to_string();
        assert!(!real.is_empty(), "this test needs the real tmux on PATH");

        let bin = dir.join("shim-bin");
        fs::create_dir_all(&bin).unwrap();
        let launched = dir.join("launched");
        let script = format!(
            "#!/bin/sh\n\
             case \"$*\" in\n\
             *{session}*)\n\
             \x20 if [ \"$1\" = \"new-session\" ]; then : > {launched}; fi\n\
             \x20 if [ \"$1\" = \"has-session\" ] && [ -e {launched} ] && [ ! -e {launched}.used ]; then\n\
             \x20   : > {launched}.used\n\
             \x20   echo \"shim: refusing one session read\" >&2\n\
             \x20   exit 1\n\
             \x20 fi\n\
             \x20 ;;\n\
             esac\n\
             exec {real} \"$@\"\n",
            session = session,
            launched = launched.display(),
            real = real,
        );
        let shim = bin.join("tmux");
        fs::write(&shim, script).unwrap();
        fs::set_permissions(&shim, fs::Permissions::from_mode(0o755)).unwrap();

        let previous_path = std::env::var("PATH").unwrap_or_default();
        std::env::set_var("PATH", format!("{}:{previous_path}", bin.display()));
        let started = supervisor.start_seat(&room, &spec.seats[0], &spec);
        std::env::set_var("PATH", previous_path);

        let status = started.expect(
            "the seat must come up: the second read of the session said the pane is there, and the first \
             one's refusal is the race this case forces",
        );
        assert_eq!(status.state, SeatLifecycleState::Running);
        assert!(
            dir.join("launched.used").exists(),
            "the shim must have refused exactly one session read, or this case proved nothing"
        );

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    /// The scope is the room spec's directory for the seat FIRST - where the pane starts and what a relative path
    /// resolves against - then the seat's own worktrees under the room's `wt/`, and neither another seat's worktree
    /// nor an unrelated name that merely starts with the seat key.
    #[test]
    fn a_seats_scope_is_its_named_directory_first_and_its_own_worktrees_after() {
        let dir = scratch("scope");
        let room = "scope-room";
        let room_dir = dir.join("rooms").join(room);
        let hub = seat_worktree(&dir, room, "dev");
        let client = room_dir.join("wt").join("dev-client");
        let other = room_dir.join("wt").join("other");
        let device = room_dir.join("wt").join("device");
        for path in [&client, &other, &device] {
            fs::create_dir_all(path).expect("a worktree other than the seat's");
        }
        let named = |cwd: Option<&str>| {
            let mut spec = serde_json::json!({
                "room": room,
                "seats": [{"seat_key": "dev", "seat_type": "developer", "runtime": "omp"}],
                "links": []
            });
            if let Some(cwd) = cwd {
                spec["seats"][0]["cwd"] = serde_json::Value::String(cwd.to_string());
            }
            serde_json::from_value::<RoomSpec>(spec).expect("room spec")
        };
        let scope = |cwd: Option<&str>| {
            let spec = named(cwd);
            seat_scope(&spec.seats[0], &spec, &room_dir)
        };
        let canonical = |path: &Path| path.canonicalize().expect("a worktree on disk");

        // Named: the spec's directory leads, and the seat's other worktree still follows it.
        assert_eq!(
            scope(Some(&hub.display().to_string())).expect("a named seat starts"),
            vec![canonical(&hub), canonical(&client)]
        );
        // Unnamed: the room's own worktree for the seat is where it starts, and the one holding another repo is
        // reachable too. `other` and `device` are in the same room and are NOT the seat's.
        assert_eq!(
            scope(None).expect("a seat with a room worktree starts"),
            vec![canonical(&hub), canonical(&client)]
        );
        // A relative directory would resolve against the daemon's own, which is the base this fix removes.
        let err = scope(Some("wt/dev")).expect_err("a relative directory is refused");
        assert!(
            err.contains("is not absolute"),
            "the refusal says why, got: {err}"
        );
    }

    /// The lane rule admits `<wt>/<seat>` and `<wt>/<seat>-*` by name, so two seat keys where one equals the other
    /// or is its `-`-prefix would name one directory for two seats. The room refuses that spec, naming both keys;
    /// `go-dev` beside `fe-dev` stays legal because neither prefix belongs to the other.
    #[test]
    fn seat_keys_that_share_a_lane_prefix_are_refused_at_room_start() {
        let spec = |keys: &[&str]| {
            let seats: Vec<serde_json::Value> = keys
                .iter()
                .map(|k| serde_json::json!({"seat_key": k, "seat_type": "developer", "runtime": "omp"}))
                .collect();
            serde_json::from_value::<RoomSpec>(serde_json::json!({
                "room": "lanes-room",
                "seats": seats,
                "links": []
            }))
            .expect("room spec")
        };

        for (keys, short, long) in [
            (["go", "go-dev"], "go", "go-dev"),
            (["go-dev", "go"], "go", "go-dev"),
        ] {
            let refusal = seat_key_collision(&spec(&keys)).expect("a prefix pair is refused");
            assert!(
                refusal.contains(&format!("'{short}'")) && refusal.contains(&format!("'{long}'")),
                "the refusal names both seat keys, got: {refusal}"
            );
        }
        let twice = seat_key_collision(&spec(&["dev", "dev"])).expect("a duplicate key is refused");
        assert!(twice.contains("names seat 'dev' twice"), "got: {twice}");
        assert!(
            seat_key_collision(&spec(&["go-dev", "go-fs"])).is_none(),
            "sibling keys that are not each other's prefix stay legal"
        );
    }

    /// `up` reaches the check through `start_room`, so the refusal is proven at that entry: a spec holding `go` and
    /// `go-dev` is refused with both keys named, and the room's directory is not made - nothing half-started is
    /// left behind for the next attempt to inherit.
    #[tokio::test]
    async fn a_room_whose_seat_keys_share_a_lane_prefix_does_not_start() {
        let dir = scratch("lane-prefix");
        let room = format!("lane-prefix-{}", std::process::id());
        let spec: RoomSpec = serde_json::from_value(serde_json::json!({
            "room": room,
            "seats": [
                {"seat_key": "go", "seat_type": "developer", "runtime": "omp"},
                {"seat_key": "go-dev", "seat_type": "developer", "runtime": "omp"}
            ],
            "links": []
        }))
        .expect("room spec");
        let supervisor = supervisor_at(&dir);

        let err = supervisor
            .start_room(spec)
            .expect_err("two seat keys that meet at a lane prefix must not start");
        assert!(
            err.contains("'go'") && err.contains("'go-dev'"),
            "the refusal names both keys, got: {err}"
        );
        assert!(
            !room_dir_of(&dir, &room).exists(),
            "the room directory is not created for a spec that is refused"
        );
    }

    /// A seat the room names no directory for, in a room that holds no worktree for it, does not start - and the
    /// refusal names the seat and the two ways to give it one. RED on the resolution this replaces: the seat
    /// started in `.`, the daemon's own directory, which is the checkout every seat on this host shares.
    #[tokio::test]
    async fn a_seat_the_room_gives_no_directory_is_refused_by_its_own_reason() {
        let dir = scratch("no-directory");
        let room = format!("no-directory-{}", std::process::id());
        let key = seat_key("no-directory");
        let spec = startable_spec(&room, &key, "sleep 300");
        let supervisor = supervisor_at(&dir);

        let err = supervisor
            .start_seat(&room, &spec.seats[0], &spec)
            .expect_err("a seat with no directory of its own must not start in the daemon's");
        assert!(
            err.contains(&format!("seat '{key}' has no directory of its own")),
            "the refusal names the seat, got: {err}"
        );
        assert!(
            err.contains("team.json")
                && err.contains(
                    &room_dir_of(&dir, &room)
                        .join("wt")
                        .join(&key)
                        .display()
                        .to_string()
                ),
            "the refusal names both ways to give the seat a directory, got: {err}"
        );
        assert!(
            !TerminalManager::is_session_alive(&RoomSupervisor::session_name(&room, &key)),
            "nothing was launched for a seat that was refused"
        );

        let _ = fs::remove_dir_all(&dir);
    }

    /// The whole point, measured on a live pane: a seat starts in the directory the room named for it, and the
    /// guard is handed the list - the named directory first, then the seat's other worktree - from `/proc/<pid>`,
    /// which is where the room measured the defect.
    #[tokio::test]
    async fn a_seat_starts_in_its_own_worktree_and_the_guard_is_given_the_list() {
        let _server = crate::pty::test_servers::own("seat-worktree");
        let dir = scratch("seat-worktree");
        let room = format!("seat-worktree-{}", std::process::id());
        let key = seat_key("seat-worktree");
        let hub = seat_worktree(&dir, &room, &key);
        let client = room_dir_of(&dir, &room)
            .join("wt")
            .join(format!("{key}-client"));
        fs::create_dir_all(&client).expect("the seat's second worktree");
        let spec: RoomSpec = serde_json::from_value(serde_json::json!({
            "room": room,
            "seats": [{"seat_key": key, "seat_type": "developer", "runtime": "sleep 300",
                       "cwd": hub.display().to_string()}],
            "links": []
        }))
        .expect("room spec");
        let supervisor = supervisor_at(&dir);

        let status = supervisor
            .start_seat(&room, &spec.seats[0], &spec)
            .expect("a seat with a directory starts");
        let pid = status.pid.expect("a live pane has a pid");

        let started_in = fs::read_link(format!("/proc/{pid}/cwd")).expect("the pane's own cwd");
        assert_eq!(
            started_in,
            hub.canonicalize().expect("the worktree on disk"),
            "the pane starts in the directory the room named"
        );
        let environ = fs::read(format!("/proc/{pid}/environ")).expect("the pane's own environment");
        let environ = String::from_utf8_lossy(&environ);
        let list = environ
            .split('\0')
            .find_map(|pair| pair.strip_prefix("AGENTHUB_WORKTREES="))
            .expect("the guard's directories reach the pane");
        assert_eq!(
            list,
            format!(
                "{}:{}",
                hub.canonicalize().unwrap().display(),
                client.canonicalize().unwrap().display()
            ),
            "the named directory first, then the seat's other worktree - and nothing else"
        );

        // THE LANE ROOT IS THE ROOM'S OWN wt/, beside that list: the guard admits `<wt>/<seat>-<lane>` by name
        // from it, so a lane opened mid-session needs no reseat and no second read of the list.
        let lanes = environ
            .split('\0')
            .find_map(|pair| pair.strip_prefix("AGENTHUB_LANES_ROOT="))
            .expect("the room's wt/ reaches the pane");
        assert_eq!(
            lanes,
            room_dir_of(&dir, &room).join("wt").display().to_string(),
            "the lane root is the room's wt/, where the seat's lanes are created"
        );

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn last_active_is_the_seats_own_freshness_stamp() {
        let dir = scratch("last-active");
        let seat = seat_key("last-active");
        write_spec(&dir, "demo", &seat);
        let stamp = "2026-10-13T20:15:30.250Z";
        write_state_file(&dir, "demo", &seat, "idle", stamp);

        let supervisor = supervisor_at(&dir);
        let statuses = supervisor.get_room_status("demo").expect("status");
        assert_eq!(statuses.len(), 1);
        let expected = DateTime::parse_from_rfc3339(stamp)
            .unwrap()
            .with_timezone(&Utc);
        assert_eq!(statuses[0].last_active, Some(expected));
        // No tmux session exists in this test, so the seat is not up: `last_active` is still the seat's own
        // stamp (it is read before the death is written), and the alert is absent, not empty.
        assert_eq!(statuses[0].state, SeatLifecycleState::Stopped);
        assert!(statuses[0].alert.is_none());
        let _ = fs::remove_dir_all(&dir);
    }

    /// A runtime that publishes nothing after launch (agy) is read from its own tool log, and the newer of
    /// the two stamps wins in both directions: a seat's freshness may be moved forward by a tool call, but
    /// neither source may move it backwards.
    #[tokio::test]
    async fn last_active_is_the_newer_of_the_document_and_the_tool_log() {
        let dir = scratch("last-active-tool-log");
        let seat = seat_key("last-active-tool-log");
        write_spec(&dir, "demo", &seat);
        let launch = "2026-10-18T08:02:52.882Z";
        write_state_file(&dir, "demo", &seat, "running", launch);
        // The launch document's word is `unknown` and nothing rewrites it for this runtime.
        let seat_dir = dir.join("rooms").join("demo").join("seats").join(&seat);
        fs::create_dir_all(&seat_dir).expect("seat dir");
        let log = seat_dir.join("tool-calls.jsonl");
        let called = "2026-10-18T08:49:22.618Z";
        fs::write(
            &log,
            format!(
                "{}x\n{{\"at\":\"{launch}\",\"tool\":\"bash\"}}\n{{\"at\":\"{called}\",\"tool\":\"bash\"}}\n",
                "x".repeat(9 * 1024)
            ),
        )
        .expect("tool log");

        let supervisor = supervisor_at(&dir);
        let statuses = supervisor.get_room_status("demo").expect("status");
        assert_eq!(
            statuses[0].last_active,
            Some(
                DateTime::parse_from_rfc3339(called)
                    .unwrap()
                    .with_timezone(&Utc)
            ),
            "the tool log moves the seat's freshness forward past a document written once at launch"
        );
        // The freshness is a timestamp, never a claim about the turn: `unknown` still refuses a reseat.
        assert_eq!(statuses[0].activity, ActivityWord::Unknown);

        // And a newer document is not dragged back by the older tool line.
        let republished = "2026-10-18T09:30:00.000Z";
        write_state_file(&dir, "demo", &seat, "idle", republished);
        let statuses = supervisor.get_room_status("demo").expect("status");
        assert_eq!(
            statuses[0].last_active,
            Some(
                DateTime::parse_from_rfc3339(republished)
                    .unwrap()
                    .with_timezone(&Utc)
            )
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// The join's rule, on the facts alone: up + rows waiting = an alert carrying the kind, how long the
    /// seat has been quiet, and the rows' ids. A seat that is down, or one with nothing waiting, gets none.
    #[test]
    fn an_alert_needs_a_live_seat_and_waiting_rows() {
        let quiet_for = chrono::Duration::seconds(120);
        let last_active = Utc::now() - quiet_for;
        let ids = vec!["qitem-b".to_string(), "qitem-a".to_string()];

        let alert = alert_for(&SeatFacts {
            alive: true,
            pane_kept: false,
            published_state: Some("idle".to_string()),
            activity: ActivityWord::Idle,
            last_active: Some(last_active),
            waiting_qitem_ids: ids.clone(),
            publisher_pid: None,
            publisher_alive: None,
        })
        .expect("a live seat with work waiting carries an alert");
        assert_eq!(alert.kind, "seat_up_with_queued_work");
        assert_eq!(alert.waiting_qitem_ids, ids);
        let quiet = alert
            .quiet_seconds
            .expect("a stamp was published, so the quiet time is known");
        assert!((119..=121).contains(&quiet), "quiet_seconds = {quiet}");

        // No stamp published: "how long quiet" is unknown, and the alert says unknown instead of a zero
        // that would read as "just active".
        let unknown = alert_for(&SeatFacts {
            alive: true,
            pane_kept: false,
            published_state: None,
            activity: ActivityWord::Unknown,
            last_active: None,
            waiting_qitem_ids: vec!["qitem-c".to_string()],
            publisher_pid: None,
            publisher_alive: None,
        })
        .expect("a live seat with work waiting still gets an alert with no stamp");
        assert_eq!(unknown.quiet_seconds, None);

        // Down with work waiting: not the seat the rail can reach, so nothing to report.
        assert!(alert_for(&SeatFacts {
            alive: false,
            pane_kept: false,
            published_state: Some("agent_exited".to_string()),
            activity: ActivityWord::Unknown,
            last_active: Some(last_active),
            waiting_qitem_ids: ids.clone(),
            publisher_pid: None,
            publisher_alive: None,
        })
        .is_none());
        // Up with nothing waiting: nothing to be woken for.
        assert!(alert_for(&SeatFacts {
            alive: true,
            pane_kept: false,
            published_state: Some("idle".to_string()),
            activity: ActivityWord::Idle,
            last_active: Some(last_active),
            waiting_qitem_ids: Vec::new(),
            publisher_pid: None,
            publisher_alive: None,
        })
        .is_none());
    }

    /// waiting_row is a rail row that satisfies the four-line create schema (4/8). The supervisor's tests need
    /// a WAITING row, never a body, so the fixture is the shortest conforming one.
    fn waiting_row(source: &str, destination: impl Into<String>, summary: &str) -> CreateInput {
        CreateInput {
            source_session: source.into(),
            destination_session: destination.into(),
            summary: summary.into(),
            body: "facts: test fixture\nrule: the four-line create schema\ndecision: fixture row for a supervisor test\nnext: claim the row".into(),
            tags: vec!["handoff".into()],
            ..Default::default()
        }
    }

    /// The rail's rows have been written with more than one spelling of a seat's name, and the join has to
    /// see all of them: a row addressed to the native session name, to the room orchestrator session name, to
    /// the bare seat key, or to `seat@room` all belong to the same seat. A spelling the join missed would
    /// read as "nothing waiting" - the lie the alert exists to prevent.
    #[tokio::test]
    async fn waiting_rows_match_every_spelling_the_rail_has_used() {
        let dir = scratch("waiting-rows");
        let seat = seat_key("spellings");
        write_spec(&dir, "demo", &seat);
        let session = RoomSupervisor::session_name("demo", &seat);
        let orchestrator = format!("demo-{seat}@demo");
        let supervisor = supervisor_at(&dir);
        let mut expected_ids = Vec::new();
        {
            // The three rows are written before the join reads, and the join opens the rail for itself - the
            // same path a seat's own request takes.
            let store = supervisor.queue_store("demo").expect("open the rail");
            let seat_at_room = format!("{seat}@demo");
            for (to, summary) in [
                (session.as_str(), "native"),
                (orchestrator.as_str(), "orchestrator"),
                (seat.as_str(), "bare key"),
                (seat_at_room.as_str(), "seat at room"),
            ] {
                let row = store
                    .create(waiting_row("lead", to, summary))
                    .expect("create");
                expected_ids.push(row.qitem_id);
            }
            // A row for somebody else, and a row for this seat that is closed: neither is waiting work.
            store
                .create(waiting_row("lead", "someone-else", "not ours"))
                .expect("create");
            let closed = store
                .create(waiting_row("lead", seat.clone(), "already done"))
                .expect("create");
            store
                .close(CloseInput {
                    qitem_id: closed.qitem_id,
                    actor_session: seat.clone(),
                    closure_reason: "no-follow-on".into(),
                    closure_target: None,
                    transition_note: "handled".into(),
                })
                .expect("close");
        }

        let waiting = supervisor
            .waiting_rows("demo", &session, &seat)
            .expect("the rail is readable");
        assert_eq!(waiting.len(), 4, "waiting = {waiting:?}");
        for id in expected_ids {
            assert!(waiting.contains(&id), "{id} missing from {waiting:?}");
        }
        let _ = fs::remove_dir_all(&dir);
    }

    /// A row that named an interval is not waiting work until the interval elapses: the join drops it, so
    /// neither the status alert nor the nudge pass raises a row that said when to ask again. Measured
    /// 2026-10-22 (lead's review item 5): an owner-decision row that named hours was nudged at 1s/2s/4s and
    /// spent its whole three-attempt ladder in 19 seconds.
    #[tokio::test]
    async fn waiting_rows_hold_a_row_until_the_interval_it_named_has_elapsed() {
        let dir = scratch("waiting-rows-wake");
        let seat = seat_key("wake");
        write_spec(&dir, "demo", &seat);
        let session = RoomSupervisor::session_name("demo", &seat);
        let supervisor = supervisor_at(&dir);
        let (due_at, sleeping) = {
            let store = supervisor.queue_store("demo").expect("open the rail");
            let mut due = waiting_row("lead", session.clone(), "due now");
            due.wake_after_seconds = Some(0);
            let due = store.create(due).expect("create");
            let mut sleeping = waiting_row("lead", session.clone(), "asleep for an hour");
            sleeping.wake_after_seconds = Some(3600);
            let sleeping = store.create(sleeping).expect("create");
            (due.qitem_id, sleeping.qitem_id)
        };

        let waiting = supervisor
            .waiting_rows("demo", &session, &seat)
            .expect("the rail is readable");
        assert!(waiting.contains(&due_at), "a due row is waiting work: {waiting:?}");
        assert!(
            !waiting.contains(&sleeping),
            "a row that named 3600s must not be raised before then: {waiting:?}"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// A create resolves every accepted spelling to the session name the alert and the nudge pass read, and
    /// REFUSES a value naming no seat of the room by name, with the seats that would have worked.
    ///
    /// The session name and the room orchestrator form are what the live rail already carried; the bare seat
    /// key and `seat@room` are what a hand-written row and the client's verb wrote before this resolution
    /// existed. `seat@room` is the case the measurement named: a row stored in it nudged nobody while its
    /// session-name twin was nudged in a second. `principal@<room>` is refused with the rest - the room
    /// declares five seats and no principal, so resolving it would be the same silence under a familiar
    /// name.
    #[tokio::test]
    async fn a_queue_destination_resolves_to_the_session_name_or_refuses_by_name() {
        let dir = scratch("queue-destination");
        let seat = seat_key("dest");
        write_spec(&dir, "demo", &seat);
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name("demo", &seat);
        for spelling in [
            session.clone(),
            format!("demo-{seat}@demo"),
            format!("{seat}@demo"),
            seat.clone(),
        ] {
            assert_eq!(
                supervisor
                    .resolve_queue_destination("demo", &spelling)
                    .expect("a seat the room declares resolves"),
                session,
                "spelling {spelling:?}"
            );
        }

        let refused = supervisor
            .resolve_queue_destination("demo", "nobody@demo")
            .expect_err("a value naming no seat is refused");
        assert!(
            refused.contains("names no seat of demo")
                && refused.contains(&format!("{seat} ({session})")),
            "{refused}"
        );
        assert!(refused.contains("Nothing was written"), "{refused}");

        let principal = supervisor
            .resolve_queue_destination("demo", "principal@demo")
            .expect_err("the room declares no principal seat");
        assert!(principal.contains("\"principal@demo\""), "{principal}");

        let other_room = supervisor
            .resolve_queue_destination("demo", &format!("{seat}@elsewhere"))
            .expect_err("a row resolves only against its own room");
        assert!(other_room.contains("\"elsewhere\""), "{other_room}");

        let _ = fs::remove_dir_all(&dir);
    }

    /// THE RAIL IS A FILE THE ROOM OWNS, AND IT CAN BE REPLACED UNDER A LIVE DAEMON — a rebuilt room, a
    /// restore, or an instrument's own hygiene (`rm -f queue.sqlite`, which the U3 harness does between legs)
    /// hands the daemon a new file at the same path. A store held from the room's first use went on writing
    /// into the deleted inode: SQLite refused it with `attempt to write a readonly database` (naming a
    /// permission on a file that is writable) and every read kept answering the OLD rows as fact, for the rest
    /// of the daemon's life. Nothing is held now, so the next verb opens the file it is about to use.
    ///
    /// The check is the one the defect needs: after the replacement, the next write is IN THE NEW FILE — the
    /// moved file keeps only the row it already had — and a read stops reporting the old rows as this room's.
    #[tokio::test]
    async fn a_replaced_rail_is_the_one_the_next_verb_uses() {
        let dir = scratch("replaced-rail");
        let seat = seat_key("replaced-rail");
        write_spec(&dir, "demo", &seat);
        let supervisor = supervisor_at(&dir);
        let room_dir = dir.join("rooms").join("demo");
        let rail = room_dir.join("queue.sqlite");

        let before = supervisor
            .queue_store("demo")
            .expect("open the rail")
            .create(waiting_row(
                "lead",
                seat.clone(),
                "before the room was rebuilt",
            ))
            .expect("create");
        assert!(
            rail.is_file(),
            "the first verb left the rail at {}",
            rail.display()
        );

        // Moved aside rather than deleted, so the test can read afterwards what the daemon did NOT do: write
        // here. It goes into a room-shaped directory of its own, so the same store API can read it back.
        let moved_dir = dir.join("rooms").join("demo-before-the-rebuild");
        fs::create_dir_all(&moved_dir).expect("a home for the replaced rail");
        let moved = moved_dir.join("queue.sqlite");
        fs::rename(&rail, &moved).expect("move the rail aside");
        assert!(!rail.is_file(), "the room's rail is gone from its path");

        let after = supervisor
            .queue_store("demo")
            .expect("the next verb opens the file it is about to use")
            .create(waiting_row(
                "lead",
                seat.clone(),
                "after the room was rebuilt",
            ))
            .expect("create into the new file");

        let rows = supervisor
            .queue_store("demo")
            .expect("open the rail")
            .list(&ListFilter {
                active_only: true,
                ..Default::default()
            })
            .expect("list");
        assert_eq!(
            rows.len(),
            1,
            "the new rail holds the new row and not the old one: {rows:?}"
        );
        assert_eq!(rows[0].qitem_id, after.qitem_id);

        let stale = QueueStore::open(&moved_dir)
            .expect("the moved file is still a rail")
            .list(&ListFilter {
                active_only: true,
                ..Default::default()
            })
            .expect("list the moved rail");
        assert_eq!(stale.len(), 1);
        assert_eq!(
            stale[0].qitem_id, before.qitem_id,
            "the moved file kept its own row: the write went to the new file, not into the deleted inode"
        );

        let _ = fs::remove_dir_all(&dir);
    }

    /// THE MEASURED DEFECT, at the level it was measured (task 022666d6): a gate row addressed to an idle
    /// agy seat earned NO nudge attempt for six minutes. The seat's runtime publishes no word of its own -
    /// agy has no runner - so the only document in `run/` was the PREVIOUS omp generation's
    /// `agent_socket_closed`, written at 04:00:36Z, before the 04:02:58Z relaunch. The old gate compared
    /// that raw `state` to `idle`, so the seat was silent until a human sent by hand.
    ///
    /// Two facts are pinned: the pass SELECTS the seat (unknown is not busy), and it calls `nudge_seat` on
    /// the route the runtime actually has - the pane - where the attempt is recorded. The pane here has
    /// echo off and a process that never reads, so nothing it proves is a submission: the log must say
    /// `unconfirmed`, which is the honest word for a paste no reader took.
    #[tokio::test]
    async fn a_queued_row_for_an_idle_agy_seat_earns_a_recorded_nudge_attempt() {
        let _server = crate::pty::test_servers::own("nudge-agy");
        let dir = scratch("nudge-agy");
        let room = format!("nudge-agy-{}", std::process::id());
        let key = seat_key("nudge-agy");
        write_spec_with_runtime(&dir, &room, &key, "agy");
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(
            &session,
            Path::new("."),
            "sh -c 'stty -echo; sleep 300'",
            &HashMap::new(),
        )
        .expect("scratch pane");
        // The previous generation's death record, exactly as the live reviewer's `run/reviewer.state.json`
        // held it at 06:09Z.
        write_state_file(
            &dir,
            &room,
            &key,
            "agent_socket_closed",
            "2026-10-17T04:00:36.547Z",
        );
        let row = supervisor.queue_store(&room).expect("open the rail");
        let created = row
            .create(waiting_row("lead", session.clone(), "the owner's gate ask"))
            .expect("create");

        let pending = supervisor.pending_nudges();
        assert_eq!(
            pending.len(),
            1,
            "the agy seat is up with a row addressed to its session and has said nothing this generation"
        );
        assert_eq!(pending[0].seat, key);
        assert!(!pending[0].has_runner, "agy has no runner mailbox");
        assert_eq!(pending[0].qitem_ids, vec![created.qitem_id.clone()]);

        crate::ipc::nudge_pass(&supervisor);

        let log = fs::read_to_string(crate::runner::nudge_log_path(&dir, &room, &key))
            .expect("the pass must record its attempt, delivered or not");
        let line: serde_json::Value =
            serde_json::from_str(log.lines().next().expect("one line")).expect("json");
        assert_eq!(line["attempt"], 1);
        assert_eq!(line["qitem_ids"], serde_json::json!([created.qitem_id]));
        assert_eq!(
            line["outcome"], "unconfirmed",
            "the pane route records the pane's own word: a paste into a pane that never read it is not a submission"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// THE STATE WORD IS NOT THIS SEAT'S LIVENESS. A runtime with no runner publishes nothing, so the only
    /// document in `run/` may be a PREVIOUS generation's word - and a gate on that word either silences a
    /// live seat (`agent_socket_closed`, the measured 04:00:36Z document under a 07:02:33Z pane) or keeps a
    /// dead one talking (`idle`). The pane decides: a session with a process attached is live whatever the
    /// document says, and "no document at all" is the same live seat on a first launch.
    #[tokio::test]
    async fn a_runner_less_seats_liveness_is_its_pane_whatever_the_word_in_the_file() {
        let _server = crate::pty::test_servers::own("nudge-word");
        let dir = scratch("nudge-word");
        let room = format!("nudge-word-{}", std::process::id());
        let key = seat_key("nudge-word");
        write_spec_with_runtime(&dir, &room, &key, "agy");
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(
            &session,
            Path::new("."),
            "sh -c 'stty -echo; sleep 300'",
            &HashMap::new(),
        )
        .expect("scratch pane");
        supervisor
            .queue_store(&room)
            .expect("open the rail")
            .create(waiting_row("lead", session.clone(), "the owner's gate ask"))
            .expect("create");

        for state in ["agent_socket_closed", "agent_exited", "stopped", "idle"] {
            write_state_file(&dir, &room, &key, state, "2026-10-17T04:00:36.547Z");
            assert_eq!(
                supervisor.pending_nudges().len(),
                1,
                "the pane is up, so `{state}` is not this seat's liveness"
            );
        }
        fs::remove_file(
            dir.join("rooms")
                .join(&room)
                .join("run")
                .join(format!("{key}.state.json")),
        )
        .expect("remove the file");
        assert_eq!(
            supervisor.pending_nudges().len(),
            1,
            "a first launch has no document, and the pane is still the only proof it can give"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// A PANE WHOSE PROCESS IS GONE IS NOT A LIVE SEAT, and neither the session nor its pid says so: measured
    /// here 2026-10-17, a pane kept by `remain-on-exit` still answers `has-session` 0 and still reports the dead
    /// process's pid, with only `#{pane_dead}=1` marking the death. The alert alone (which requires the session)
    /// would nudge into that pane forever, so the pane's own dead bit is what excludes it - and a probe that
    /// FAILED is not that bit (the read-failure rule `dce05c8` measured: an unreadable pid is not a dead pane).
    #[tokio::test]
    async fn a_runner_less_seat_whose_pane_is_dead_is_not_a_candidate() {
        let _server = crate::pty::test_servers::own("nudge-deadpane");
        let dir = scratch("nudge-deadpane");
        let room = format!("nudge-deadpane-{}", std::process::id());
        let key = seat_key("nudge-deadpane");
        write_spec_with_runtime(&dir, &room, &key, "agy");
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(
            &session,
            Path::new("."),
            "sh -c 'stty -echo; sleep 0.4'",
            &HashMap::new(),
        )
        .expect("scratch pane");
        let kept = std::process::Command::new("tmux")
            .args(["set-option", "-t", &session, "remain-on-exit", "on"])
            .output()
            .expect("this test needs tmux");
        assert!(
            kept.status.success(),
            "the pane must be kept after its command exits"
        );
        // The command has exited and the pane is kept: the session and its pid survive, the process does not.
        std::thread::sleep(Duration::from_millis(1200));
        assert!(
            TerminalManager::is_session_alive(&session),
            "the fixture rests on the session outliving its pane's process"
        );
        assert!(
            !TerminalManager::pane_is_live(&session),
            "its process is gone"
        );
        supervisor
            .queue_store(&room)
            .expect("open the rail")
            .create(waiting_row("lead", session.clone(), "the owner's gate ask"))
            .expect("create");

        assert!(
            supervisor.pending_nudges().is_empty(),
            "a pane with no process behind it is not a seat a nudge can reach"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// THE STATUS ROW ITSELF MUST NOT CALL A PANE WITH NO PROCESS BEHIND IT `running` - the half the nudge pass was
    /// already right about while the row every reader shares was wrong. Measured 2026-10-20T23:09: a seat with no
    /// process read `running ... quiet_seconds=1127` and `heal --check`, which reads this row, printed `clean`. The
    /// evidence is the pane's own dead bit (`pane_dead`), and the detail names it instead of a stale published word.
    #[tokio::test]
    async fn a_pane_kept_after_its_process_exited_does_not_read_running() {
        let _server = crate::pty::test_servers::own("status-deadpane");
        let dir = scratch("status-deadpane");
        let room = format!("status-deadpane-{}", std::process::id());
        let key = seat_key("deadpane");
        write_spec_with_runtime(&dir, &room, &key, "agy");
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(
            &session,
            Path::new("."),
            "sh -c 'stty -echo; sleep 0.4'",
            &HashMap::new(),
        )
        .expect("scratch pane");
        let kept = std::process::Command::new("tmux")
            .args(["set-option", "-t", &session, "remain-on-exit", "on"])
            .output()
            .expect("this test needs tmux");
        assert!(
            kept.status.success(),
            "the pane must be kept after its command exits"
        );
        std::thread::sleep(Duration::from_millis(1200));
        assert!(
            TerminalManager::is_session_alive(&session),
            "the fixture rests on the session outliving its pane's process"
        );
        assert!(
            !TerminalManager::pane_is_live(&session),
            "its process is gone"
        );

        let statuses = supervisor.get_room_status(&room).expect("status");
        assert_eq!(statuses.len(), 1, "the room lists one seat");
        assert_eq!(
            statuses[0].state,
            SeatLifecycleState::Stopped,
            "a pane with no process behind it is not a running seat"
        );
        let detail = statuses[0].detail.as_deref().unwrap_or("");
        assert!(
            detail.contains("kept the seat's pane") && detail.contains("no live process behind it"),
            "the status must name the evidence it holds, not a stale word: {detail:?}"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// AND A LIVE PANE IS NOT A REASON TO NUDGE: the pane proves the seat is reachable, and the ALERT is
    /// still the trigger - a seat owed nothing stays quiet, however long its pane has been up.
    #[tokio::test]
    async fn a_runner_less_seat_with_nothing_owed_is_not_nudged() {
        let _server = crate::pty::test_servers::own("nudge-owed");
        let dir = scratch("nudge-owed");
        let room = format!("nudge-owed-{}", std::process::id());
        let key = seat_key("nudge-owed");
        write_spec_with_runtime(&dir, &room, &key, "agy");
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(
            &session,
            Path::new("."),
            "sh -c 'stty -echo; sleep 300'",
            &HashMap::new(),
        )
        .expect("scratch pane");

        assert!(
            supervisor.pending_nudges().is_empty(),
            "a waiting pane with no row addressed to it has nothing to be nudged about"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// A RUNNER-LESS LAUNCH OWNS ITS STATE DOCUMENT. Nothing in the seat will rewrite the file, so the
    /// PREVIOUS generation's death word must not outlive the pane the room just started - and the document
    /// must name the process that can still be checked, the pane's pid, not the daemon's, which outlives
    /// every seat it starts.
    #[tokio::test]
    async fn a_runner_less_launch_writes_a_state_document_the_pane_can_back() {
        let _server = crate::pty::test_servers::own("launch-state");
        let dir = scratch("launch-state");
        let room = format!("launch-state-{}", std::process::id());
        let key = seat_key("launch-state");
        seat_worktree(&dir, &room, &key);
        let spec = startable_spec(&room, &key, "sleep 30");
        // The document the previous generation left, in the shape and time the live reviewer's held it.
        write_state_file(
            &dir,
            &room,
            &key,
            "agent_socket_closed",
            "2026-10-17T04:00:36.547Z",
        );
        let supervisor = supervisor_at(&dir);

        let status = supervisor
            .start_seat(&room, &spec.seats[0], &spec)
            .expect("the pane is up");
        assert_eq!(status.state, SeatLifecycleState::Running);

        let path = dir
            .join("rooms")
            .join(&room)
            .join("run")
            .join(format!("{key}.state.json"));
        let doc: serde_json::Value = serde_json::from_str(
            &fs::read_to_string(&path).expect("the launch published its state"),
        )
        .expect("json");
        assert_eq!(
            doc["state"], "running",
            "the pane is up, so the launch's own word replaces the previous generation's death"
        );
        assert_eq!(
            doc["activity"], "unknown",
            "a pane that is up has not said what it is doing, and `running` is not a turn word"
        );
        assert_eq!(
            doc["pid"].as_u64().unwrap() as u32,
            crate::pty::pane_pid(&RoomSupervisor::session_name(&room, &key)).unwrap(),
            "the claim names the pane's process, the one that can still be checked"
        );
        let written: chrono::DateTime<chrono::Utc> = doc["updated_at"]
            .as_str()
            .unwrap()
            .parse()
            .expect("rfc3339");
        assert!(
            written > chrono::Utc::now() - chrono::Duration::minutes(5),
            "a stale timestamp would make the launch read as the old document, got {}",
            doc["updated_at"]
        );

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    /// THE MEASURED DEFECT'S OTHER HALF (task 005e0aad): a row set blocked on a still-open gate stayed in
    /// the join, so the pass woke its destination for work nothing could pick up (`go-dev.nudge.jsonl`
    /// attempts 2 and 3 at 06:37:06Z/06:37:13Z). `blocked` is still an ACTIVE state for the rail - it can
    /// be listed, claimed and closed - but it is not work waiting for its destination.
    ///
    /// Both halves are pinned where they were measured: while the row is blocked no attempt is recorded
    /// and the seat's status carries no queued-work alert naming it; the blocker's close returns the row
    /// to `pending`, and the same pass then earns EXACTLY ONE attempt whose log line names the row.
    #[tokio::test]
    async fn a_blocked_row_wakes_nobody_until_its_blocker_closes() {
        let _server = crate::pty::test_servers::own("nudge-blocked");
        let dir = scratch("nudge-blocked");
        let room = format!("nudge-blocked-{}", std::process::id());
        let key = seat_key("nudge-blocked");
        write_spec_with_runtime(&dir, &room, &key, "agy");
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(&session, Path::new("."), "sleep 300", &HashMap::new())
            .expect("scratch pane");
        write_state_file(&dir, &room, &key, "idle", "2026-10-17T06:00:00.000Z");

        let store = supervisor.queue_store(&room).expect("open the rail");
        let blocker = store
            .create(waiting_row("lead", "reviewer@elsewhere", "the gate"))
            .expect("create the blocker");
        // The blocker is IN PROGRESS and still open: the shape measured on the live rail.
        store
            .update(crate::queue::UpdateInput {
                qitem_id: blocker.qitem_id.clone(),
                actor_session: "reviewer".into(),
                state: Some("in-progress".into()),
                ..Default::default()
            })
            .expect("open the gate");
        let dependent = store
            .create(waiting_row("lead", session.clone(), "the gated ask"))
            .expect("create the dependent");
        store
            .update(crate::queue::UpdateInput {
                qitem_id: dependent.qitem_id.clone(),
                actor_session: "lead".into(),
                state: Some("blocked".into()),
                blocked_on: Some(blocker.qitem_id.clone()),
                ..Default::default()
            })
            .expect("block on the gate");

        // Half one: the blocked row is on the rail and explained by it, but it wakes NOBODY.
        let alerted: Vec<String> = supervisor
            .get_room_status(&room)
            .expect("status")
            .into_iter()
            .flat_map(|s| s.alert.into_iter().flat_map(|a| a.waiting_qitem_ids))
            .collect();
        assert!(
            !alerted.contains(&dependent.qitem_id),
            "a blocked row is not queued work for its destination: {alerted:?}"
        );
        assert!(
            supervisor.pending_nudges().is_empty(),
            "a blocked row is not selected by the nudge pass"
        );
        crate::ipc::nudge_pass(&supervisor);
        assert!(
            !crate::runner::nudge_log_path(&dir, &room, &key).exists(),
            "the pass must not record an attempt for a row its blocker has not let go"
        );
        // ...and the row is still the rail's own business: active, claimable, readable.
        let active = store
            .list(&crate::queue::ListFilter {
                active_only: true,
                ..Default::default()
            })
            .expect("list");
        assert!(active.iter().any(|r| r.qitem_id == dependent.qitem_id));

        // Half two: the blocker's CLOSE is the wake - the row returns to pending, and the pass earns one
        // attempt naming it.
        store
            .close(CloseInput {
                qitem_id: blocker.qitem_id.clone(),
                actor_session: "reviewer".into(),
                closure_reason: "no-follow-on".into(),
                closure_target: None,
                transition_note: "gate closed".into(),
            })
            .expect("close the gate");
        assert_eq!(
            store.show(&dependent.qitem_id).expect("show").state,
            "pending",
            "the wake returns the row to the rail's pending set"
        );

        let pending = supervisor.pending_nudges();
        assert_eq!(pending.len(), 1, "the wake is the row, once");
        assert_eq!(pending[0].qitem_ids, vec![dependent.qitem_id.clone()]);
        crate::ipc::nudge_pass(&supervisor);
        let log = fs::read_to_string(crate::runner::nudge_log_path(&dir, &room, &key))
            .expect("the wake earns an attempt");
        let lines: Vec<&str> = log.lines().collect();
        assert_eq!(lines.len(), 1, "exactly one attempt for the wake: {log}");
        let line: serde_json::Value = serde_json::from_str(lines[0]).expect("json");
        assert_eq!(line["attempt"], 1);
        assert_eq!(line["qitem_ids"], serde_json::json!([dependent.qitem_id]));
        let _ = fs::remove_dir_all(&dir);
    }

    /// A SEAT MID-TURN IS NOT NUDGED: `working`/`retrying`/`blocked` (one word, `Working`) and
    /// `needs_input` are the two states a nudge must not interrupt - the first would collide with a live
    /// turn, the second talks past a seat already asking for input.
    #[tokio::test]
    async fn a_seat_that_says_a_turn_is_in_flight_is_not_nudged() {
        let _server = crate::pty::test_servers::own("nudge-busy");
        let dir = scratch("nudge-busy");
        let room = format!("nudge-busy-{}", std::process::id());
        let key = seat_key("nudge-busy");
        write_spec(&dir, &room, &key);
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(&session, Path::new("."), "sleep 300", &HashMap::new())
            .expect("scratch pane");
        let row = supervisor.queue_store(&room).expect("open the rail");
        row.create(waiting_row("lead", session.clone(), "the owner's gate ask"))
            .expect("create");

        for state in ["working", "retrying", "blocked", "needs_input"] {
            write_state_file(&dir, &room, &key, state, "2026-10-17T06:00:00.000Z");
            assert!(
                supervisor.pending_nudges().is_empty(),
                "{state} is a seat saying a turn is in flight, and a nudge would interrupt it"
            );
        }
        // ...and the same seat one word later IS selected: the gate reads the word, not the history.
        write_state_file(&dir, &room, &key, "idle", "2026-10-17T06:00:00.000Z");
        assert_eq!(
            supervisor.pending_nudges().len(),
            1,
            "idle is the runner's come in"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// AN UNKNOWN WORD IS NOT BUSY. A document without the `activity` word (an older runner, or a hand
    /// written fixture) says nothing this pass can act on, and silence must cost a nudge attempt rather
    /// than the wake: the nudge is a seat's only automatic route to a waiting row.
    #[tokio::test]
    async fn a_document_without_the_word_is_unknown_and_still_earns_a_nudge() {
        let _server = crate::pty::test_servers::own("nudge-unknown");
        let dir = scratch("nudge-unknown");
        let room = format!("nudge-unknown-{}", std::process::id());
        let key = seat_key("nudge-unknown");
        write_spec(&dir, &room, &key);
        let supervisor = supervisor_at(&dir);
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(&session, Path::new("."), "sleep 300", &HashMap::new())
            .expect("scratch pane");
        // `state` alone: the shape the runner wrote before `activity` was published beside it.
        let run_dir = dir.join("rooms").join(&room).join("run");
        fs::create_dir_all(&run_dir).expect("run dir");
        fs::write(
            run_dir.join(format!("{key}.state.json")),
            serde_json::json!({"state": "idle", "classification": "", "resume_attempts": 0})
                .to_string(),
        )
        .expect("state file");
        let row = supervisor.queue_store(&room).expect("open the rail");
        row.create(waiting_row("lead", session, "the owner's gate ask"))
            .expect("create");

        assert_eq!(
            supervisor.pending_nudges().len(),
            1,
            "a word this runtime cannot read is unknown, and unknown is not busy"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// Pin 2 of the ordering (the seat's own requirement): the alert is ADVISORY, so a rail that cannot be
    /// read costs the alert and NOTHING else. The status must still answer every seat it knows with its
    /// liveness and freshness - a status that failed because an advisory about a different fact could not
    /// be computed would trade the freshness signal for an outage. The room here holds one seat and a
    /// `queue.sqlite` that is a *directory*, so opening the rail fails for real (not by mocking).
    #[tokio::test]
    async fn an_unreadable_rail_costs_the_alert_and_not_the_status() {
        let dir = scratch("rail-unreadable");
        let seat = seat_key("rail-unreadable");
        write_spec(&dir, "demo", &seat);
        write_state_file(&dir, "demo", &seat, "idle", "2026-10-13T20:15:30.250Z");
        // A directory where the store's file must be: `rusqlite` cannot open it, so `waiting_rows` errors.
        fs::create_dir_all(dir.join("rooms/demo/queue.sqlite")).expect("blocking path");

        let supervisor = supervisor_at(&dir);
        let statuses = supervisor
            .get_room_status("demo")
            .expect("status must answer even when the rail cannot be read");
        assert_eq!(statuses.len(), 1);
        assert_eq!(statuses[0].seat, seat);
        // The freshness stamp is a fact about a local file that has nothing to do with the rail, so it is
        // still reported...
        assert_eq!(
            statuses[0].last_active,
            Some(
                DateTime::parse_from_rfc3339("2026-10-13T20:15:30.250Z")
                    .unwrap()
                    .with_timezone(&Utc)
            )
        );
        // ...and the missing advisory is absent, not a fabricated empty one.
        assert!(statuses[0].alert.is_none());
        let _ = fs::remove_dir_all(&dir);
    }

    /// A SEAT IS STARTED KNOWING WHICH DAEMON IT BELONGS TO. A seat's own `4genteam send`/`room queue`
    /// resolves the socket from `$AGENTHUB_RUNTIME_SOCKET` and then THIS MACHINE'S own path, so a seat whose env
    /// lacks it addresses whichever daemon owns the default socket - measured 2026-10-14 in an isolated run: the
    /// run's own seat reached the box's live runtime, whose answer was a refusal for a mailbox under the live
    /// state directory.
    ///
    /// The spec's own entry does not win: the socket that matters is the one the daemon that STARTED the seat
    /// answers on, and a value recorded in a spec can only be the socket of whichever daemon existed then.
    #[test]
    fn a_seats_env_states_the_socket_of_the_daemon_that_started_it() {
        let seat: SeatSpec = serde_json::from_value(serde_json::json!({
            "seat_key": "dev",
            "seat_type": "developer",
            "runtime": "omp",
            "env": {"AGENTHUB_RUNTIME_SOCKET": "/tmp/stale.sock"}
        }))
        .expect("seat");
        let env = seat_env(
            &seat,
            "isolated",
            "agenthub--isolated--dev",
            Path::new("/state/rooms/isolated/seats/dev"),
            Path::new("/tmp/isolation/rt/agenthub.sock"),
            Path::new("/nonexistent-state-root"),
            Path::new("/state/rooms/isolated/wt/dev"),
        );

        assert_eq!(
            env.get("AGENTHUB_RUNTIME_SOCKET").map(String::as_str),
            Some("/tmp/isolation/rt/agenthub.sock"),
            "the seat has to be told the socket of the daemon that started it"
        );
        assert_eq!(
            env.get("AGENTHUB_ROOM").map(String::as_str),
            Some("isolated")
        );
        assert_eq!(
            env.get("AGENTHUB_SESSION_NAME").map(String::as_str),
            Some("agenthub--isolated--dev"),
            "the session name the seat's own client sends as"
        );
    }

    /// The connector credential is the one key a seat's environment must not hold, wherever it is named: the
    /// state root's `.env` is the client's own configuration file and does carry it, and the ruling is that it
    /// does not travel to a seat. Everything else in the contract survives the filter.
    #[test]
    fn a_seats_env_never_carries_the_connector_credential() {
        let dir = scratch("seat-env-connector");
        fs::write(
            dir.join(".env"),
            "AGENTHUB_CONNECTOR_TOKEN=from-the-file\nAGENTHUB_MCP_URL=http://127.0.0.1:8000/mcp\n",
        )
        .expect("write .env");
        let seat: SeatSpec = serde_json::from_value(serde_json::json!({
            "seat_key": "dev", "seat_type": "developer", "runtime": "omp",
            "env": {"AGENTHUB_CONNECTOR_TOKEN": "from-the-spec"}
        }))
        .expect("seat");
        let env = seat_env(
            &seat,
            "demo",
            "agenthub--demo--dev",
            Path::new("/s"),
            Path::new("/r.sock"),
            &dir,
            Path::new("/rooms/demo/wt/dev"),
        );
        assert!(
            !env.contains_key("AGENTHUB_CONNECTOR_TOKEN"),
            "the credential must not reach a seat, from the file ({env:?})"
        );
        assert_eq!(
            env.get("AGENTHUB_MCP_URL").map(String::as_str),
            Some("http://127.0.0.1:8000/mcp")
        );
        assert_eq!(env.get("AGENTHUB_SEAT").map(String::as_str), Some("dev"));
    }

    /// The state root's `.env` reaches every seat: the MCP URL and token are configured once, and a seat that
    /// is started holds them without a copy. The seat's own spec wins over the file, and the runtime's keys
    /// win over both.
    #[test]
    fn a_seats_env_carries_the_state_roots_env_file() {
        let dir = scratch("seat-env-file");
        fs::write(
            dir.join(".env"),
            "# a comment\nAGENTHUB_MCP_URL=http://127.0.0.1:8000/mcp\nexport QUOTED=\"a b\"\nSPEC_WINS=file\nAGENTHUB_ROOM=file\nbad key=x\n\nEMPTY=\n",
        )
        .expect("write .env");
        let seat: SeatSpec = serde_json::from_value(serde_json::json!({
            "seat_key": "dev", "seat_type": "developer", "runtime": "omp",
            "env": {"SPEC_WINS": "spec"}
        }))
        .expect("seat");
        let env = seat_env(
            &seat,
            "demo",
            "agenthub--demo--dev",
            Path::new("/s"),
            Path::new("/r.sock"),
            &dir,
            Path::new("/rooms/demo/wt/dev"),
        );
        assert_eq!(
            env.get("AGENTHUB_MCP_URL").map(String::as_str),
            Some("http://127.0.0.1:8000/mcp")
        );
        assert_eq!(env.get("QUOTED").map(String::as_str), Some("a b"));
        assert_eq!(env.get("EMPTY").map(String::as_str), Some(""));
        assert_eq!(
            env.get("SPEC_WINS").map(String::as_str),
            Some("spec"),
            "the spec wins over the file"
        );
        assert_eq!(
            env.get("AGENTHUB_ROOM").map(String::as_str),
            Some("demo"),
            "the runtime wins over both"
        );
        // The pair is the seat MARK the guard reads from /proc/<pid>/environ to attribute a pid (ruling
        // `RULING-2026-10-21-pid-ownership`): both halves, the seat key over the file's value, and neither scrubbed.
        assert_eq!(
            env.get("AGENTHUB_SEAT").map(String::as_str),
            Some("dev"),
            "the mark's other half is the seat key"
        );
        assert!(
            !crate::env_is_denied("AGENTHUB_ROOM") && !crate::env_is_denied("AGENTHUB_SEAT"),
            "a denied key never reaches the pane, so the mark would be unreadable"
        );
        assert!(!env.contains_key("bad key"));
        let none = seat_env(
            &seat,
            "demo",
            "s",
            Path::new("/s"),
            Path::new("/r.sock"),
            Path::new("/nonexistent-state-root"),
            Path::new("/rooms/demo/wt/dev"),
        );
        assert!(
            !none.contains_key("AGENTHUB_MCP_URL"),
            "no file, no variables"
        );
    }

    /// The MCP config a seat is started with carries variable references only, so it is safe to leave in the
    /// seat dir and holds no credential.
    #[test]
    fn the_seat_mcp_config_names_variables_and_no_secret() {
        let parsed: serde_json::Value = serde_json::from_str(SEAT_MCP_CONFIG).expect("valid json");
        let server = &parsed["mcpServers"]["agenthub_http"];
        assert_eq!(server["url"], "${AGENTHUB_URL}/mcp");
        assert_eq!(
            server["headers"]["Authorization"],
            "Bearer ${AGENTHUB_TOKEN}"
        );
    }

    /// THE DELEGATION ENTRY IS THE ENVIRONMENT'S, NOT THE FILE'S. With no script named the seat's mcp.json is
    /// the constant byte for byte; with one named, the entry runs THAT script with the seat's own scope as the
    /// delegation's working directory. A blank value is absence, because a shell that exports the name with
    /// nothing in it must not point node at an empty path.
    #[test]
    fn a_seat_gets_the_delegation_server_only_when_the_environment_names_one() {
        let cwd = Path::new("/rooms/demo/wt/dev");
        assert_eq!(
            seat_mcp_config(&HashMap::new(), cwd),
            SEAT_MCP_CONFIG,
            "absence must write today's file"
        );
        let mut env = HashMap::new();
        env.insert(
            DEEPSEEK_MCP_SERVER_ENV.to_string(),
            "/opt/deepseek/server.cjs".to_string(),
        );
        let text = seat_mcp_config(&env, cwd);
        let parsed: serde_json::Value = serde_json::from_str(&text).expect("valid json");
        let server = &parsed["mcpServers"]["deepseek"];
        assert_eq!(server["command"], "node");
        assert_eq!(server["args"][0], "/opt/deepseek/server.cjs");
        assert_eq!(server["env"][DEEPSEEK_MCP_CWD_ENV], "/rooms/demo/wt/dev");
        assert_eq!(server["env"]["DEEPSEEK_WORKSPACE_ATTACH"], "1");
        assert_eq!(server["env"]["DEEPSEEK_MCP_PERMISSION"], "allow");
        assert_eq!(
            parsed["mcpServers"]["agenthub_http"]["url"], "${AGENTHUB_URL}/mcp",
            "the cloud entry survives"
        );
        env.insert(DEEPSEEK_MCP_SERVER_ENV.to_string(), "   ".to_string());
        assert_eq!(
            seat_mcp_config(&env, cwd),
            SEAT_MCP_CONFIG,
            "a blank script is no script"
        );
    }

    /// The delegation server's working directory is decided by the runtime, not inherited: one daemon starts
    /// every seat of every room, so the seat's own scope is the only directory that cannot hand another room's
    /// tree to a job. A value left in the `.env` does not win.
    #[test]
    fn a_seats_env_points_the_delegation_server_at_the_seats_scope() {
        let dir = scratch("seat-env-deepseek");
        fs::write(
            dir.join(".env"),
            "DEEPSEEK_MCP_DEFAULT_CWD=/elsewhere\nDEEPSEEK_MCP_SERVER=/opt/ds/server.cjs\n",
        )
        .expect("write .env");
        let seat: SeatSpec = serde_json::from_value(serde_json::json!({
            "seat_key": "dev", "seat_type": "developer", "runtime": "omp"
        }))
        .expect("seat");
        let env = seat_env(
            &seat,
            "demo",
            "agenthub--demo--dev",
            Path::new("/s"),
            Path::new("/r.sock"),
            &dir,
            Path::new("/rooms/demo/wt/dev"),
        );
        assert_eq!(
            env.get(DEEPSEEK_MCP_CWD_ENV).map(String::as_str),
            Some("/rooms/demo/wt/dev")
        );
        assert_eq!(
            env.get(DEEPSEEK_MCP_SERVER_ENV).map(String::as_str),
            Some("/opt/ds/server.cjs")
        );
    }

    /// The hook the runtime writes is the one that blocks and logs: if it lost its guard or its log the seat would
    /// run unguarded and unrecorded with nothing failing.
    #[test]
    fn the_seat_tool_hook_guards_and_logs() {
        for needle in [
            "\"tool_call\"",
            "\"tool_result\"",
            "block: true",
            "tool-calls.jsonl",
            "env-secret",
            "docker",
            "tmux-mutate",
            "push",
        ] {
            assert!(SEAT_TOOL_HOOK.contains(needle), "the hook lost {needle}");
        }
    }

    /// THE SEAT'S POLICY FILE IS ON DISK BEFORE THE RUNNER IS.
    ///
    /// `start_seat` prepares the seat dir and only then builds the pane's command and launches the pane, so a
    /// file written in the preparation block is in place before the agent that reads it exists. The instrument
    /// is the launch itself failing: an omp seat's pane runs THIS binary as its runner, and a test binary does
    /// not speak the runner's CLI, so the pane is gone at once and `start_seat` refuses - while the document is
    /// on disk anyway. That is exactly the ordering the change is for: a write moved after the launch could not
    /// reach this file on a start that never came up.
    #[tokio::test]
    async fn the_seats_policy_document_is_written_before_the_runner_starts() {
        let _server = crate::pty::test_servers::own("seat-policy");
        let dir = scratch("seat-policy");
        let room = format!("seat-policy-{}", std::process::id());
        let key = seat_key("seat-policy");
        let _ = seat_worktree(&dir, &room, &key);
        let mut spec = startable_spec(&room, &key, "omp");
        let document = "bash:\n  allowCompoundCommands: true\ncompaction:\n  keepRecentTokens: 8000\ntools:\n  approval:\n    edit: deny\n";
        spec.seats[0].policy_document = Some(document.to_string());
        let supervisor = supervisor_at(&dir);

        let _ = supervisor.start_seat(&room, &spec.seats[0], &spec);

        let installed = room_dir_of(&dir, &room)
            .join("seats")
            .join(&key)
            .join("config.yml");
        assert_eq!(
            fs::read_to_string(&installed).expect("the seat's config.yml"),
            document,
            "the document the spec carried is installed verbatim, before the pane is up"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// A DOCUMENT IS NOT A LICENSE TO WRITE ANOTHER RUNTIME'S FILE. The field travels on every seat of a room,
    /// and only an omp seat's directory is its destination: a claude-code, agy or codex seat whose spec carries
    /// a document gets no `config.yml` from it.
    #[tokio::test]
    async fn a_seat_of_another_runtime_is_given_no_config_document() {
        let _server = crate::pty::test_servers::own("seat-policy-other");
        let dir = scratch("seat-policy-other");
        let room = format!("seat-policy-other-{}", std::process::id());
        let key = seat_key("seat-policy-other");
        let _ = seat_worktree(&dir, &room, &key);
        let mut spec = startable_spec(&room, &key, "sleep 30");
        spec.seats[0].policy_document = Some("tools:\n  approval:\n    edit: deny\n".to_string());
        let supervisor = supervisor_at(&dir);

        let _ = supervisor.start_seat(&room, &spec.seats[0], &spec);

        let installed = room_dir_of(&dir, &room)
            .join("seats")
            .join(&key)
            .join("config.yml");
        assert!(
            !installed.exists(),
            "a seat of another runtime must not receive an omp config: {}",
            installed.display()
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// A RESTART THAT BROUGHT A FRESH DOCUMENT INSTALLS IT AND KEEPS IT IN THE ROOM'S OWN RECORD.
    ///
    /// The reseat is the act that applies a policy change to a running room, so the document has to reach the
    /// seat dir (the file the agent reads) AND `spec.json`: every later route - a heal, a restore, a plain
    /// restart from a caller with no document - starts from that record, and one that still held the old
    /// document would quietly put the seat back on it.
    #[tokio::test]
    async fn a_restart_installs_the_document_it_was_given_and_records_it() {
        let _server = crate::pty::test_servers::own("seat-policy-restart");
        let dir = scratch("seat-policy-restart");
        let room = format!("seat-policy-restart-{}", std::process::id());
        let key = seat_key("seat-policy-restart");
        let worktree = seat_worktree(&dir, &room, &key);
        let spec = startable_spec(&room, &key, "omp");
        let room_dir = room_dir_of(&dir, &room);
        fs::create_dir_all(&room_dir).expect("room dir");
        fs::write(
            room_dir.join("spec.json"),
            serde_json::to_string_pretty(&spec).expect("spec json"),
        )
        .expect("spec file");
        let supervisor = supervisor_at(&dir);
        let first = "tools:\n  approval:\n    edit: deny\n";
        let document = "tools:\n  approval:\n    edit: deny\n    ast_edit: deny\n";
        // A FIRST START, on a document the restart has to REPLACE. It also gives the room the seat dir and the
        // file whose content the assertion below reads back.
        let mut first_spec = spec.clone();
        first_spec.seats[0].policy_document = Some(first.to_string());
        let _ = supervisor.start_seat(&room, &first_spec.seats[0], &first_spec);
        let installed = room_dir.join("seats").join(&key).join("config.yml");
        assert_eq!(
            fs::read_to_string(&installed).expect("the first start's config.yml"),
            first
        );

        // THE SEAT IS UP, as far as the restart's first step is concerned. That pane is a stand-in for the
        // running seat: an omp seat's pane runs THIS binary as its runner and a test binary does not speak the
        // runner's CLI, so a real one would be gone before the restart read its document - and stopping a
        // session that never existed is a different failure ("no current target") than the one under test.
        TerminalManager::launch_session(
            &RoomSupervisor::session_name(&room, &key),
            &worktree,
            "sleep 300",
            &HashMap::new(),
        )
        .expect("the seat's session");

        let _ = supervisor.restart_seat(&room, &key, false, Some(document));

        assert_eq!(
            fs::read_to_string(&installed).expect("the seat's config.yml"),
            document,
            "a reseat installs the document it was handed, replacing the one the seat started on"
        );
        let recorded: RoomSpec =
            serde_json::from_str(&fs::read_to_string(room_dir.join("spec.json")).expect("spec"))
                .expect("spec");
        assert_eq!(
            recorded.seats[0].policy_document.as_deref(),
            Some(document),
            "the room's own record carries it, so a later route cannot install the old document"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// A claude seat's settings file, written by the writer the supervisor calls, names the seat's own guard on
    /// both phases with the phase as the argument. The gate is `GUARD_HAS_CLAUDE_HOOK`, so the test forces the
    /// writer on and reads the FILE it wrote - not an in-memory string - and the runner's `--settings` flag then
    /// follows that same file (`runner::tests::claude_argv_names_the_seats_settings_only_while_the_file_is_there`).
    #[test]
    fn the_claude_settings_run_the_seat_guard_on_both_phases() {
        let dir = scratch("claude-settings");
        let seat_dir = dir.join("seats").join("architect");
        fs::create_dir_all(&seat_dir).unwrap();
        let path = seat_dir.join(CLAUDE_SETTINGS_FILE);
        write_claude_guard_settings_when(&seat_dir, true).unwrap();
        let settings: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(&path).unwrap()).unwrap();
        let command = |phase: &str| {
            settings["hooks"][phase][0]["hooks"][0]["command"]
                .as_str()
                .unwrap_or_default()
                .to_string()
        };
        for (phase, word) in [
            ("PreToolUse", "pre"),
            ("PostToolUse", "post"),
            ("PostToolUseFailure", "post"),
        ] {
            assert_eq!(
                command(phase),
                format!(
                    "node \"$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts\" --claude-hook {word}"
                ),
                "{phase} must run the seat's own guard"
            );
        }
        // A file the writer did not produce is not its to clear: a seat's own directory is the runtime's to fill, not
        // to empty, as the agy installer beside it already refuses a foreign file. Its own file is rewritten while
        // the guard carries the mode, and cleared while it does not.
        fs::write(&path, "{\"stale\":true}").unwrap();
        write_claude_guard_settings_when(&seat_dir, false).unwrap();
        assert_eq!(
            fs::read_to_string(&path).unwrap(),
            "{\"stale\":true}",
            "a settings file the writer did not write is left alone"
        );
        write_claude_guard_settings_when(&seat_dir, true).unwrap();
        assert!(
            !fs::read_to_string(&path).unwrap().contains("stale"),
            "the entry is rewritten, not kept"
        );
        write_claude_guard_settings_when(&seat_dir, false).unwrap();
        assert!(!path.exists(), "the writer clears the file it wrote");
        // The gate production uses is the const, and a true flag is only sound for an asset that carries the
        // dispatch. Prose that merely mentions the flag cannot open it.
        write_claude_guard_settings(&seat_dir).unwrap();
        assert_eq!(path.exists(), GUARD_HAS_CLAUDE_HOOK);
        if GUARD_HAS_CLAUDE_HOOK {
            assert!(
                SEAT_TOOL_HOOK.contains("--claude-hook"),
                "the flag is true only for a guard that carries the mode"
            );
        }
        let _ = fs::remove_dir_all(&dir);
    }

    /// The agy guard is added beside another tool's hook without touching it, is idempotent, and a file that is not a
    /// JSON object is refused rather than overwritten.
    #[test]
    fn an_agy_seat_is_launched_with_its_model() {
        assert_eq!(
            agy_command(Some("gemini-3.8-flash-low")),
            "agy --model 'gemini-3.8-flash-low'"
        );
        assert_eq!(agy_command(Some("a'b")), r"agy --model 'a'\''b'");
        assert_eq!(agy_command(Some(" ")), "agy");
        assert_eq!(agy_command(None), "agy");
    }

    #[test]
    fn the_agy_guard_hook_joins_the_global_file_and_leaves_the_rest() {
        let home = scratch("agy-guard");
        let path = home.join(".gemini/config/hooks.json");
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, r#"{"herdr":{"PreInvocation":[{"command":"x"}]}}"#).unwrap();
        install_agy_guard_hook(&home).unwrap();
        let first = fs::read_to_string(&path).unwrap();
        install_agy_guard_hook(&home).unwrap();
        assert_eq!(
            first,
            fs::read_to_string(&path).unwrap(),
            "a second install changed the file"
        );
        let hooks: serde_json::Value = serde_json::from_str(&first).unwrap();
        assert_eq!(hooks["herdr"]["PreInvocation"][0]["command"], "x");
        let pre = hooks[AGY_GUARD_HOOK]["PreToolUse"][0]["hooks"][0]["command"]
            .as_str()
            .unwrap();
        assert!(
            pre.contains("--agy-hook pre") && pre.contains(r#"{"decision":"ask"}"#),
            "{pre}"
        );
        fs::write(&path, "[1]").unwrap();
        assert!(install_agy_guard_hook(&home).is_err());
        assert_eq!(fs::read_to_string(&path).unwrap(), "[1]");
        let _ = fs::remove_dir_all(&home);
    }

    /// AN AGY SEAT REACHES THE CLOUD THROUGH AGY'S OWN SERVER FILE, and the entry is added beside the owner's own
    /// servers without touching them. It carries no credential - the shell line reads the seat's own variables at
    /// connect time - and it starts nothing at all outside a seat, which is what keeps one global file safe to
    /// hold. A second install changes nothing, and the owner's other servers survive it.
    #[test]
    fn the_agy_mcp_server_joins_the_global_file_and_leaves_the_rest() {
        let home = scratch("agy-mcp");
        let path = home.join(".gemini/config/mcp_config.json");
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(
            &path,
            r#"{"mcpServers":{"owner-server":{"command":"node","args":["server.cjs"]}}}"#,
        )
        .unwrap();
        install_agy_mcp_server(&home, false).unwrap();
        let first = fs::read_to_string(&path).unwrap();
        install_agy_mcp_server(&home, false).unwrap();
        assert_eq!(
            first,
            fs::read_to_string(&path).unwrap(),
            "a second install changed the file"
        );
        let config: serde_json::Value = serde_json::from_str(&first).unwrap();
        assert_eq!(
            config["mcpServers"]["owner-server"]["command"], "node",
            "the owner's own server was touched"
        );
        assert!(
            config["mcpServers"].get(AGY_DEEPSEEK_SERVER).is_none(),
            "no script named, no delegation entry"
        );
        let server = &config["mcpServers"][AGY_MCP_SERVER];
        assert_eq!(server["command"], "sh");
        assert_eq!(
            server["disabled"], false,
            "agy leaves a disabled server unloaded"
        );
        let line = server["args"][1].as_str().unwrap();
        assert!(
            line.contains("mcp-proxy --transport streamablehttp"),
            "{line}"
        );
        assert!(
            line.contains(r#""$AGENTHUB_URL/mcp""#),
            "the bridge must reach the seat's own daemon: {line}"
        );
        assert!(
            line.contains(r#"API_ACCESS_TOKEN="$AGENTHUB_TOKEN" exec mcp-proxy"#),
            "the token is handed over in the bridge's environment, read at connect time: {line}"
        );
        assert!(
            !line.contains("Bearer"),
            "an Authorization argument would publish the token in argv: {line}"
        );
        assert!(
            line.contains(r#"[ -n "$AGENTHUB_SEAT" ]"#),
            "a session that is not a seat starts no bridge: {line}"
        );
        fs::write(&path, r#"{"mcpServers":[]}"#).unwrap();
        assert!(install_agy_mcp_server(&home, false).is_err());
        assert_eq!(fs::read_to_string(&path).unwrap(), r#"{"mcpServers":[]}"#);
        fs::write(&path, "[1]").unwrap();
        assert!(install_agy_mcp_server(&home, false).is_err());
        assert_eq!(fs::read_to_string(&path).unwrap(), "[1]");
        let _ = fs::remove_dir_all(&home);
    }

    /// THE BRIDGE'S ARGV CARRIES NO CREDENTIAL: the installed line hands the seat's token to `mcp-proxy` through
    /// `API_ACCESS_TOKEN`, which the bridge reads from its own environment and turns into
    /// `Authorization: Bearer ...`; nothing expands into an argument. `/proc/<pid>/cmdline` is world-readable and
    /// `/proc/<pid>/environ` is owner-only, so this is the difference between a machine-wide and an owner-only
    /// credential. A stand-in `mcp-proxy` on PATH records what the shell really spawned.
    #[test]
    fn the_agy_bridge_argv_carries_no_credential() {
        use std::os::unix::fs::PermissionsExt;
        let home = scratch("agy-argv");
        let bin = home.join("bin");
        fs::create_dir_all(&bin).unwrap();
        let argv_out = home.join("argv");
        let env_out = home.join("env");
        let proxy = bin.join("mcp-proxy");
        fs::write(
            &proxy,
            "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGV_OUT\"\nprintf '%s' \"${API_ACCESS_TOKEN:+set}\" > \"$ENV_OUT\"\n",
        )
        .unwrap();
        fs::set_permissions(&proxy, fs::Permissions::from_mode(0o755)).unwrap();
        let line = agy_mcp_entry()["args"][1].as_str().unwrap().to_string();
        let inherited_path = std::env::var("PATH").unwrap_or_default();
        let status = std::process::Command::new("sh")
            .arg("-c")
            .arg(&line)
            .env("PATH", format!("{}:{inherited_path}", bin.display()))
            .env("AGENTHUB_SEAT", "1")
            .env("AGENTHUB_TOKEN", "SECRET-bearer-value")
            .env("AGENTHUB_URL", "http://127.0.0.1:9")
            .env("ARGV_OUT", &argv_out)
            .env("ENV_OUT", &env_out)
            .status()
            .unwrap();
        assert!(status.success(), "the installed line did not run: {status}");
        let argv = fs::read_to_string(&argv_out).unwrap();
        assert!(
            !argv.contains("SECRET-bearer-value"),
            "the credential is in the bridge's argv: {argv}"
        );
        assert!(
            !argv.contains("Bearer"),
            "an expanded Authorization reached argv: {argv}"
        );
        assert!(
            argv.contains("streamablehttp"),
            "the bridge must still be the HTTP client: {argv}"
        );
        assert!(
            argv.contains("http://127.0.0.1:9/mcp"),
            "the bridge must reach the seat's own daemon: {argv}"
        );
        assert_eq!(
            fs::read_to_string(&env_out).unwrap(),
            "set",
            "the token must reach the bridge's environment"
        );
        let _ = fs::remove_dir_all(&home);
    }

    /// THE DELEGATION ENTRY IN AGY'S SHARED FILE: the runtime owns the name, so it is installed when the seat's
    /// environment names a script and REMOVED when it does not - a hardcoded path left in a global file is the
    /// defect the rule exists to remove. The line holds no path and starts nothing outside a seat.
    #[test]
    fn the_agy_delegation_entry_follows_the_environment() {
        let home = scratch("agy-deepseek");
        let path = home.join(".gemini/config/mcp_config.json");
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(
            &path,
            r#"{"mcpServers":{"deepseek":{"command":"node","args":["/stale/hardcoded/server.cjs"]},"exa":{"type":"http","url":"http://x"}}}"#,
        )
        .unwrap();
        install_agy_mcp_server(&home, true).unwrap();
        let config: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(&path).unwrap()).unwrap();
        let server = &config["mcpServers"][AGY_DEEPSEEK_SERVER];
        assert_eq!(server["command"], "sh");
        assert_eq!(server["disabled"], false);
        let line = server["args"][1].as_str().unwrap();
        assert!(
            line.contains(r#"[ -n "$DEEPSEEK_MCP_SERVER" ]"#),
            "the path is read at connect time: {line}"
        );
        assert!(
            line.contains(r#"exec node "$DEEPSEEK_MCP_SERVER""#),
            "{line}"
        );
        assert!(
            !line.contains("/stale/hardcoded/server.cjs"),
            "a literal path survived: {line}"
        );
        assert_eq!(
            config["mcpServers"]["exa"]["url"], "http://x",
            "another server was touched"
        );
        install_agy_mcp_server(&home, false).unwrap();
        let config: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(&path).unwrap()).unwrap();
        assert!(
            config["mcpServers"].get(AGY_DEEPSEEK_SERVER).is_none(),
            "absence must leave no entry"
        );
        assert_eq!(
            config["mcpServers"]["exa"]["url"], "http://x",
            "another server was touched"
        );
        assert!(config["mcpServers"][AGY_MCP_SERVER].is_object());
        let _ = fs::remove_dir_all(&home);
    }

    /// An agy start installs BOTH of agy's own files: a seat that comes up guarded but unable to call the cloud is
    /// the defect this pairing is here to prevent.
    #[test]
    fn an_agy_start_installs_the_guard_hook_and_the_mcp_server() {
        let home = scratch("agy-start");
        install_agy_start_files(&home, false).unwrap();
        assert!(
            home.join(".gemini/config/hooks.json").is_file(),
            "no guard hook was installed"
        );
        assert!(
            home.join(".gemini/config/mcp_config.json").is_file(),
            "no MCP server was installed"
        );
        let _ = fs::remove_dir_all(&home);
    }

    /// The seat's own folder is nested one level deeper than the room's skills, and a seat receives share +
    /// room + its own folder, the more specific replacing the shared one, and nothing of another seat.
    #[test]
    fn a_seat_gets_its_nested_skills_and_not_another_seats() {
        let state = scratch("skills-state");
        let tree = scratch("skills-tree");
        let skill = |rel: &str, extra: &str| {
            let dir = tree.join(rel);
            fs::create_dir_all(&dir).expect("dir");
            fs::write(
                dir.join("SKILL.md"),
                "---\nname: x\ndescription: d\n---\nbody\n",
            )
            .expect("skill");
            if !extra.is_empty() {
                fs::write(dir.join(extra), "y").expect("extra");
            }
        };
        skill("share/common", "");
        skill("share/both", "");
        skill("rooms/r/room-skill", "");
        skill("rooms/r/dev/own-skill", "baseline.json");
        skill("rooms/r/dev/both", "");
        skill("rooms/r/other/theirs", "");
        fs::write(
            state.join(".env"),
            format!("{SKILLS_ROOT_ENV}={}\n", tree.display()),
        )
        .expect("env");
        let seat_dir = state.join("rooms/r/seats/dev");
        fs::create_dir_all(seat_dir.join("skills/stale")).expect("stale");
        install_seat_skills(&state, "r", "dev", &seat_dir).expect("install");
        let mut names: Vec<String> = fs::read_dir(seat_dir.join("skills"))
            .expect("skills dir")
            .map(|e| e.expect("entry").file_name().to_string_lossy().to_string())
            .collect();
        names.sort();
        assert_eq!(
            names,
            ["both", "common", "own-skill", "room-skill"],
            "no stale, no other seat's skill"
        );
        assert!(
            seat_dir.join("skills/own-skill/baseline.json").is_file(),
            "files beside SKILL.md go too"
        );
    }

    /// The seat's `<seat>-workflow` body - frontmatter stripped - becomes `<seat dir>/AGENTS.md`, the file the
    /// agent reads at every start; another seat's workflow is not it, and a seat whose workflow skill is gone
    /// keeps no stale copy.
    #[test]
    fn a_seats_workflow_skill_becomes_its_always_loaded_agents_file() {
        let state = scratch("workflow-state");
        let tree = scratch("workflow-tree");
        let skill = |rel: &str, body: &str| {
            let dir = tree.join(rel);
            fs::create_dir_all(&dir).expect("dir");
            fs::write(
                dir.join("SKILL.md"),
                format!("---\nname: x\ndescription: d\n---\n{body}\n"),
            )
            .expect("skill");
        };
        skill("rooms/r/dev/dev-workflow", "# dev - how this seat works");
        skill("rooms/r/other/other-workflow", "# other");
        fs::write(
            state.join(".env"),
            format!("{SKILLS_ROOT_ENV}={}\n", tree.display()),
        )
        .expect("env");
        let seat_dir = state.join("rooms/r/seats/dev");
        install_seat_skills(&state, "r", "dev", &seat_dir).expect("install");
        let agents = seat_dir.join(SEAT_WORKFLOW_FILE);
        assert_eq!(
            fs::read_to_string(&agents).expect("AGENTS.md"),
            "# dev - how this seat works\n"
        );

        fs::remove_dir_all(tree.join("rooms/r/dev/dev-workflow")).expect("drop the workflow");
        install_seat_skills(&state, "r", "dev", &seat_dir).expect("reinstall");
        assert!(
            !agents.exists(),
            "a removed workflow leaves no stale AGENTS.md"
        );
    }

    /// A skill file's mode comes from the COPY, not from this process's umask: the source's bits are decided by
    /// whoever wrote it in the room, and a room that ships `0644` shipped it into every seat's own tree -
    /// measured on a scratch room 2026-10-16, `rooms/<room>/seats/<seat>/skills/**` was the one part of the
    /// state root the umask did not reach. The source's bits are set explicitly here so the case means the same
    /// thing whatever this test process's umask happens to be.
    #[cfg(unix)]
    #[test]
    fn an_installed_skill_keeps_the_owners_bits_and_drops_the_rest() {
        use std::os::unix::fs::PermissionsExt;
        let mode = |path: &Path, bits: u32| {
            fs::set_permissions(path, fs::Permissions::from_mode(bits))
                .expect("set the source mode");
        };
        let perm = |path: PathBuf| fs::metadata(path).expect("mode").permissions().mode() & 0o777;

        let state = scratch("skills-modes");
        let tree = scratch("skills-modes-tree");
        let source = tree.join("share/common");
        fs::create_dir_all(&source).expect("dir");
        fs::write(
            source.join("SKILL.md"),
            "---\nname: x\ndescription: d\n---\nbody\n",
        )
        .expect("skill");
        fs::write(source.join("polluter.sh"), "#!/bin/sh\n").expect("script");
        mode(&source.join("SKILL.md"), 0o644);
        mode(&source.join("polluter.sh"), 0o755);
        fs::write(
            state.join(".env"),
            format!("{SKILLS_ROOT_ENV}={}\n", tree.display()),
        )
        .expect("env");

        let seat_dir = state.join("rooms/r/seats/dev");
        install_seat_skills(&state, "r", "dev", &seat_dir).expect("install");

        let installed = seat_dir.join("skills/common");
        assert_eq!(
            perm(installed.join("SKILL.md")),
            0o600,
            "a copied skill's file must not keep the room's 0644"
        );
        assert_eq!(
            perm(installed.join("polluter.sh")),
            0o700,
            "the owner's execute bit is kept, and nothing beyond it"
        );
    }

    /// The nudge pass enumerates the rooms that are on disk. With no tmux session behind any seat, no seat
    /// is up and nothing is nudged - which is also what makes a daemon restart safe to run this pass.
    #[tokio::test]
    async fn the_nudge_pass_finds_no_seat_in_a_room_that_is_not_up() {
        let dir = scratch("pending-nudges");
        let seat = seat_key("pending-nudges");
        write_spec(&dir, "demo", &seat);
        write_state_file(&dir, "demo", &seat, "idle", "2026-10-13T20:15:30.250Z");
        let supervisor = supervisor_at(&dir);
        {
            let store = supervisor.queue_store("demo").expect("open the rail");
            store
                .create(waiting_row("lead", seat.clone(), "waiting work"))
                .expect("create");
        }
        assert!(supervisor.pending_nudges().is_empty());
        let _ = fs::remove_dir_all(&dir);
    }

    /// The model a seat starts with is recorded in the room's own spec, for the seat's next start, and the
    /// other seats' records survive the rewrite.
    #[tokio::test]
    async fn set_seat_model_is_written_to_the_room_spec() {
        let dir = scratch("set-model");
        write_spec(&dir, "lab", "dev");
        let supervisor = supervisor_at(&dir);

        supervisor
            .set_seat_model("lab", "dev", "  deepseek/deepseek-flash ")
            .unwrap();

        let saved: RoomSpec =
            serde_json::from_str(&fs::read_to_string(dir.join("rooms/lab/spec.json")).unwrap())
                .unwrap();
        assert_eq!(
            saved.seats[0].model.as_deref(),
            Some("deepseek/deepseek-flash")
        );
        assert_eq!(
            saved.seats[0].runtime, "omp",
            "the rest of the seat record is untouched"
        );

        assert!(supervisor
            .set_seat_model("lab", "nobody", "m")
            .unwrap_err()
            .contains("not found"));
        assert!(supervisor
            .set_seat_model("lab", "dev", " ")
            .unwrap_err()
            .contains("empty"));
        let _ = fs::remove_dir_all(&dir);
    }

    /// A room's links are read back from its spec, so the bridge reports the topology the room was started with.
    #[tokio::test]
    async fn room_links_are_read_from_the_room_spec() {
        let dir = scratch("room-links");
        write_spec(&dir, "lab", "dev");
        let supervisor = supervisor_at(&dir);
        assert!(supervisor.get_room_links("lab").unwrap().is_empty());

        let spec_file = dir.join("rooms/lab/spec.json");
        let mut spec: RoomSpec =
            serde_json::from_str(&fs::read_to_string(&spec_file).unwrap()).unwrap();
        spec.links.push(SeatLink {
            from: "dev".into(),
            kind: "delegates_to".into(),
            to: "qa".into(),
        });
        fs::write(&spec_file, serde_json::to_string(&spec).unwrap()).unwrap();

        let links = supervisor.get_room_links("lab").unwrap();
        assert_eq!(
            (
                links[0].from.as_str(),
                links[0].kind.as_str(),
                links[0].to.as_str()
            ),
            ("dev", "delegates_to", "qa")
        );
        assert!(supervisor
            .get_room_links("ghost")
            .unwrap_err()
            .contains("cannot read room spec"));
        let _ = fs::remove_dir_all(&dir);
    }

    /// A fresh restart's clearing removes the seat's saved agent sessions and leaves other seats' alone; nothing
    /// saved is not a failure. It is tested on its own because the restart around it stops a tmux session, which
    /// shares a process-wide `TMUX_TMPDIR` with other tests and so cannot make this assertion reliable.
    #[tokio::test]
    async fn clearing_saved_sessions_drops_one_seats_and_tolerates_none() {
        let dir = scratch("clear-sessions");
        write_spec(&dir, "lab", "dev");
        let mine = dir.join("rooms/lab/seats/dev/sessions");
        let other = dir.join("rooms/lab/seats/other/sessions");
        for sessions in [&mine, &other] {
            fs::create_dir_all(sessions).unwrap();
            fs::write(sessions.join("a.jsonl"), "x").unwrap();
        }
        let supervisor = supervisor_at(&dir);

        supervisor.clear_saved_sessions("lab", "dev").unwrap();
        assert!(!mine.exists(), "the seat's own sessions are gone");
        assert!(
            other.join("a.jsonl").exists(),
            "another seat's sessions are untouched"
        );
        supervisor.clear_saved_sessions("lab", "dev").unwrap();
        let _ = fs::remove_dir_all(&dir);
    }

    /// The facts a claim makes, with the three fields this defect turns on: what the seat's document says, who
    /// published it, and whether that publisher is still there.
    fn claim_facts(
        word: Option<&str>,
        publisher_pid: Option<u32>,
        publisher_alive: Option<bool>,
    ) -> SeatFacts {
        SeatFacts {
            alive: true,
            pane_kept: false,
            published_state: word.map(str::to_string),
            activity: ActivityWord::from_state_document(None),
            last_active: None,
            publisher_pid,
            publisher_alive,
            waiting_qitem_ids: Vec::new(),
        }
    }

    /// A SEAT'S DOCUMENT CARRIES ITS AUTHOR, which is what makes a stale claim detectable at all: the pid is
    /// written by `write_state` and read by `published_claim`, so the pair is asserted as a pair - a document that
    /// names no publisher is a claim nothing on this side can check.
    #[test]
    fn a_seat_state_document_names_the_process_that_published_it() {
        let dir = scratch("state-pid");
        let path = dir.join("dev.state.json");
        crate::runner::write_state(&path, "working", "a turn is running", 0).expect("write state");

        let doc: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(&path).unwrap()).unwrap();
        let (word, pid) = published_claim(Some(&doc));
        assert_eq!(word.as_deref(), Some("working"));
        assert_eq!(
            pid,
            Some(std::process::id()),
            "the document must name the process that wrote it"
        );
        assert_eq!(
            pid.and_then(process_is_alive),
            Some(true),
            "and that process is this test"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// DEFECT 2, and the half of the daemon's own promise that the code did not keep: a claim whose publisher is
    /// gone. The measured case is a pane that outlived its agent - the file keeps `working`, and every reader of
    /// that document, this status included, is told a seat is working whose agent was killed.
    #[test]
    fn an_observed_death_names_the_claim_whose_publisher_is_gone() {
        let why = observed_death(&claim_facts(Some("working"), Some(4242), Some(false)), true)
            .expect("a claim whose publisher is gone is the death this side can observe");
        assert!(
            why.contains("pid 4242"),
            "the sentence must name the publisher: {why}"
        );
        assert!(
            why.contains("working"),
            "the sentence must name the stale word: {why}"
        );
    }

    /// AND IT SAYS NOTHING IN EVERY OTHER CASE, which is what keeps it from killing live seats: a live publisher; a
    /// host that cannot answer (`None` from procfs is unverifiable, never `false`); a death the runner published
    /// itself; a seat whose pane is gone; a runtime that publishes no readiness document; and a seat with no
    /// document at all, which is the one shape that names no publisher.
    #[test]
    fn an_observed_death_is_silent_when_nothing_contradicts_the_claim() {
        let no_pane = {
            let mut facts = claim_facts(Some("working"), Some(4242), Some(false));
            facts.alive = false;
            facts
        };
        let cases: [(&str, SeatFacts, bool); 6] = [
            (
                "a runner that is alive",
                claim_facts(Some("working"), Some(4242), Some(true)),
                true,
            ),
            (
                "a host that cannot answer",
                claim_facts(Some("working"), Some(4242), None),
                true,
            ),
            (
                "a death the runner published itself",
                claim_facts(Some("agent_exited"), Some(4242), Some(false)),
                true,
            ),
            ("a pane that is gone", no_pane, true),
            (
                "a runtime that publishes no readiness document",
                claim_facts(Some("working"), Some(4242), Some(false)),
                false,
            ),
            (
                "a seat that published nothing",
                claim_facts(None, None, None),
                true,
            ),
        ];
        for (what, facts, publishes) in cases {
            assert!(
                observed_death(&facts, publishes).is_none(),
                "{what} must not be reported as an observed death"
            );
        }
    }

    /// AND THE CORRECTION LANDS IN THE FILE, not only in this function's answer: the defect is that the DOCUMENT
    /// claims a live seat, and the readers of that document are not all `get_room_status` - the client's watch
    /// reads it, and so does anyone with `cat`. Corrected once, and the next read sees a death from a live
    /// publisher, so the correction is not rewritten on every status call.
    #[test]
    fn the_observed_death_reaches_the_seat_state_file_and_stays_there() {
        let dir = scratch("state-corrected");
        let path = dir.join("reviewer.state.json");
        crate::runner::write_state(&path, "working", "a turn is running", 0)
            .expect("seed the stale claim");

        let why = observed_death(&claim_facts(Some("working"), Some(4242), Some(false)), true)
            .expect("observed");
        crate::runner::write_state(&path, "agent_exited", &why, 0).expect("correct the claim");

        let doc: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(&path).unwrap()).unwrap();
        assert_eq!(
            doc["state"], "agent_exited",
            "the file must carry the death, not the stale word"
        );
        assert!(
            doc["classification"]
                .as_str()
                .is_some_and(|c| c.contains("pid 4242")),
            "the file must carry the reason the daemon observed: {doc}"
        );

        // AND IT STAYS CORRECTED: read the file back the way the status does, and nothing contradicts it any more
        // - the publisher is the process that wrote the correction, and it is alive.
        let (word, pid) = published_claim(Some(&doc));
        let reread = SeatFacts {
            published_state: word,
            publisher_pid: pid,
            publisher_alive: pid.and_then(process_is_alive),
            ..claim_facts(None, None, None)
        };
        assert!(
            observed_death(&reread, true).is_none(),
            "a corrected file must not be corrected again"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// A STATUS READ IS NOT AN OBSERVATION OF A DEATH, and it must not leave one on disk. A seat that was
    /// never started has no pane and no document; the status must still answer (with its own sentence) and
    /// must NOT write `run/<seat>.state.json`. Measured 2026-10-17 06:16:38Z: one read over such a seat
    /// created `agent_socket_closed` signed with the DAEMON's pid, so a later reader (the nudge gate) took a
    /// reader's assertion for the seat's own word.
    #[tokio::test]
    async fn a_status_read_over_a_seat_that_never_ran_writes_no_state_document() {
        let dir = scratch("status-never-ran");
        let room = format!("status-never-ran-{}", std::process::id());
        let key = seat_key("status-never-ran");
        write_spec(&dir, &room, &key);
        let supervisor = supervisor_at(&dir);
        let state_path = dir
            .join("rooms")
            .join(&room)
            .join("run")
            .join(format!("{key}.state.json"));
        assert!(
            !state_path.exists(),
            "the fixture must start with no document"
        );

        let status = supervisor
            .get_room_status(&room)
            .expect("status")
            .into_iter()
            .next()
            .expect("one seat");
        assert_eq!(status.state, SeatLifecycleState::Stopped);
        assert!(
            status
                .detail
                .as_deref()
                .is_some_and(|d| d.contains("agent_socket_closed")),
            "the reader still REPORTS what it saw: {:?}",
            status.detail
        );
        assert!(
            !state_path.exists(),
            "a reader that observed no death must not manufacture the seat's document: {:?}",
            fs::read_to_string(&state_path)
        );
        // ...and a second read is no different: the assertion above is not a one-read accident.
        let _ = supervisor.get_room_status(&room).expect("status");
        assert!(
            !state_path.exists(),
            "and a repeated read writes nothing either"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    /// AND `None` IS NOT `false`: a host that cannot answer must keep the verdict it would have had, so the first
    /// branch of `process_is_alive` answers `None` rather than inventing a death from a missing directory. On a box
    /// with procfs the distinction it can show is the other end of it: a live pid is `Some(true)`, and a pid that
    /// cannot exist is `Some(false)`.
    #[test]
    fn process_liveness_separates_a_live_pid_from_one_that_cannot_exist() {
        if !Path::new("/proc/self").exists() {
            assert_eq!(
                process_is_alive(std::process::id()),
                None,
                "without procfs the answer is unverifiable, not dead"
            );
            return;
        }
        assert_eq!(
            process_is_alive(std::process::id()),
            Some(true),
            "this test process is alive"
        );
        assert_eq!(
            process_is_alive(u32::MAX),
            Some(false),
            "a pid that cannot exist is not alive"
        );
    }

    /// DEFECT 2, THROUGH THE PATH THAT ACTUALLY RUNS IT. A real pane, a seat whose document claims a turn is
    /// running, and a publisher that is not there any more - which is what a SIGHUPed agent leaves behind - read
    /// through `get_room_status`, the surface the client's `ps` and the daemon's own nudge pass read.
    ///
    /// TWO THINGS MUST COME OUT OF IT: the seat is not `running`, and the DOCUMENT stops claiming a live seat,
    /// because the readers of that document are not all this function. This is the case as it was measured: the
    /// reviewer's file said `working` for a session that had been gone for 42 minutes.
    #[tokio::test]
    async fn the_status_corrects_a_claim_whose_publisher_is_gone() {
        let _server = crate::pty::test_servers::own("status-stale");
        let dir = scratch("status-stale");
        let room = format!("status-stale-{}", std::process::id());
        let key = seat_key("status-stale");
        let supervisor = supervisor_at(&dir);

        // A runtime that publishes a readiness document, and a room spec on disk, since the status reads the spec
        // rather than any in-memory copy.
        let spec = startable_spec(&room, &key, "omp");
        let room_dir = dir.join("rooms").join(&room);
        fs::create_dir_all(room_dir.join("run")).unwrap();
        fs::write(
            room_dir.join("spec.json"),
            serde_json::to_string(&spec).unwrap(),
        )
        .unwrap();

        // A REAL pane under a private tmux server: the seat is up, and its agent is not.
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(&session, &room_dir, "sleep 30", &HashMap::new())
            .expect("pane");
        let state_path = room_dir.join("run").join(format!("{key}.state.json"));
        crate::runner::write_state(&state_path, "working", "a turn is running", 0)
            .expect("the stale claim");
        // The publisher is then made a pid that cannot exist - the one way to fake a gone runner without killing
        // one - while the pane stays up.
        let mut doc: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(&state_path).unwrap()).unwrap();
        doc["pid"] = serde_json::json!(u32::MAX);
        fs::write(&state_path, serde_json::to_string(&doc).unwrap()).unwrap();

        let statuses = supervisor.get_room_status(&room).expect("status");
        let status = statuses
            .iter()
            .find(|s| s.seat == key)
            .expect("the seat's row");
        assert_eq!(
            status.state,
            SeatLifecycleState::Dead,
            "a claim whose publisher is gone is not `running`: {:?}",
            status.detail
        );
        let detail = status.detail.clone().unwrap_or_default();
        assert!(
            detail.contains("gone") && detail.contains("working"),
            "the row must say what happened rather than repeat the word it could not trust: {detail}"
        );

        let after: serde_json::Value =
            serde_json::from_str(&fs::read_to_string(&state_path).unwrap()).unwrap();
        assert_eq!(
            after["state"], "agent_exited",
            "the document itself must stop claiming a live seat: {after}"
        );

        let _ = TerminalManager::stop_session(&session);
        let _ = fs::remove_dir_all(&dir);
    }

    /// A PARKED SEAT IS NOT HANDED WORK AT THE RUNTIME TIER EITHER.
    ///
    /// The client refuses its own routes, but a client is only a caller: `SendText` (and this daemon's own
    /// `send` command), `RestartSeat` and a room start all cross this supervisor. The record the client's
    /// safeguard leaves in the seat's own directory is what each of them reads, and each refusal names what
    /// did not happen.
    ///
    /// RED before the gate: the two delivery assertions saw a transport answer (no pane for the session) and
    /// the start assertion saw a seat prepared and a pane started.
    #[tokio::test]
    async fn a_parked_seat_is_refused_by_the_runtimes_own_routes() {
        let dir = scratch("parked-routes");
        let room = format!("parked-{}", std::process::id());
        let key = seat_key("parked");
        let spec = startable_spec(&room, &key, "sleep 30");
        let supervisor = supervisor_at(&dir);
        let seat_dir = dir.join("rooms").join(&room).join("seats").join(&key);
        fs::create_dir_all(&seat_dir).expect("seat dir");
        fs::write(
            dir.join("rooms").join(&room).join("spec.json"),
            serde_json::to_string(&spec).expect("spec json"),
        )
        .expect("spec file");
        let session = RoomSupervisor::session_name(&room, &key);

        // CONTROL: with no record, a park is not what answers. The delivery still fails - there is no pane for
        // this session - but that is a transport answer, and a test that cannot tell them apart proves nothing.
        let before = supervisor.deliver_text(&session, "hi", &crate::runner::Origin::default());
        assert!(
            !matches!(&before, Err(e) if e.contains("PARKED")),
            "an unparked seat must not be refused as parked: {before:?}"
        );

        // The record the client's safeguard writes, verbatim in shape.
        fs::write(
            seat_dir.join("parked.json"),
            r#"{"seat":"parked","reason":"spent 1.50 USD in the last 24h, over the 1.00 USD ceiling","at":"2026-10-16T17:23:51Z","cost_usd":1.5,"calls":1}"#,
        )
        .expect("record");

        let sent = supervisor
            .deliver_text(&session, "hi", &crate::runner::Origin::default())
            .expect_err("a send must refuse");
        assert!(
            sent.contains("PARKED")
                && sent.contains("cost_usd=1.50")
                && sent.contains("Nothing was sent"),
            "deliver_text: {sent}"
        );

        let restarted = supervisor
            .restart_seat(&room, &key, false, None)
            .expect_err("a restart must refuse");
        assert!(
            restarted.contains("PARKED") && restarted.contains("nothing was restarted"),
            "restart_seat: {restarted}"
        );

        // A room start funnels through `start_seat`, so its refusal rides the room's EXISTING rule for a seat
        // that did not come up: reported by name with the park as the reason, and a room whose every seat is
        // parked starts no seat and says so.
        let started = supervisor
            .start_room(spec.clone())
            .expect_err("a room whose only seat is parked starts no seat");
        assert!(started.contains("PARKED"), "start_room: {started}");
        // ...and nothing of that start ran: a seat prepared for a start has its mcp.json written.
        assert!(
            !seat_dir.join("mcp.json").exists(),
            "a parked seat must not be prepared for a start"
        );

        // AND THE PARK LIFTS WHEN THE RECORD GOES, IN THIS PROCESS. The record's EXISTENCE is the park (see
        // park.rs), nothing above caches that answer, and no restart is needed to stop refusing - an exit that
        // only takes effect once the daemon is replaced is not an exit, and a park remembered past the record's
        // removal is how a seat that was let back in stays out. This is the runtime's half of the round trip
        // the way back depends on: the same route that just refused answers for itself again.
        fs::remove_file(seat_dir.join("parked.json")).expect("the record is the whole park");
        let after = supervisor.deliver_text(&session, "hi", &crate::runner::Origin::default());
        assert!(
            !matches!(&after, Err(e) if e.contains("PARKED")),
            "with the record gone the same send must answer for itself again: {after:?}"
        );

        let _ = fs::remove_dir_all(&dir);
    }
}
