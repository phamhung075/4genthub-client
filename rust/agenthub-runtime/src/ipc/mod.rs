use crate::pty::TerminalManager;
use crate::supervisor::RoomSupervisor;
use crate::types::*;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tracing::{error, info};

pub struct IpcServer {
    socket_path: PathBuf,
    supervisor: Arc<RoomSupervisor>,
}

impl IpcServer {
    pub fn default_socket_path() -> PathBuf {
        if let Ok(runtime_dir) = std::env::var("XDG_RUNTIME_DIR") {
            let path = PathBuf::from(runtime_dir).join("agenthub.sock");
            return path;
        }
        let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
        PathBuf::from(home).join(".config").join("4genthub").join("run").join("runtime.sock")
    }

    pub fn new(socket_path: impl Into<PathBuf>, supervisor: Arc<RoomSupervisor>) -> Self {
        Self {
            socket_path: socket_path.into(),
            supervisor,
        }
    }

    pub async fn run(&self) -> Result<(), String> {
        if let Some(parent) = self.socket_path.parent() {
            let _ = fs::create_dir_all(parent);
        }
        let _ = fs::remove_file(&self.socket_path);

        let listener = UnixListener::bind(&self.socket_path)
            .map_err(|e| format!("cannot bind unix socket {}: {e}", self.socket_path.display()))?;

        info!("agenthub-runtime IPC server listening at {}", self.socket_path.display());

        loop {
            match listener.accept().await {
                Ok((stream, _)) => {
                    let supervisor = Arc::clone(&self.supervisor);
                    tokio::spawn(async move {
                        if let Err(e) = Self::handle_client(stream, supervisor).await {
                            error!("ipc client error: {e}");
                        }
                    });
                }
                Err(e) => {
                    error!("listener accept error: {e}");
                }
            }
        }
    }

    async fn handle_client(stream: UnixStream, supervisor: Arc<RoomSupervisor>) -> Result<(), String> {
        let (reader, mut writer) = stream.into_split();
        let mut reader = BufReader::new(reader);
        let mut line = String::new();

        while reader.read_line(&mut line).await.map_err(|e| e.to_string())? > 0 {
            let trimmed = line.trim();
            if trimmed.is_empty() {
                line.clear();
                continue;
            }

            let response = match serde_json::from_str::<IpcRequest>(trimmed) {
                Ok(req) => Self::dispatch_request(req, &supervisor).await,
                Err(e) => IpcResponse::err(format!("invalid request JSON: {e}")),
            };

            let mut out = serde_json::to_string(&response).unwrap_or_else(|_| "{\"ok\":false}".into());
            out.push('\n');
            writer.write_all(out.as_bytes()).await.map_err(|e| e.to_string())?;
            writer.flush().await.map_err(|e| e.to_string())?;

            line.clear();
        }

        Ok(())
    }

    async fn dispatch_request(req: IpcRequest, supervisor: &RoomSupervisor) -> IpcResponse {
        match req {
            IpcRequest::Ping => IpcResponse::success(serde_json::json!({"status": "pong"})),

            IpcRequest::StartRoom(spec) => match supervisor.start_room(spec) {
                Ok(statuses) => IpcResponse::success(serde_json::to_value(statuses).unwrap()),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::StopRoom { room } => match supervisor.stop_room(&room) {
                Ok(_) => IpcResponse::success(serde_json::json!({"stopped": room})),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::StopSeat { room, seat } => match supervisor.stop_seat(&room, &seat) {
                Ok(_) => IpcResponse::success(serde_json::json!({"stopped_seat": seat})),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::RestartSeat { room, seat } => match supervisor.restart_seat(&room, &seat) {
                Ok(status) => IpcResponse::success(serde_json::to_value(status).unwrap()),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::SendText { session, text } => match TerminalManager::send_text(&session, &text) {
                Ok(_) => IpcResponse::success(serde_json::json!({"delivered": session})),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::GetRoomStatus { room } => match supervisor.get_room_status(&room) {
                Ok(statuses) => IpcResponse::success(serde_json::to_value(statuses).unwrap()),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::Whoami { session, pid } => {
                match supervisor.resolve_whoami(session.as_deref(), pid) {
                    Ok(resp) => IpcResponse::success(serde_json::to_value(resp).unwrap()),
                    Err(e) => IpcResponse::err(e),
                }
            }

            IpcRequest::ForceCompact { session } => match crate::runner::request_compact(supervisor.state_dir(), &session) {
                Ok(msg) => IpcResponse::success(serde_json::json!({"result": msg})),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::ListAllRooms => {
                // List subdirectories under rooms/
                let base = supervisor.room_dir("..").join("rooms");
                let mut rooms = Vec::new();
                if let Ok(entries) = fs::read_dir(base) {
                    for entry in entries.flatten() {
                        if entry.path().is_dir() {
                            rooms.push(entry.file_name().to_string_lossy().to_string());
                        }
                    }
                }
                IpcResponse::success(serde_json::to_value(rooms).unwrap())
            }

            // The room's durable rail. Every arm opens the room's own store and answers with the row
            // RE-READ from it, so what a caller is told is what the next read shows.
            IpcRequest::QueueCreate { room, input } => match supervisor.queue_store(&room) {
                Ok(store) => match store.create(input) {
                    Ok(row) => serde_json::to_value(row)
                        .map(IpcResponse::success)
                        .unwrap_or_else(|e| IpcResponse::err(format!("serialize row: {e}"))),
                    Err(e) => IpcResponse::err(e),
                },
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::QueueList { room, filter } => match supervisor.queue_store(&room) {
                Ok(store) => match store.list(&filter) {
                    Ok(rows) => serde_json::to_value(rows)
                        .map(IpcResponse::success)
                        .unwrap_or_else(|e| IpcResponse::err(format!("serialize rows: {e}"))),
                    Err(e) => IpcResponse::err(e),
                },
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::QueueShow { room, qitem_id } => match supervisor.queue_store(&room) {
                Ok(store) => match store.show(&qitem_id) {
                    Ok(row) => serde_json::to_value(row)
                        .map(IpcResponse::success)
                        .unwrap_or_else(|e| IpcResponse::err(format!("serialize row: {e}"))),
                    Err(e) => IpcResponse::err(e),
                },
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::QueueUpdate { room, input } => match supervisor.queue_store(&room) {
                Ok(store) => match store.update(input) {
                    Ok(row) => serde_json::to_value(row)
                        .map(IpcResponse::success)
                        .unwrap_or_else(|e| IpcResponse::err(format!("serialize row: {e}"))),
                    Err(e) => IpcResponse::err(e),
                },
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::QueueClaim {
                room,
                qitem_id,
                actor_session,
            } => match supervisor.queue_store(&room) {
                Ok(store) => match store.claim(&qitem_id, &actor_session) {
                    Ok(row) => serde_json::to_value(row)
                        .map(IpcResponse::success)
                        .unwrap_or_else(|e| IpcResponse::err(format!("serialize row: {e}"))),
                    Err(e) => IpcResponse::err(e),
                },
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::QueueClose { room, input } => match supervisor.queue_store(&room) {
                Ok(store) => match store.close(input) {
                    Ok(row) => serde_json::to_value(row)
                        .map(IpcResponse::success)
                        .unwrap_or_else(|e| IpcResponse::err(format!("serialize row: {e}"))),
                    Err(e) => IpcResponse::err(e),
                },
                Err(e) => IpcResponse::err(e),
            },
        }
    }
}
