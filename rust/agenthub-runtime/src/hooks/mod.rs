use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::fs::OpenOptions;
use std::io::Write;
use std::path::PathBuf;
use std::sync::Arc;
use tokio::sync::broadcast::{self, error::RecvError};
use tracing::{error, warn};

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
            loop {
                // A lagged receiver lost events but is still subscribed: say how many and keep writing. Only a
                // closed bus ends the listener, otherwise one burst would silence the audit for good.
                let event = match rx.recv().await {
                    Ok(event) => event,
                    Err(RecvError::Lagged(missed)) => {
                        warn!("the seat audit missed {missed} events");
                        continue;
                    }
                    Err(RecvError::Closed) => break,
                };
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

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("agenthub-hooks-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("scratch dir");
        dir
    }

    fn started(seat: &str) -> SeatEvent {
        SeatEvent::SessionStarted {
            room: "r".into(),
            seat: seat.into(),
            session_name: "s".into(),
            pid: 1,
            timestamp: Utc::now(),
        }
    }

    async fn lines(path: &std::path::Path, want: usize) -> Vec<String> {
        for _ in 0..100 {
            let text = std::fs::read_to_string(path).unwrap_or_default();
            let got: Vec<String> = text.lines().map(String::from).collect();
            if got.len() >= want {
                return got;
            }
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
        Vec::new()
    }

    #[tokio::test]
    async fn an_event_is_appended_to_its_seats_audit_file() {
        let dir = scratch("append");
        let bus = HookBus::new(&dir, 8);
        bus.emit(started("lead"));
        bus.emit(started("go-dev"));
        let lead = lines(&dir.join("r/lead-audit.jsonl"), 1).await;
        assert_eq!(lead.len(), 1);
        assert!(lead[0].contains("\"type\":\"SessionStarted\""));
        assert_eq!(lines(&dir.join("r/go-dev-audit.jsonl"), 1).await.len(), 1);
    }

    #[tokio::test]
    async fn a_burst_that_laps_the_receiver_does_not_end_the_audit() {
        let dir = scratch("burst");
        let bus = HookBus::new(&dir, 2);
        for _ in 0..50 {
            bus.emit(started("lead"));
        }
        bus.emit(started("after-burst"));
        assert_eq!(lines(&dir.join("r/after-burst-audit.jsonl"), 1).await.len(), 1);
    }
}
