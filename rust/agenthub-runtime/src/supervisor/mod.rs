use crate::hooks::{HookBus, SeatEvent};
use crate::pty::TerminalManager;
use crate::types::*;
use chrono::Utc;
use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use tracing::{error, info, warn};

pub struct RoomSupervisor {
    base_state_dir: PathBuf,
    active_rooms: Arc<Mutex<HashMap<String, RoomSpec>>>,
    hook_bus: HookBus,
}

impl RoomSupervisor {
    pub fn new(base_state_dir: impl Into<PathBuf>, hook_bus: HookBus) -> Self {
        let base_state_dir = base_state_dir.into();
        let _ = fs::create_dir_all(&base_state_dir);
        Self {
            base_state_dir,
            active_rooms: Arc::new(Mutex::new(HashMap::new())),
            hook_bus,
        }
    }

    /// Room state directory: ~/.config/4genthub/rooms/<room>/
    pub fn room_dir(&self, room: &str) -> PathBuf {
        self.base_state_dir.join("rooms").join(room)
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

        for seat in &final_spec.seats {
            match self.start_seat(&room_name, seat, &final_spec) {
                Ok(status) => statuses.push(status),
                Err(e) => {
                    error!("failed to start seat {}: {e}", seat.seat_key);
                }
            }
        }

        self.active_rooms.lock().unwrap().insert(room_name, final_spec);
        Ok(statuses)
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

        // Build command line according to runtime
        let runtime = AgentRuntime::from_str_loose(&seat.runtime);
        let cmd = match runtime {
            AgentRuntime::Omp => "omp".to_string(),
            AgentRuntime::ClaudeCode => "claude --dangerously-skip-permissions".to_string(),
            AgentRuntime::Agy => "agy".to_string(),
            AgentRuntime::Codex => "codex".to_string(),
            AgentRuntime::Custom(ref c) => c.clone(),
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

        self.active_rooms.lock().unwrap().remove(room);
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
