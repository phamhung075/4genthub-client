use crate::hooks::{HookBus, SeatEvent};
use crate::pty::TerminalManager;
use crate::queue::QueueStore;
use crate::types::*;
use chrono::Utc;
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
            let alive = TerminalManager::is_session_alive(&session);
            let state = if alive {
                SeatLifecycleState::Running
            } else {
                SeatLifecycleState::Stopped
            };
            // The reason, because "not running" alone is exactly the shape that let a dead turn look healthy
            // (`SPEC` §4). The runner publishes the three death facts when it survives to see them; when the
            // pane's teardown kills it first, the state file still claims `idle`/`running` — so the daemon,
            // which owns this file's path too, replaces that claim with the death it can actually observe.
            let state_path = room_dir.join("run").join(format!("{}.state.json", seat.seat_key));
            let published = fs::read_to_string(&state_path)
                .ok()
                .and_then(|t| serde_json::from_str::<serde_json::Value>(&t).ok())
                .and_then(|v| v.get("state").and_then(|s| s.as_str()).map(str::to_string));
            let detail = if alive {
                None
            } else {
                match published {
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
                last_active: None,
                transcript_path: Some(transcript_path.to_string_lossy().to_string()),
            });
        }
        Ok(statuses)
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
