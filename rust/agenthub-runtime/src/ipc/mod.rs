use crate::pty::TerminalManager;
use crate::supervisor::RoomSupervisor;
use crate::types::*;
use chrono::Utc;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tracing::{error, info, warn};

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

        // The daemon's one piece of periodic work. It is spawned here because this is where the daemon
        // owns its long-lived tasks (this accept loop and one task per client), and it is stopped through
        // the same shutdown that stops the accept loop - so the daemon never returns while a task still
        // holds the supervisor and can write into its state dir.
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
        let nudge = tokio::spawn(nudge_loop(Arc::clone(&self.supervisor), shutdown_rx));

        let result = self.accept_until_shutdown(&listener).await;

        let _ = shutdown_tx.send(true);
        let _ = nudge.await;
        let _ = fs::remove_file(&self.socket_path);
        result
    }

    /// The accept loop, and the two signals that end it: SIGINT (the operator's Ctrl-C) and SIGTERM (how a
    /// supervisor, or a plain `kill`, stops a daemon). Both mean "the daemon is going", and both have to
    /// stop the nudge loop too - which is why the shutdown lives here rather than in `main`.
    async fn accept_until_shutdown(&self, listener: &UnixListener) -> Result<(), String> {
        let mut terminate = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .map_err(|e| format!("cannot listen for SIGTERM: {e}"))?;
        let ctrl_c = tokio::signal::ctrl_c();
        tokio::pin!(ctrl_c);
        loop {
            tokio::select! {
                accepted = listener.accept() => match accepted {
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
                },
                _ = &mut ctrl_c => {
                    info!("SIGINT: stopping the daemon");
                    return Ok(());
                }
                _ = terminate.recv() => {
                    info!("SIGTERM: stopping the daemon");
                    return Ok(());
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

            IpcRequest::RestartSeat { room, seat, fresh, policy_document } => {
                match supervisor.restart_seat(&room, &seat, fresh, policy_document.as_deref()) {
                    Ok(status) => IpcResponse::success(serde_json::to_value(status).unwrap()),
                    Err(e) => IpcResponse::err(e),
                }
            }

            IpcRequest::SetSeatModel { room, seat, model } => match supervisor.set_seat_model(&room, &seat, &model) {
                Ok(_) => IpcResponse::success(serde_json::json!({"seat": seat, "model": model})),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::SendText {
                session,
                text,
                from,
                verb,
            } => match supervisor.deliver_text(
                &session,
                &text,
                &crate::runner::Origin { from, verb },
            ) {
                Ok(answer) => IpcResponse::success(answer),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::GetRoomStatus { room } => match supervisor.get_room_status(&room) {
                Ok(statuses) => IpcResponse::success(serde_json::to_value(statuses).unwrap()),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::GetRoomLinks { room } => match supervisor.get_room_links(&room) {
                Ok(links) => IpcResponse::success(serde_json::to_value(links).unwrap()),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::ListSessions { room } => match supervisor.list_sessions(room.as_deref()) {
                Ok(entries) => IpcResponse::success(serde_json::to_value(entries).unwrap()),
                Err(e) => IpcResponse::err(e),
            },

            IpcRequest::Transcript { session, lines } => {
                match supervisor.read_transcript(&session, lines) {
                    Ok(answer) => IpcResponse::success(serde_json::to_value(answer).unwrap()),
                    Err(e) => IpcResponse::err(e),
                }
            }

            IpcRequest::Capture { session, lines } => {
                match supervisor.capture_session(&session, lines) {
                    Ok(answer) => IpcResponse::success(serde_json::to_value(answer).unwrap()),
                    Err(e) => IpcResponse::err(e),
                }
            }

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
                Ok(store) => match match filter.requester_session.clone() {
                    Some(session) => store.list_rate_limited(&session, &filter),
                    None => store.list(&filter),
                } {
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
/// How often the daemon looks for a seat that is up with rail work waiting and has not been woken. Short
/// enough that a row handed to an idle seat is answered within seconds; long enough that a pass (a tmux
/// probe and a rail read per seat) is not a spin.
const NUDGE_POLL_INTERVAL_SECS: u64 = 2;

/// The daemon's periodic nudge pass, until the shutdown it was handed says the daemon is going.
///
/// A pass shells out (tmux), opens SQLite and writes files, so it runs on the blocking pool rather than
/// stalling the accept loop's runtime.
async fn nudge_loop(supervisor: Arc<RoomSupervisor>, mut shutdown: tokio::sync::watch::Receiver<bool>) {
    loop {
        tokio::select! {
            // Any change is the shutdown (`true`), and a dropped sender means the daemon is going too:
            // both end the loop, rather than leaving a task that would keep nudging a daemon that no
            // longer exists.
            _ = shutdown.changed() => return,
            _ = tokio::time::sleep(Duration::from_secs(NUDGE_POLL_INTERVAL_SECS)) => {}
        }
        let supervisor = Arc::clone(&supervisor);
        let _ = tokio::task::spawn_blocking(move || nudge_pass(&supervisor)).await;
    }
}

/// One pass: for every seat the rail is holding work for whose runner says it is idle, deliver the nudge
/// once - through the seat's runner mailbox, the channel the agent actually reads - and record the
/// attempt. The ladder is bounded and its state is the durable attempt log, so a restart resumes the same
/// ladder instead of starting a fresh one against a seat that has already been asked twice.
fn nudge_pass(supervisor: &RoomSupervisor) {
    let state_dir = supervisor.state_dir();
    for pending in supervisor.pending_nudges() {
        let key = crate::runner::nudge_key(&pending.seat, &pending.qitem_ids);
        let progress = crate::runner::nudge_progress(state_dir, &pending.room, &pending.seat, &key);
        if progress.attempts >= crate::runner::MAX_NUDGE_ATTEMPTS {
            // Asked the whole ladder and the work is still there. The alert on the room status is the
            // signal that remains, and it stays on.
            continue;
        }
        let attempt = progress.attempts + 1;
        if let Some(last) = progress.last_ts {
            let waited = Utc::now().signed_duration_since(last).num_seconds().max(0) as u64;
            if waited < crate::runner::nudge_delay(attempt).as_secs() {
                continue;
            }
        }
        match crate::runner::nudge_seat(
            state_dir,
            &pending.room,
            &pending.seat,
            &pending.session_name,
            &pending.qitem_ids,
            attempt,
        ) {
            Ok(answer) => info!(
                "nudged {}/{} (attempt {attempt}/{}): {answer}",
                pending.room,
                pending.seat,
                crate::runner::MAX_NUDGE_ATTEMPTS
            ),
            Err(e) => warn!(
                "nudge for {}/{} (attempt {attempt}/{}) was attempted and recorded but not delivered: {e}",
                pending.room,
                pending.seat,
                crate::runner::MAX_NUDGE_ATTEMPTS
            ),
        }
    }
}
