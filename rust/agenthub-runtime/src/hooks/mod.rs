use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::fs::OpenOptions;
use std::io::Write;
use std::path::PathBuf;
use std::sync::Arc;
use tokio::sync::broadcast;
use tracing::{error, info};

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", content = "payload")]
pub enum SeatEvent {
    SessionStarted {
        room: String,
        seat: String,
        session_name: String,
        pid: u32,
        timestamp: DateTime<Utc>,
    },
    PreTurn {
        room: String,
        seat: String,
        turn_id: String,
        tokens: usize,
        timestamp: DateTime<Utc>,
    },
    PostTurn {
        room: String,
        seat: String,
        turn_id: String,
        tokens: usize,
        duration_ms: u64,
        timestamp: DateTime<Utc>,
    },
    ToolRequested {
        room: String,
        seat: String,
        tool: String,
        args_summary: String,
        timestamp: DateTime<Utc>,
    },
    ToolCompleted {
        room: String,
        seat: String,
        tool: String,
        is_error: bool,
        duration_ms: u64,
        timestamp: DateTime<Utc>,
    },
    ContextLimitReached {
        room: String,
        seat: String,
        tokens: usize,
        threshold: usize,
        timestamp: DateTime<Utc>,
    },
    SessionTerminated {
        room: String,
        seat: String,
        exit_code: Option<i32>,
        timestamp: DateTime<Utc>,
    },
}

#[derive(Clone)]
pub struct HookBus {
    sender: broadcast::Sender<SeatEvent>,
    audit_root: Arc<PathBuf>,
}

impl HookBus {
    pub fn new(audit_root: impl Into<PathBuf>, capacity: usize) -> Self {
        let (sender, _) = broadcast::channel(capacity);
        let bus = Self {
            sender,
            audit_root: Arc::new(audit_root.into()),
        };
        bus.start_audit_listener();
        bus
    }

    pub fn subscribe(&self) -> broadcast::Receiver<SeatEvent> {
        self.sender.subscribe()
    }

    pub fn emit(&self, event: SeatEvent) {
        if let Err(e) = self.sender.send(event) {
            error!("failed to broadcast seat event: {e}");
        }
    }

    /// Spawns background worker to append events to audit.jsonl per seat.
    fn start_audit_listener(&self) {
        let mut rx = self.sender.subscribe();
        let audit_root = Arc::clone(&self.audit_root);

        tokio::spawn(async move {
            while let Ok(event) = rx.recv().await {
                let (room, seat) = match &event {
                    SeatEvent::SessionStarted { room, seat, .. } => (room, seat),
                    SeatEvent::PreTurn { room, seat, .. } => (room, seat),
                    SeatEvent::PostTurn { room, seat, .. } => (room, seat),
                    SeatEvent::ToolRequested { room, seat, .. } => (room, seat),
                    SeatEvent::ToolCompleted { room, seat, .. } => (room, seat),
                    SeatEvent::ContextLimitReached { room, seat, .. } => (room, seat),
                    SeatEvent::SessionTerminated { room, seat, .. } => (room, seat),
                };

                let room_dir = audit_root.join(room);
                if let Err(e) = std::fs::create_dir_all(&room_dir) {
                    error!("cannot create room audit dir {}: {e}", room_dir.display());
                    continue;
                }

                let audit_path = room_dir.join(format!("{seat}-audit.jsonl"));
                if let Ok(json_line) = serde_json::to_string(&event) {
                    if let Ok(mut f) = OpenOptions::new().create(true).append(true).open(&audit_path) {
                        let _ = writeln!(f, "{json_line}");
                    }
                }
            }
        });
    }
}
