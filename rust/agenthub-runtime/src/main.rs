use clap::{Parser, Subcommand};
use std::path::PathBuf;
use std::sync::Arc;
use tracing::{error, info, Level};
use tracing_subscriber::FmtSubscriber;

mod context;
mod hooks;
mod ipc;
mod pty;
mod rules;
mod skills;
mod supervisor;
mod types;

use hooks::HookBus;
use ipc::IpcServer;
use supervisor::RoomSupervisor;

#[derive(Parser)]
#[command(name = "agenthub-runtime")]
#[command(about = "Native Rust execution engine for 4genthub seats")]
struct Cli {
    #[arg(short, long, default_value = "~/.config/4genthub")]
    state_dir: String,

    #[arg(short, long)]
    socket: Option<PathBuf>,

    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    /// Start the background IPC supervisor daemon
    Daemon,
    /// Resolve seat identity (Rule 1 parity)
    Whoami {
        #[arg(long)]
        session: Option<String>,
        #[arg(long)]
        json: bool,
    },
    /// Send input to a seat session
    Send {
        session: String,
        text: String,
    },
    /// Show status of a room
    Status {
        room: String,
        #[arg(long)]
        json: bool,
    },
    /// Force active compaction on an omp seat
    Compact {
        session: String,
    },
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let subscriber = FmtSubscriber::builder()
        .with_max_level(Level::INFO)
        .finish();
    let _ = tracing::subscriber::set_global_default(subscriber);

    let cli = Cli::parse();
    let state_dir_str = shellexpand(&cli.state_dir);
    let state_path = PathBuf::from(&state_dir_str);

    let hook_bus = HookBus::new(state_path.join("audit"), 1024);
    let supervisor = Arc::new(RoomSupervisor::new(&state_path, hook_bus));
    let socket_path = cli.socket.unwrap_or_else(IpcServer::default_socket_path);

    match cli.command {
        Commands::Daemon => {
            info!("starting agenthub-runtime daemon...");
            let server = IpcServer::new(socket_path, supervisor);
            server.run().await.map_err(|e| e.into())
        }

        Commands::Whoami { session, json } => {
            let session_name = session.or_else(|| std::env::var("AGENTHUB_SESSION_NAME").ok());
            match supervisor.resolve_whoami(session_name.as_deref(), None) {
                Ok(resp) => {
                    if json {
                        println!("{}", serde_json::to_string_pretty(&resp)?);
                    } else {
                        println!("Rig:     {}", resp.identity.rig_name);
                        println!("Seat:    {}", resp.identity.member_id);
                        println!("Role:    {}", resp.identity.role);
                        println!("Runtime: {}", resp.identity.runtime);
                        println!("Session: {}", resp.identity.session_name);
                    }
                    Ok(())
                }
                Err(e) => {
                    eprintln!("whoami error: {e}");
                    std::process::exit(1);
                }
            }
        }

        Commands::Send { session, text } => {
            pty::TerminalManager::send_text(&session, &text)
                .map_err(|e| format!("send error: {e}"))?;
            println!("delivered to {session}");
            Ok(())
        }

        Commands::Status { room, json } => {
            match supervisor.get_room_status(&room) {
                Ok(statuses) => {
                    if json {
                        println!("{}", serde_json::to_string_pretty(&statuses)?);
                    } else {
                        for s in statuses {
                            println!("{:15} {:12} {:?}", s.seat, s.runtime, s.state);
                        }
                    }
                    Ok(())
                }
                Err(e) => {
                    eprintln!("status error: {e}");
                    std::process::exit(1);
                }
            }
        }

        Commands::Compact { session } => {
            match context::send_force_compact(&session) {
                Ok(res) => {
                    println!("{res}");
                    Ok(())
                }
                Err(e) => {
                    eprintln!("compact error: {e}");
                    std::process::exit(1);
                }
            }
        }
    }
}

fn shellexpand(path: &str) -> String {
    if let Some(stripped) = path.strip_prefix("~/") {
        if let Ok(home) = std::env::var("HOME") {
            return format!("{home}/{stripped}");
        }
    }
    path.to_string()
}
