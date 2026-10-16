use crate::hooks::{HookBus, SeatEvent};
use crate::pty::TerminalManager;
use crate::queue::{ListFilter, QueueStore};
use crate::types::*;
use chrono::{DateTime, Utc};
use parking_lot::Mutex;
use std::collections::HashMap;
use std::fs;
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
    match published_state(path).as_deref() {
        Some(state) if DEATH_STATES.contains(&state) => false,
        _ => true,
    }
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
    /// The seat's tmux session is up.
    alive: bool,
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
    pub qitem_ids: Vec<String>,
}

/// The names a rail row might use to address one seat, because the rail's rows have been written with
/// more than one spelling and a spelling the join missed reads as "nothing waiting" - the exact lie the
/// alert exists to prevent.
///
/// The three that exist in this tree's world: the session name this runtime derives
/// (`agenthub--<room>--<seat>`, the same one `SendTextToSeat` and the pane's `AGENTHUB_SESSION_NAME` use),
/// the bare seat key (what the design's own room recipe named and what a row written by hand usually
/// says), and the rig orchestrator session name the live rig's rows carry (`<rig>-<seat>@<rig>`, where this
/// runtime's room IS the rig name). A row written by a seat on the live rail is addressed with the third.
fn addressed_names(room: &str, session: &str, seat: &str) -> [String; 3] {
    [
        session.to_string(),
        seat.to_string(),
        format!("{room}-{seat}@{room}"),
    ]
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

/// Make sure agy's global `hooks.json` carries the seat guard, and touch nothing else in it: the file also holds
/// other tools' hooks. A file that is not a JSON object is an error rather than a file to overwrite.
fn install_agy_guard_hook(home: &Path) -> Result<(), String> {
    let path = home.join(".gemini").join("config").join("hooks.json");
    let mut hooks = match fs::read_to_string(&path) {
        Ok(text) => serde_json::from_str::<serde_json::Value>(&text)
            .ok()
            .filter(serde_json::Value::is_object)
            .ok_or_else(|| format!("{} is not a JSON object; the agy seat guard was not installed", path.display()))?,
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

/// The variable that names the client's skill tree (`share/`, `rooms/<room>/`, `rooms/<room>/<seat>/`). It is
/// read from the state root's `.env`, else from the daemon's own environment.
const SKILLS_ROOT_ENV: &str = "AGENTHUB_CLIENT_SKILLS";

/// Install the seat's skills into `<seat dir>/skills/<name>`, which is where omp (its agent dir is the seat
/// dir) loads them from: the shared skills, the room's, and the seat's own folder, the more specific one
/// replacing a shared skill of the same name. The directory is runtime-owned, so it is rebuilt every start
/// and a skill removed from the tree does not linger. No tree configured means no skills, which is not an
/// error: a machine with no tree has nothing to install.
fn install_seat_skills(state_dir: &Path, room: &str, seat: &str, seat_dir: &Path) -> Result<(), String> {
    let root = env_file_vars(state_dir)
        .remove(SKILLS_ROOT_ENV)
        .or_else(|| std::env::var(SKILLS_ROOT_ENV).ok())
        .filter(|v| !v.is_empty());
    let Some(root) = root else { return Ok(()) };
    let target = seat_dir.join("skills");
    if target.exists() {
        fs::remove_dir_all(&target).map_err(|e| format!("cannot clear {}: {e}", target.display()))?;
    }
    for skill in crate::skills::SkillResolver::new(root).resolve_for_seat(room, seat) {
        let Some(source) = skill.path.parent() else { continue };
        copy_dir(source, &target.join(&skill.name))
            .map_err(|e| format!("cannot install the skill {}: {e}", skill.name))?;
    }
    Ok(())
}

/// Copy a directory tree, replacing what is at `to`. Every file under a skill goes with it (a skill may
/// carry baselines and references beside its SKILL.md).
///
/// THE COPY DECIDES THESE MODES, NOT THE PROCESS'S UMASK: `fs::copy` carries the SOURCE's permission bits
/// across, so a skill that ships `0644` in the rig landed `0644` in the seat's own tree whatever the umask
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
            [q @ (b'"' | b'\''), .., last] if q == last && value.len() >= 2 => &value[1..value.len() - 1],
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
    env.insert("PI_CODING_AGENT_DIR".into(), seat_dir.to_string_lossy().to_string());
    env.insert(
        "AGENTHUB_RUNTIME_SOCKET".into(),
        runtime_socket.to_string_lossy().to_string(),
    );
    if let Some(m) = &seat.model {
        env.insert("AGENTHUB_MODEL".into(), m.clone());
    }
    // The connector credential never enters a seat's environment, and the filter sits HERE - the one place a
    // seat's environment is decided - rather than downstream: this map IS the pane's `-e` list, so a key
    // dropped only at that point is a key that had already reached the seat's contract. Nothing a seat does
    // reads the connector token (a seat addresses its own daemon through `AGENTHUB_URL`/`AGENTHUB_TOKEN`).
    env.retain(|k, _| !crate::ENV_DENY_NAMES.contains(&k.as_str()));
    env
}

impl RoomSupervisor {
    pub fn new(base_state_dir: impl Into<PathBuf>, hook_bus: HookBus, runtime_socket: impl Into<PathBuf>) -> Self {
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
    /// The room IS the rig here - `room_dir`, the socket's own rooms and whoami's `rigName` all say so -
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
        info!("starting room {} with {} seats", room_name, spec.seats.len());

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
        let spec_json = serde_json::to_string_pretty(&final_spec).map_err(|e| format!("serialize spec: {e}"))?;
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
                    statuses.push(Self::failed_seat_status(&self.base_state_dir, &room_name, seat, e));
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
            transcript_path: seat_transcript_path(&base_state_dir.join("rooms").join(room), &seat.seat_key)
                .map(|p| p.to_string_lossy().to_string()),
            // A seat that never came up has nothing waiting *for it* - it is not the seat the rail's rows
            // name - so there is no join to report.
            alert: None,
        }
    }

    /// Start a single seat.
    pub fn start_seat(&self, room: &str, seat: &SeatSpec, spec: &RoomSpec) -> Result<SeatStatus, String> {
        let session_name = Self::session_name(room, &seat.seat_key);
        let room_dir = self.room_dir(room);
        let seat_dir = room_dir.join("seats").join(&seat.seat_key);
        fs::create_dir_all(&seat_dir).map_err(|e| format!("cannot create seat dir: {e}"))?;
        fs::write(seat_dir.join("mcp.json"), SEAT_MCP_CONFIG)
            .map_err(|e| format!("cannot write the seat's mcp.json: {e}"))?;
        let hook_dir = seat_dir.join("hooks").join("pre");
        fs::create_dir_all(&hook_dir).map_err(|e| format!("cannot create the seat's hook dir: {e}"))?;
        fs::write(hook_dir.join("seat-guard.ts"), SEAT_TOOL_HOOK)
            .map_err(|e| format!("cannot write the seat's tool hook: {e}"))?;
        install_seat_skills(&self.base_state_dir, room, &seat.seat_key, &seat_dir)?;
        if matches!(AgentRuntime::from_str_loose(&seat.runtime), AgentRuntime::Agy) {
            let home = std::env::var_os("HOME").ok_or("HOME is not set, so agy's hooks.json cannot be found")?;
            install_agy_guard_hook(Path::new(&home))?;
        }

        // Determine working directory
        let cwd_str = seat.cwd.as_deref().or(spec.cwd.as_deref()).unwrap_or(".");
        let cwd = PathBuf::from(cwd_str);

        // Build command line according to runtime. An omp seat's pane runs THIS binary as a runner, which
        // spawns the agent and owns its channel (design §1: the runner replaces `pi-runner.js`); the other
        // runtimes keep their own command until their native rpc paths exist.
        let runtime = AgentRuntime::from_str_loose(&seat.runtime);
        let cmd = match &runtime {
            AgentRuntime::Omp | AgentRuntime::ClaudeCode => {
                let self_exe = std::env::current_exe()
                    .map_err(|e| format!("cannot resolve this binary's path: {e}"))?;
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
        let env = seat_env(seat, room, &session_name, &seat_dir, &self.runtime_socket, &self.base_state_dir);

        // WHEN THIS GENERATION'S PANE WAS CREATED. The readiness proof below is a file the previous
        // generation's teardown also writes into, so existence alone cannot separate a handshake from an
        // inherited death record: this instant is what does. Taken BEFORE the launch, so a state file written
        // by this generation's runner is necessarily newer than it.
        let launched_at = std::time::SystemTime::now();

        // Launch tmux session
        let pid = TerminalManager::launch_session(&session_name, &cwd, &cmd, &env)?;

        // A launch is not liveness. The pid tmux just handed back is a **record** of the session it created,
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
                return Err(format!(
                    "seat {room}/{} died before it reported readiness (its pane is gone and no ready frame ever \
                     arrived: waited up to {}; pid {pid} was the session record tmux returned)",
                    seat.seat_key,
                    if signal.is_some() {
                        format!("{SEAT_READY_WINDOW_SECS}s")
                    } else {
                        format!("{NON_OMP_READY_GRACE_MS}ms")
                    }
                ));
            }
            // A DEATH THE SEAT ITSELF RECORDED ENDS THE WAIT NOW, not at the deadline. The runner publishes
            // `agent_socket_closed` and friends when it survives to see the agent go; a pane that outlives its
            // agent is exactly the case liveness cannot see. Waiting the window out for a seat that has already
            // said it is dead is what would make a longer window cost a real verdict, so this clause is the
            // reason `SEAT_READY_WINDOW_SECS` can cover a cold first launch.
            if let Some(path) = signal.as_ref() {
                if let Some(state) = fresh_death_state(path, launched_at) {
                    return Err(format!(
                        "seat {room}/{} died before it reported readiness: its own record says `{state}`, written \
                         after this launch; not reporting it as running",
                        seat.seat_key
                    ));
                }
            }
            if std::time::Instant::now() >= deadline {
                match signal {
                    Some(path) => {
                        return Err(readiness_refusal(room, &seat.seat_key, &path, launched_at))
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

        let status = SeatStatus {
            room: room.to_string(),
            seat: seat.seat_key.clone(),
            session_name: session_name.clone(),
            runtime: seat.runtime.clone(),
            model: seat.model.clone(),
            pid: Some(pid),
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
            pid,
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
        let content = fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec: {e}"))?;
        let mut spec: RoomSpec = serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        let seat_spec = spec
            .seats
            .iter_mut()
            .find(|s| s.seat_key == seat)
            .ok_or_else(|| format!("seat {seat} not found in room {room}"))?;
        seat_spec.model = Some(model.to_string());
        // Compute the document before the file is opened for writing: a failed serialize must not leave
        // the room's only spec truncated.
        let spec_json = serde_json::to_string_pretty(&spec).map_err(|e| format!("serialize spec: {e}"))?;
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
    pub fn restart_seat(&self, room: &str, seat: &str, fresh: bool) -> Result<SeatStatus, String> {
        self.stop_seat(room, seat)?;
        if fresh {
            self.clear_saved_sessions(room, seat)?;
        }
        let room_dir = self.room_dir(room);
        let spec_file = room_dir.join("spec.json");
        let content = fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec: {e}"))?;
        let spec: RoomSpec = serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        let seat_spec = spec.seats.iter().find(|s| s.seat_key == seat)
            .ok_or_else(|| format!("seat {seat} not found in room {room}"))?;
        self.start_seat(room, seat_spec, &spec)
    }

    /// Get all seat statuses for a room.
    /// The links the room's spec declares between its seats. A room with none answers an empty list.
    pub fn get_room_links(&self, room: &str) -> Result<Vec<SeatLink>, String> {
        let content = fs::read_to_string(self.room_dir(room).join("spec.json"))
            .map_err(|e| format!("cannot read room spec: {e}"))?;
        let spec: RoomSpec = serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        Ok(spec.links)
    }

    pub fn get_room_status(&self, room: &str) -> Result<Vec<SeatStatus>, String> {
        let room_dir = self.room_dir(room);
        let spec_file = room_dir.join("spec.json");
        let content = fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec: {e}"))?;
        let spec: RoomSpec = serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;

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
            let state_path = room_dir.join("run").join(format!("{}.state.json", seat.seat_key));
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
                (true, true, false, false) if facts.published_state.is_none() => SeatLifecycleState::Starting,
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
                    _ => {
                        let reason = "agent_socket_closed: the pane ended without publishing a death";
                        let _ = crate::runner::write_state(
                            &state_path,
                            "agent_socket_closed",
                            "the pane ended without publishing a death",
                            0,
                        );
                        Some(reason.to_string())
                    }
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
                    rig_name: name.clone(),
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
    pub fn deliver_text(&self, session: &str, text: &str) -> Result<serde_json::Value, String> {
        let (room, seat) = self.resolve_seat(session)?;
        let canonical = Self::session_name(&room, &seat);
        if !self.seat_runtime(&room, &seat)?.has_runner() {
            let outcome = TerminalManager::send_text(&canonical, text)?;
            return Ok(serde_json::json!({"delivered": canonical, "outcome": outcome}));
        }
        let id = format!("send-{}-{}", std::process::id(), Utc::now().timestamp_millis());
        let answer = crate::runner::send_to_seat(
            &self.base_state_dir,
            &canonical,
            &serde_json::json!({"id": id, "type": "text", "text": text}),
            Some((text.as_bytes(), true)),
            true,
        )?;
        Ok(serde_json::json!({"delivered": canonical, "outcome": "relayed", "runner": answer}))
    }

    fn seat_runtime(&self, room: &str, seat: &str) -> Result<AgentRuntime, String> {
        let spec_file = self.room_dir(room).join("spec.json");
        let content = fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec for {room}: {e}"))?;
        let spec: RoomSpec = serde_json::from_str(&content).map_err(|e| format!("parse spec for {room}: {e}"))?;
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

    /// Everything one seat's status row is built from, read once: whether its session is up, the runner's
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
        let alive = TerminalManager::is_session_alive(&session);
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
        let last_active = published
            .as_ref()
            .and_then(|v| v.get("updated_at").and_then(|t| t.as_str()))
            .and_then(|t| DateTime::parse_from_rfc3339(t).ok())
            .map(|t| t.with_timezone(&Utc));

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
    /// The rail's rows are written by the client's `room queue` verbs, whose `--to` is a free session
    /// string, and the two spellings in use disagree: the live rig's session name is
    /// `<rig>-<seat>@<rig>` while this runtime derives `agenthub--<room>--<seat>`, and a row a human wrote
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
            .filter(|row| names.iter().any(|name| name == &row.destination_session))
            .map(|row| row.qitem_id)
            .collect())
    }

    /// Every seat the periodic nudge pass should act on: it is up, the rail is holding active rows for it,
    /// and its runner's own state file says `idle` - the runner's word for "no turn is in flight".
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
                if status.state != SeatLifecycleState::Running {
                    continue;
                }
                let Some(alert) = status.alert else {
                    continue;
                };
                if !self.seat_is_idle(&room, &status.seat) {
                    continue;
                }
                out.push(PendingNudge {
                    room: room.clone(),
                    seat: status.seat,
                    session_name: status.session_name,
                    qitem_ids: alert.waiting_qitem_ids,
                });
            }
        }
        out
    }

    /// Whether a seat's own state file says `idle`: exactly the runner's word for "no turn in flight".
    ///
    /// Only that word counts. A seat that published `retrying`, `needs_input` or `blocked` is occupied or
    /// stuck, and a nudge would either collide with a live turn or talk past a seat that already gave up;
    /// a seat that published nothing (no state file) has not said it is ready to hear one, so it is not
    /// `idle` either. This is the one reader that looks at the state file a second time - once here and
    /// once inside `get_room_status`'s join - and it is deliberate: the idle gate is a *nudge* rule, not a
    /// status rule (the alert stands whether the seat is mid-turn or quiet), so it must not be folded into
    /// the status row and change what the status says.
    fn seat_is_idle(&self, room: &str, seat: &str) -> bool {
        let path = self
            .room_dir(room)
            .join("run")
            .join(format!("{seat}.state.json"));
        fs::read_to_string(path)
            .ok()
            .and_then(|t| serde_json::from_str::<serde_json::Value>(&t).ok())
            .and_then(|v| v.get("state").and_then(|s| s.as_str()).map(str::to_string))
            .is_some_and(|state| state == "idle")
    }

    /// Generate the exact `whoami` payload for an active seat session (Rule 1 parity).
    pub fn resolve_whoami(&self, query_session: Option<&str>, _pid: Option<u32>) -> Result<WhoamiResponse, String> {
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
                                let peers: Vec<WhoamiPeer> = spec.seats.iter()
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
                                                session_name: Self::session_name(&spec.room, &link.to),
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
                                                session_name: Self::session_name(&spec.room, &link.from),
                                            }),
                                        });
                                    }
                                }

                                let transcript_str = seat_transcript_path(&entry.path(), &seat.seat_key)
                                    .map(|p| p.to_string_lossy().to_string());

                                return Ok(WhoamiResponse {
                                    resolved_by: "session_name".into(),
                                    identity: WhoamiIdentity {
                                        rig_name: spec.room.clone(),
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
                                        tail_command: transcript_str.as_ref().map(|p| format!("tail -f {p}")),
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
                    rig_name: r,
                    member_id: s.clone(),
                    runtime,
                    session_name: session,
                    logical_id: s,
                    role,
                    model,
                },
                peers: vec![],
                edges: WhoamiEdges { outgoing: vec![], incoming: vec![] },
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
    /// reads (`clientsync.parseRigPsSessions`) is present and spelled as the CLI spells it - an entry that
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
            entry.rig_name, room,
            "the room IS the rig for a native room"
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
            "rigName",
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
            all.iter().any(|e| e.rig_name == room_a) && all.iter().any(|e| e.rig_name == room_b),
            "both hosted rooms are listed: {all:?}"
        );

        let one = supervisor.list_sessions(Some(room_a)).expect("one room");
        assert_eq!(one.len(), 1, "a named room narrows the list: {one:?}");
        assert_eq!(one[0].rig_name, room_a);

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

        let sessions = state_dir.join("rooms").join(room).join("seats").join(&seat).join("sessions");
        fs::create_dir_all(&sessions).expect("sessions dir");
        fs::write(sessions.join("2026-10-14T15-43-38-600Z_aaa.jsonl"), "old\n").expect("old session");
        fs::write(sessions.join("2026-10-14T16-00-00-000Z_bbb.jsonl"), "one\ntwo\nthree\n").expect("new session");
        fs::write(sessions.join("notes.txt"), "not a session").expect("noise");

        let whole = supervisor.read_transcript(&seat, None).expect("whole log");
        assert_eq!(whole.content, "one\ntwo\nthree\n");
        assert!(whole.path.ends_with("2026-10-14T16-00-00-000Z_bbb.jsonl"), "{}", whole.path);
        let status = supervisor.get_room_status(room).expect("status");
        assert_eq!(status[0].transcript_path.as_deref(), Some(whole.path.as_str()));
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

    /// A scratch state dir under the system temp dir, unique per test, removed by the caller.
    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "agenthub-supervisor-{name}-{}",
            std::process::id()
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).expect("scratch dir");
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

        let state_dir = scratch("deliver-one-frame");
        let seat = seat_key("deliver");
        write_spec(&state_dir, "deliver", &seat);
        let run_dir = state_dir.join("rooms").join("deliver").join("run");
        fs::create_dir_all(&run_dir).expect("run dir");
        let listener = UnixListener::bind(run_dir.join(format!("{seat}.sock"))).expect("mailbox");
        let runner = std::thread::spawn(move || {
            let (stream, _) = listener.accept().expect("a frame");
            let mut line = String::new();
            BufReader::new(stream).read_line(&mut line).expect("one line");
            let frame: serde_json::Value = serde_json::from_str(line.trim()).expect("a frame");
            let reply = frame["replyTo"].as_str().expect("replyTo").to_string();
            let mut back = UnixStream::connect(reply).expect("reply socket");
            writeln!(back, "{}", serde_json::json!({"relayed": "prompt"})).expect("reply");
            frame
        });

        let text = "From: principal@demo\nTo: dev\n---\nthe body";
        let answer = supervisor_at(&state_dir).deliver_text(&seat, text).expect("delivered");
        let frame = runner.join().expect("runner thread");
        assert_eq!(frame["type"], "text");
        assert_eq!(frame["text"], text, "the whole message is one frame");
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
            SeatEvent::SessionTerminated { room, seat, exit_code, .. } => {
                assert_eq!((room.as_str(), seat.as_str(), exit_code), ("demo", "dev", None));
            }
            other => panic!("expected SessionTerminated, got {other:?}"),
        }
        let _ = fs::remove_dir_all(&state_dir);
    }

    fn write_spec(state_dir: &Path, room: &str, seat: &str) {
        let room_dir = state_dir.join("rooms").join(room);
        fs::create_dir_all(&room_dir).expect("room dir");
        let spec = serde_json::json!({
            "room": room,
            "seats": [{"seat_key": seat, "seat_type": "developer", "runtime": "omp"}],
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
        let spec = startable_spec(&room, &key, "sleep 30");
        let supervisor = supervisor_at(&dir);

        let status = supervisor
            .start_seat(&room, &spec.seats[0], &spec)
            .expect("a live non-omp pane must be called running once its grace has passed");
        assert_eq!(status.state, SeatLifecycleState::Running);
        assert!(TerminalManager::is_session_alive(&RoomSupervisor::session_name(
            &room, &key
        )));
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

        let _ = supervisor.stop_seat(&room, &key);
        let _ = fs::remove_dir_all(&dir);
    }

    /// The freshness the daemon reports is the seat's OWN (`updated_at`, the runner's stamp), not the
    /// `None` this used to be hardcoded to and not the launch instant `start_seat` records. Fails without
    /// the join: the assertion below reads `None` on the old code.
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

    /// The join's rule, on the facts alone: up + rows waiting = an alert carrying the kind, how long the
    /// seat has been quiet, and the rows' ids. A seat that is down, or one with nothing waiting, gets none.
    #[test]
    fn an_alert_needs_a_live_seat_and_waiting_rows() {
        let quiet_for = chrono::Duration::seconds(120);
        let last_active = Utc::now() - quiet_for;
        let ids = vec!["qitem-b".to_string(), "qitem-a".to_string()];

        let alert = alert_for(&SeatFacts {
            alive: true,
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
        let quiet = alert.quiet_seconds.expect("a stamp was published, so the quiet time is known");
        assert!((119..=121).contains(&quiet), "quiet_seconds = {quiet}");

        // No stamp published: "how long quiet" is unknown, and the alert says unknown instead of a zero
        // that would read as "just active".
        let unknown = alert_for(&SeatFacts {
            alive: true,
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
            published_state: Some("idle".to_string()),
            activity: ActivityWord::Idle,
            last_active: Some(last_active),
            waiting_qitem_ids: Vec::new(),
            publisher_pid: None,
            publisher_alive: None,
        })
        .is_none());
    }

    /// The rail's rows have been written with more than one spelling of a seat's name, and the join has to
    /// see all of them: a row addressed to the native session name, to the rig orchestrator session name, or to the
    /// bare seat key all belong to the same seat. A spelling the join missed would read as "nothing
    /// waiting" - the lie the alert exists to prevent.
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
            for (to, summary) in [
                (session.as_str(), "native"),
                (orchestrator.as_str(), "orchestrator"),
                (seat.as_str(), "bare key"),
            ] {
                let row = store
                    .create(CreateInput {
                        source_session: "lead".into(),
                        destination_session: to.into(),
                        summary: summary.into(),
                        ..Default::default()
                    })
                    .expect("create");
                expected_ids.push(row.qitem_id);
            }
            // A row for somebody else, and a row for this seat that is closed: neither is waiting work.
            store
                .create(CreateInput {
                    source_session: "lead".into(),
                    destination_session: "someone-else".into(),
                    summary: "not ours".into(),
                    ..Default::default()
                })
                .expect("create");
            let closed = store
                .create(CreateInput {
                    source_session: "lead".into(),
                    destination_session: seat.clone(),
                    summary: "already done".into(),
                    ..Default::default()
                })
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
        assert_eq!(waiting.len(), 3, "waiting = {waiting:?}");
        for id in expected_ids {
            assert!(waiting.contains(&id), "{id} missing from {waiting:?}");
        }
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
            .create(CreateInput {
                source_session: "lead".into(),
                destination_session: seat.clone(),
                summary: "before the room was rebuilt".into(),
                ..Default::default()
            })
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
            .create(CreateInput {
                source_session: "lead".into(),
                destination_session: seat.clone(),
                summary: "after the room was rebuilt".into(),
                ..Default::default()
            })
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

    /// Only the runner's own `idle` earns a nudge: `retrying`/`needs_input`/`blocked` are occupied or
    /// stuck, a death is gone, and no state file has not said the seat is ready.
    #[tokio::test]
    async fn only_the_runners_own_idle_word_earns_a_nudge() {
        let dir = scratch("idle-gate");
        let seat = seat_key("idle-gate");
        write_spec(&dir, "demo", &seat);
        let supervisor = supervisor_at(&dir);

        assert!(
            !supervisor.seat_is_idle("demo", &seat),
            "a seat that published nothing has not said it is ready"
        );
        write_state_file(&dir, "demo", &seat, "idle", "2026-10-13T20:15:30.250Z");
        assert!(supervisor.seat_is_idle("demo", &seat));
        for state in ["retrying", "needs_input", "blocked", "agent_exited"] {
            write_state_file(&dir, "demo", &seat, state, "2026-10-13T20:15:30.250Z");
            assert!(!supervisor.seat_is_idle("demo", &seat), "{state} is not idle");
        }
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
        );

        assert_eq!(
            env.get("AGENTHUB_RUNTIME_SOCKET").map(String::as_str),
            Some("/tmp/isolation/rt/agenthub.sock"),
            "the seat has to be told the socket of the daemon that started it"
        );
        assert_eq!(env.get("AGENTHUB_ROOM").map(String::as_str), Some("isolated"));
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
        let env = seat_env(&seat, "demo", "agenthub--demo--dev", Path::new("/s"), Path::new("/r.sock"), &dir);
        assert!(
            !env.contains_key("AGENTHUB_CONNECTOR_TOKEN"),
            "the credential must not reach a seat, from the file ({env:?})"
        );
        assert_eq!(env.get("AGENTHUB_MCP_URL").map(String::as_str), Some("http://127.0.0.1:8000/mcp"));
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
        let env = seat_env(&seat, "demo", "agenthub--demo--dev", Path::new("/s"), Path::new("/r.sock"), &dir);
        assert_eq!(env.get("AGENTHUB_MCP_URL").map(String::as_str), Some("http://127.0.0.1:8000/mcp"));
        assert_eq!(env.get("QUOTED").map(String::as_str), Some("a b"));
        assert_eq!(env.get("EMPTY").map(String::as_str), Some(""));
        assert_eq!(env.get("SPEC_WINS").map(String::as_str), Some("spec"), "the spec wins over the file");
        assert_eq!(env.get("AGENTHUB_ROOM").map(String::as_str), Some("demo"), "the runtime wins over both");
        assert!(!env.contains_key("bad key"));
        let none = seat_env(&seat, "demo", "s", Path::new("/s"), Path::new("/r.sock"), Path::new("/nonexistent-state-root"));
        assert!(!none.contains_key("AGENTHUB_MCP_URL"), "no file, no variables");
    }

    /// The MCP config a seat is started with carries variable references only, so it is safe to leave in the
    /// seat dir and holds no credential.
    #[test]
    fn the_seat_mcp_config_names_variables_and_no_secret() {
        let parsed: serde_json::Value = serde_json::from_str(SEAT_MCP_CONFIG).expect("valid json");
        let server = &parsed["mcpServers"]["agenthub_http"];
        assert_eq!(server["url"], "${AGENTHUB_URL}/mcp");
        assert_eq!(server["headers"]["Authorization"], "Bearer ${AGENTHUB_TOKEN}");
    }

    /// The hook the runtime writes is the one that blocks and logs: if it lost its guard or its log the seat would
    /// run unguarded and unrecorded with nothing failing.
    #[test]
    fn the_seat_tool_hook_guards_and_logs() {
        for needle in ["\"tool_call\"", "\"tool_result\"", "block: true", "tool-calls.jsonl", "env-secret", "docker", "tmux-mutate", "push"] {
            assert!(SEAT_TOOL_HOOK.contains(needle), "the hook lost {needle}");
        }
    }

    /// The agy guard is added beside another tool's hook without touching it, is idempotent, and a file that is not a
    /// JSON object is refused rather than overwritten.
    #[test]
    fn an_agy_seat_is_launched_with_its_model() {
        assert_eq!(agy_command(Some("gemini-3.8-flash-low")), "agy --model 'gemini-3.8-flash-low'");
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
        assert_eq!(first, fs::read_to_string(&path).unwrap(), "a second install changed the file");
        let hooks: serde_json::Value = serde_json::from_str(&first).unwrap();
        assert_eq!(hooks["herdr"]["PreInvocation"][0]["command"], "x");
        let pre = hooks[AGY_GUARD_HOOK]["PreToolUse"][0]["hooks"][0]["command"].as_str().unwrap();
        assert!(pre.contains("--agy-hook pre") && pre.contains(r#"{"decision":"ask"}"#), "{pre}");
        fs::write(&path, "[1]").unwrap();
        assert!(install_agy_guard_hook(&home).is_err());
        assert_eq!(fs::read_to_string(&path).unwrap(), "[1]");
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
            fs::write(dir.join("SKILL.md"), "---\nname: x\ndescription: d\n---\nbody\n").expect("skill");
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
        fs::write(state.join(".env"), format!("{SKILLS_ROOT_ENV}={}\n", tree.display())).expect("env");
        let seat_dir = state.join("rooms/r/seats/dev");
        fs::create_dir_all(seat_dir.join("skills/stale")).expect("stale");
        install_seat_skills(&state, "r", "dev", &seat_dir).expect("install");
        let mut names: Vec<String> = fs::read_dir(seat_dir.join("skills"))
            .expect("skills dir")
            .map(|e| e.expect("entry").file_name().to_string_lossy().to_string())
            .collect();
        names.sort();
        assert_eq!(names, ["both", "common", "own-skill", "room-skill"], "no stale, no other seat's skill");
        assert!(seat_dir.join("skills/own-skill/baseline.json").is_file(), "files beside SKILL.md go too");
    }

    /// A skill file's mode comes from the COPY, not from this process's umask: the source's bits are decided by
    /// whoever wrote it in the rig, and a rig that ships `0644` shipped it into every seat's own tree -
    /// measured on a scratch room 2026-10-16, `rooms/<room>/seats/<seat>/skills/**` was the one part of the
    /// state root the umask did not reach. The source's bits are set explicitly here so the case means the same
    /// thing whatever this test process's umask happens to be.
    #[cfg(unix)]
    #[test]
    fn an_installed_skill_keeps_the_owners_bits_and_drops_the_rest() {
        use std::os::unix::fs::PermissionsExt;
        let mode = |path: &Path, bits: u32| {
            fs::set_permissions(path, fs::Permissions::from_mode(bits)).expect("set the source mode");
        };
        let perm = |path: PathBuf| fs::metadata(path).expect("mode").permissions().mode() & 0o777;

        let state = scratch("skills-modes");
        let tree = scratch("skills-modes-tree");
        let source = tree.join("share/common");
        fs::create_dir_all(&source).expect("dir");
        fs::write(source.join("SKILL.md"), "---\nname: x\ndescription: d\n---\nbody\n").expect("skill");
        fs::write(source.join("polluter.sh"), "#!/bin/sh\n").expect("script");
        mode(&source.join("SKILL.md"), 0o644);
        mode(&source.join("polluter.sh"), 0o755);
        fs::write(state.join(".env"), format!("{SKILLS_ROOT_ENV}={}\n", tree.display())).expect("env");

        let seat_dir = state.join("rooms/r/seats/dev");
        install_seat_skills(&state, "r", "dev", &seat_dir).expect("install");

        let installed = seat_dir.join("skills/common");
        assert_eq!(
            perm(installed.join("SKILL.md")),
            0o600,
            "a copied skill's file must not keep the rig's 0644"
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
                .create(CreateInput {
                    source_session: "lead".into(),
                    destination_session: seat.clone(),
                    summary: "waiting work".into(),
                    ..Default::default()
                })
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

        supervisor.set_seat_model("lab", "dev", "  deepseek/deepseek-flash ").unwrap();

        let saved: RoomSpec =
            serde_json::from_str(&fs::read_to_string(dir.join("rooms/lab/spec.json")).unwrap()).unwrap();
        assert_eq!(saved.seats[0].model.as_deref(), Some("deepseek/deepseek-flash"));
        assert_eq!(saved.seats[0].runtime, "omp", "the rest of the seat record is untouched");

        assert!(supervisor.set_seat_model("lab", "nobody", "m").unwrap_err().contains("not found"));
        assert!(supervisor.set_seat_model("lab", "dev", " ").unwrap_err().contains("empty"));
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
        let mut spec: RoomSpec = serde_json::from_str(&fs::read_to_string(&spec_file).unwrap()).unwrap();
        spec.links.push(SeatLink { from: "dev".into(), kind: "delegates_to".into(), to: "qa".into() });
        fs::write(&spec_file, serde_json::to_string(&spec).unwrap()).unwrap();

        let links = supervisor.get_room_links("lab").unwrap();
        assert_eq!((links[0].from.as_str(), links[0].kind.as_str(), links[0].to.as_str()), ("dev", "delegates_to", "qa"));
        assert!(supervisor.get_room_links("ghost").unwrap_err().contains("cannot read room spec"));
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
        assert!(other.join("a.jsonl").exists(), "another seat's sessions are untouched");
        supervisor.clear_saved_sessions("lab", "dev").unwrap();
        let _ = fs::remove_dir_all(&dir);
    }

    /// The facts a claim makes, with the three fields this defect turns on: what the seat's document says, who
    /// published it, and whether that publisher is still there.
    fn claim_facts(word: Option<&str>, publisher_pid: Option<u32>, publisher_alive: Option<bool>) -> SeatFacts {
        SeatFacts {
            alive: true,
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

        let doc: serde_json::Value = serde_json::from_str(&fs::read_to_string(&path).unwrap()).unwrap();
        let (word, pid) = published_claim(Some(&doc));
        assert_eq!(word.as_deref(), Some("working"));
        assert_eq!(pid, Some(std::process::id()), "the document must name the process that wrote it");
        assert_eq!(pid.and_then(process_is_alive), Some(true), "and that process is this test");
        let _ = fs::remove_dir_all(&dir);
    }

    /// DEFECT 2, and the half of the daemon's own promise that the code did not keep: a claim whose publisher is
    /// gone. The measured case is a pane that outlived its agent - the file keeps `working`, and every reader of
    /// that document, this status included, is told a seat is working whose agent was killed.
    #[test]
    fn an_observed_death_names_the_claim_whose_publisher_is_gone() {
        let why = observed_death(&claim_facts(Some("working"), Some(4242), Some(false)), true)
            .expect("a claim whose publisher is gone is the death this side can observe");
        assert!(why.contains("pid 4242"), "the sentence must name the publisher: {why}");
        assert!(why.contains("working"), "the sentence must name the stale word: {why}");
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
            ("a runner that is alive", claim_facts(Some("working"), Some(4242), Some(true)), true),
            ("a host that cannot answer", claim_facts(Some("working"), Some(4242), None), true),
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
            ("a seat that published nothing", claim_facts(None, None, None), true),
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
        crate::runner::write_state(&path, "working", "a turn is running", 0).expect("seed the stale claim");

        let why = observed_death(&claim_facts(Some("working"), Some(4242), Some(false)), true).expect("observed");
        crate::runner::write_state(&path, "agent_exited", &why, 0).expect("correct the claim");

        let doc: serde_json::Value = serde_json::from_str(&fs::read_to_string(&path).unwrap()).unwrap();
        assert_eq!(doc["state"], "agent_exited", "the file must carry the death, not the stale word");
        assert!(
            doc["classification"].as_str().is_some_and(|c| c.contains("pid 4242")),
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
        assert!(observed_death(&reread, true).is_none(), "a corrected file must not be corrected again");
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
        assert_eq!(process_is_alive(std::process::id()), Some(true), "this test process is alive");
        assert_eq!(process_is_alive(u32::MAX), Some(false), "a pid that cannot exist is not alive");
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
        fs::write(room_dir.join("spec.json"), serde_json::to_string(&spec).unwrap()).unwrap();

        // A REAL pane under a private tmux server: the seat is up, and its agent is not.
        let session = RoomSupervisor::session_name(&room, &key);
        TerminalManager::launch_session(&session, &room_dir, "sleep 30", &HashMap::new()).expect("pane");
        let state_path = room_dir.join("run").join(format!("{key}.state.json"));
        crate::runner::write_state(&state_path, "working", "a turn is running", 0).expect("the stale claim");
        // The publisher is then made a pid that cannot exist - the one way to fake a gone runner without killing
        // one - while the pane stays up.
        let mut doc: serde_json::Value = serde_json::from_str(&fs::read_to_string(&state_path).unwrap()).unwrap();
        doc["pid"] = serde_json::json!(u32::MAX);
        fs::write(&state_path, serde_json::to_string(&doc).unwrap()).unwrap();

        let statuses = supervisor.get_room_status(&room).expect("status");
        let status = statuses.iter().find(|s| s.seat == key).expect("the seat's row");
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

        let after: serde_json::Value = serde_json::from_str(&fs::read_to_string(&state_path).unwrap()).unwrap();
        assert_eq!(
            after["state"], "agent_exited",
            "the document itself must stop claiming a live seat: {after}"
        );

        let _ = TerminalManager::stop_session(&session);
        let _ = fs::remove_dir_all(&dir);
    }
}
