use crate::hooks::{HookBus, SeatEvent};
use crate::pty::TerminalManager;
use crate::queue::{ListFilter, QueueStore};
use crate::types::*;
use chrono::{DateTime, Utc};
use parking_lot::{Mutex, MutexGuard};
use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;
use tracing::{error, info, warn};

/// How long a seat is given to prove it is actually up before `start_seat` refuses to call it running. The
/// signal is the seat's own state file (`runner::state_file_path`), which the runner publishes only after the
/// agent answered the transport handshake; a seat that dies takes its tmux session with it, so the loop
/// normally exits in well under a second either way. Named, because it is the window U1's exit-code contract
/// has to be built on: 0 only when every seat is running, and a seat that never proved itself is not.
const SEAT_READY_WINDOW_SECS: u64 = 20;

/// What the runtimes without a runner get: their signal is only that the session survived, so the window is
/// a grace period, not a handshake. Short, because it proves nothing about the agent inside the pane.
const NON_OMP_READY_GRACE_MS: u64 = 1500;

pub struct RoomSupervisor {
    base_state_dir: PathBuf,
    active_rooms: Arc<Mutex<HashMap<String, RoomSpec>>>,
    /// The room's queue store, opened on first use and held for as long as the daemon serves the room.
    /// SQLite is the rail: one connection per room, the same object every verb reads, so a write is
    /// visible to the next read without a re-open in between.
    queue_stores: Arc<Mutex<HashMap<String, QueueStore>>>,
    hook_bus: HookBus,
}

/// A room's open queue store, borrowed from the supervisor's cache for the length of one verb. Derefs to
/// the store, so a caller writes `store.create(input)` exactly as it would on an owned store.
pub struct RoomQueueStore<'a> {
    stores: MutexGuard<'a, HashMap<String, QueueStore>>,
    room: String,
}

impl std::ops::Deref for RoomQueueStore<'_> {
    type Target = QueueStore;

    fn deref(&self) -> &QueueStore {
        self.stores
            .get(&self.room)
            .expect("the store is inserted for this room before the borrow is handed out")
    }
}

/// The one read of a seat's own state file and the room's rail that both the status row and the nudge
/// pass are built from (see `RoomSupervisor::seat_facts`).
struct SeatFacts {
    /// The seat's tmux session is up.
    alive: bool,
    /// The runner's own word for what it is doing: `idle`, `retrying`, `needs_input`, `blocked`, or a
    /// death (`agent_exited`, …). `None` when the seat published nothing.
    published_state: Option<String>,
    /// The seat's freshness stamp: the state file's `updated_at`.
    last_active: Option<DateTime<Utc>>,
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
/// says), and the OpenRig session name the live rig's rows carry (`<rig>-<seat>@<rig>`, where this
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

impl RoomSupervisor {
    pub fn new(base_state_dir: impl Into<PathBuf>, hook_bus: HookBus) -> Self {
        let base_state_dir = base_state_dir.into();
        let _ = fs::create_dir_all(&base_state_dir);
        Self {
            base_state_dir,
            active_rooms: Arc::new(Mutex::new(HashMap::new())),
            queue_stores: Arc::new(Mutex::new(HashMap::new())),
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

    /// The room's durable queue store.
    ///
    /// A room that was never started has no directory and no rail, so a queue verb against it is a refusal
    /// naming the room — not an empty queue, which is the same untrue answer as a bare `[]` (Rule 2: no
    /// answer where there is no record). A *stopped* room keeps its directory and its rows, and stays
    /// readable.
    pub fn queue_store(&self, room: &str) -> Result<RoomQueueStore<'_>, String> {
        let room_dir = self.room_dir(room);
        if !room_dir.is_dir() {
            return Err(format!(
                "no such room '{room}': {} is not a room directory",
                room_dir.display()
            ));
        }
        let mut stores = self.queue_stores.lock();
        if !stores.contains_key(room) {
            stores.insert(room.to_string(), QueueStore::open(&room_dir)?);
        }
        Ok(RoomQueueStore {
            stores,
            room: room.to_string(),
        })
    }

    /// Derive standard session name: agenthub--<room>--<seat>
    pub fn session_name(room: &str, seat: &str) -> String {
        format!("agenthub--{room}--{seat}")
    }

    /// Start a room with its seats.
    pub fn start_room(&self, spec: RoomSpec) -> Result<Vec<SeatStatus>, String> {
        let room_name = spec.room.clone();
        info!("starting room {} with {} seats", room_name, spec.seats.len());

        let room_dir = self.room_dir(&room_name);
        fs::create_dir_all(&room_dir).map_err(|e| format!("cannot create room dir: {e}"))?;

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
            detail: Some(reason),
            started_at: None,
            token_estimate: 0,
            last_active: None,
            transcript_path: Some(
                base_state_dir
                    .join("rooms")
                    .join(room)
                    .join("logs")
                    .join(format!("{}.jsonl", seat.seat_key))
                    .to_string_lossy()
                    .to_string(),
            ),
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

        // Determine working directory
        let cwd_str = seat.cwd.as_deref().or(spec.cwd.as_deref()).unwrap_or(".");
        let cwd = PathBuf::from(cwd_str);

        // Build command line according to runtime. An omp seat's pane runs THIS binary as a runner, which
        // spawns the agent and owns its channel (design §1: the runner replaces `pi-runner.js`); the other
        // runtimes keep their own command until their native rpc paths exist.
        let runtime = AgentRuntime::from_str_loose(&seat.runtime);
        let cmd = match &runtime {
            AgentRuntime::Omp => {
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
            AgentRuntime::ClaudeCode => "claude --dangerously-skip-permissions".to_string(),
            AgentRuntime::Agy => "agy".to_string(),
            AgentRuntime::Codex => "codex".to_string(),
            AgentRuntime::Custom(c) => c.clone(),
        };

        // Build environment variables
        let mut env = seat.env.clone();
        env.insert("AGENTHUB_ROOM".into(), room.into());
        env.insert("AGENTHUB_SEAT".into(), seat.seat_key.clone());
        env.insert("AGENTHUB_ROLE".into(), seat.seat_type.clone());
        env.insert("AGENTHUB_RUNTIME".into(), seat.runtime.clone());
        env.insert("AGENTHUB_SESSION_NAME".into(), session_name.clone());
        env.insert("PI_CODING_AGENT_DIR".into(), seat_dir.to_string_lossy().to_string());
        if let Some(m) = &seat.model {
            env.insert("AGENTHUB_MODEL".into(), m.clone());
        }

        // Launch tmux session
        let pid = TerminalManager::launch_session(&session_name, &cwd, &cmd, &env)?;

        // A launch is not liveness. The pid tmux just handed back is a **record** of the session it created,
        // and the pane's process may already be gone - a seat whose runtime command does not exist came back
        // `running` with a pid and no session seconds later. So wait for proof that the seat actually got up
        // before saying so. An omp seat proves it with its state file
        // (`runner::state_file_path`), which its runner publishes only after the agent answered the transport
        // handshake; the other runtimes have no runner and prove less - their signal is the session still
        // existing after a grace period, which is stated here rather than passed off as handshake proof.
        let (signal, window) = match &runtime {
            AgentRuntime::Omp => (
                Some(crate::runner::state_file_path(
                    &self.base_state_dir,
                    room,
                    &seat.seat_key,
                )),
                Duration::from_secs(SEAT_READY_WINDOW_SECS),
            ),
            _ => (None, Duration::from_millis(NON_OMP_READY_GRACE_MS)),
        };
        let deadline = std::time::Instant::now() + window;
        loop {
            if signal.as_ref().is_some_and(|path| path.exists()) {
                break;
            }
            if !TerminalManager::is_session_alive(&session_name) {
                return Err(format!(
                    "seat {room}/{} died before it reported readiness (its pane is gone within {}; pid {pid} \
                     was the session record tmux returned)",
                    seat.seat_key,
                    if signal.is_some() {
                        format!("{SEAT_READY_WINDOW_SECS}s")
                    } else {
                        format!("{NON_OMP_READY_GRACE_MS}ms")
                    }
                ));
            }
            if std::time::Instant::now() >= deadline {
                return Err(match signal {
                    Some(path) => format!(
                        "seat {room}/{} did not report readiness within {SEAT_READY_WINDOW_SECS}s (no {}); \
                         not reporting it as running",
                        seat.seat_key,
                        path.display()
                    ),
                    None => format!(
                        "seat {room}/{} has no runtime handshake to wait for, and its session did not \
                         survive {NON_OMP_READY_GRACE_MS}ms; not reporting it as running",
                        seat.seat_key
                    ),
                });
            }
            std::thread::sleep(Duration::from_millis(100));
        }

        // Transcript log path
        let logs_dir = room_dir.join("logs");
        let _ = fs::create_dir_all(&logs_dir);
        let transcript_path = logs_dir.join(format!("{}.jsonl", seat.seat_key));

        let status = SeatStatus {
            room: room.to_string(),
            seat: seat.seat_key.clone(),
            session_name: session_name.clone(),
            runtime: seat.runtime.clone(),
            model: seat.model.clone(),
            pid: Some(pid),
            state: SeatLifecycleState::Running,
            detail: None,
            started_at: Some(Utc::now()),
            token_estimate: 0,
            last_active: Some(Utc::now()),
            transcript_path: Some(transcript_path.to_string_lossy().to_string()),
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
                }
            }
        }

        self.active_rooms.lock().remove(room);
        // The room's store goes with its seats; the next verb for this room opens the file fresh, which is
        // what makes a stopped room's rows durable-but-not-cached rather than lost.
        self.queue_stores.lock().remove(room);
        // A spill outlives no room (`SPEC-runner-relay-protocol-2026-10-13.md` §2): its payloads are the
        // room's bytes, and a tree left behind would be a payload no handle can name again.
        let _ = crate::frame::remove_spill_trees(&room_dir);
        Ok(())
    }

    /// Stop one seat.
    pub fn stop_seat(&self, room: &str, seat: &str) -> Result<(), String> {
        let session = Self::session_name(room, seat);
        TerminalManager::stop_session(&session)
    }

    /// Restart one seat.
    pub fn restart_seat(&self, room: &str, seat: &str) -> Result<SeatStatus, String> {
        self.stop_seat(room, seat)?;
        let room_dir = self.room_dir(room);
        let spec_file = room_dir.join("spec.json");
        let content = fs::read_to_string(&spec_file).map_err(|e| format!("cannot read room spec: {e}"))?;
        let spec: RoomSpec = serde_json::from_str(&content).map_err(|e| format!("parse spec: {e}"))?;
        let seat_spec = spec.seats.iter().find(|s| s.seat_key == seat)
            .ok_or_else(|| format!("seat {seat} not found in room {room}"))?;
        self.start_seat(room, seat_spec, &spec)
    }

    /// Get all seat statuses for a room.
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
            let state = if facts.alive {
                SeatLifecycleState::Running
            } else {
                SeatLifecycleState::Stopped
            };

            // The reason, because "not running" alone is exactly the shape that let a dead turn look healthy
            // (`SPEC` §4). The runner publishes the three death facts when it survives to see them; when the
            // pane's teardown kills it first, the state file still claims `idle`/`running` — so the daemon,
            // which owns this file's path too, replaces that claim with the death it can actually observe.
            let state_path = room_dir.join("run").join(format!("{}.state.json", seat.seat_key));
            let detail = if facts.alive {
                None
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

            let logs_dir = room_dir.join("logs");
            let transcript_path = logs_dir.join(format!("{}.jsonl", seat.seat_key));

            statuses.push(SeatStatus {
                room: room.to_string(),
                seat: seat.seat_key,
                session_name: session,
                runtime: seat.runtime,
                model: seat.model,
                pid: None,
                state,
                detail,
                started_at: None,
                token_estimate: 0,
                last_active: facts.last_active,
                transcript_path: Some(transcript_path.to_string_lossy().to_string()),
                alert: alert_for(&facts),
            });
        }
        Ok(statuses)
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
        let published_state = published
            .as_ref()
            .and_then(|v| v.get("state").and_then(|s| s.as_str()).map(str::to_string));
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
            last_active,
            waiting_qitem_ids,
        }
    }

    /// The active rows the room's rail is holding for one seat, by qitem id.
    ///
    /// The rail's rows are written by the client's `room queue` verbs, whose `--to` is a free session
    /// string, and the two spellings in use disagree: the live rig's `OPENRIG_SESSION_NAME` is
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

                                let transcript_path = entry.path().join("logs").join(format!("{}.jsonl", seat.seat_key));
                                let transcript_str = transcript_path.to_string_lossy().to_string();

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
                                        path: Some(transcript_str.clone()),
                                        tail_command: Some(format!("tail -f {transcript_str}")),
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::queue::{CloseInput, CreateInput};

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
        RoomSupervisor::new(state_dir, HookBus::new(state_dir.join("audit"), 8))
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
                "classification": "",
                "resume_attempts": 0,
                "updated_at": updated_at,
            })
            .to_string(),
        )
        .expect("state file");
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
            last_active: Some(last_active),
            waiting_qitem_ids: ids.clone(),
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
            last_active: None,
            waiting_qitem_ids: vec!["qitem-c".to_string()],
        })
        .expect("a live seat with work waiting still gets an alert with no stamp");
        assert_eq!(unknown.quiet_seconds, None);

        // Down with work waiting: not the seat the rail can reach, so nothing to report.
        assert!(alert_for(&SeatFacts {
            alive: false,
            published_state: Some("agent_exited".to_string()),
            last_active: Some(last_active),
            waiting_qitem_ids: ids.clone(),
        })
        .is_none());
        // Up with nothing waiting: nothing to be woken for.
        assert!(alert_for(&SeatFacts {
            alive: true,
            published_state: Some("idle".to_string()),
            last_active: Some(last_active),
            waiting_qitem_ids: Vec::new(),
        })
        .is_none());
    }

    /// The rail's rows have been written with more than one spelling of a seat's name, and the join has to
    /// see all of them: a row addressed to the native session name, to the OpenRig session name, or to the
    /// bare seat key all belong to the same seat. A spelling the join missed would read as "nothing
    /// waiting" - the lie the alert exists to prevent.
    #[tokio::test]
    async fn waiting_rows_match_every_spelling_the_rail_has_used() {
        let dir = scratch("waiting-rows");
        let seat = seat_key("spellings");
        write_spec(&dir, "demo", &seat);
        let session = RoomSupervisor::session_name("demo", &seat);
        let openrig = format!("demo-{seat}@demo");
        let supervisor = supervisor_at(&dir);
        let mut expected_ids = Vec::new();
        {
            // Scoped: `queue_store` holds the store mutex for the borrow, and `waiting_rows` takes it again.
            let store = supervisor.queue_store("demo").expect("open the rail");
            for (to, summary) in [
                (session.as_str(), "native"),
                (openrig.as_str(), "openrig"),
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
}
