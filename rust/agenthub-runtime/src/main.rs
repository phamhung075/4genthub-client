use clap::{Parser, Subcommand};
use std::path::PathBuf;
use std::sync::Arc;
use tracing::{error, info, Level};
use tracing_subscriber::FmtSubscriber;

mod context;
mod frame;
mod hooks;
mod ipc;
mod pty;
mod queue;
mod runner;
mod rules;
mod skills;
mod supervisor;
mod types;

use hooks::HookBus;
use ipc::IpcServer;
use supervisor::RoomSupervisor;

/// The environment a seat's agent is allowed to see: everything the runtime was given, **minus** anything
/// that addresses another rig.
///
/// Default deny, by prefix, because the failure this prevents is one of omission: the runtime only *adds*
/// its own `AGENTHUB_*` variables, so without this a room started the way the operator starts one hands its
/// agent the live rig's addressing - `OPENRIG_HOME`, `OPENRIG_URL`, the activity-hook token, `OPENRIG_DB`,
/// and the session/node/transcript ids - and an agent process can then reach the live daemon from inside a
/// scratch room. Measured before this existed: nine `OPENRIG_*` variables were visible in the agent's own
/// `/proc/<pid>/environ`.
pub const ENV_DENY_PREFIXES: [&str; 1] = ["OPENRIG_"];

pub fn env_is_denied(key: &str) -> bool {
    ENV_DENY_PREFIXES.iter().any(|p| key.starts_with(p))
}

/// The denied keys actually present in this process's environment.
///
/// Used to launch a seat's pane through `env -u <key> …`, which is the only mechanism that satisfies the
/// design's predicate — "no `~/.openrig` path in any room pid's argv or environ" — for *every* process the
/// room owns, not just the agent: `/proc/<pid>/environ` is the environment the kernel was given at `exec`,
/// so removing a variable from a running process would not be visible where the predicate is measured, and
/// tmux gives a pane its session environment from the tmux *server* when the variable is not in the client's.
pub fn denied_env_keys() -> Vec<String> {
    std::env::vars()
        .map(|(key, _)| key)
        .filter(|key| env_is_denied(key))
        .collect()
}

/// Remove every denied variable from a child's environment. Callers are the two places the runtime starts
/// something: the tmux client that becomes a seat's pane, and the agent the runner spawns.
pub fn scrub_env(command: &mut std::process::Command) -> &mut std::process::Command {
    let denied: Vec<String> = std::env::vars()
        .map(|(key, _)| key)
        .filter(|key| env_is_denied(key))
        .collect();
    for key in denied {
        command.env_remove(key);
    }
    command
}

#[derive(Parser)]
#[command(name = "agenthub-runtime")]
#[command(about = "Native Rust execution engine for 4genthub seats")]
struct Cli {
    /// Top-level state directory (rooms, specs, transcripts). `-s` is this one.
    #[arg(short, long, default_value = "~/.config/4genthub")]
    state_dir: String,

    /// IPC socket path. Long form only: `-s` already means `--state-dir`, and clap refuses two arguments
    /// with the same short — the whole binary panicked before it could parse a subcommand.
    #[arg(long)]
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
    /// Be a seat's foreground process: spawn its agent, own its channel, proxy its pane
    Runner {
        /// <room>/<seat>, as `4genteam ps` names a seat
        target: String,
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

        Commands::Runner { target } => match runner::run(&state_path, &target) {
            Ok(()) => Ok(()),
            Err(e) => {
                eprintln!("runner error: {e}");
                std::process::exit(1);
            }
        },

        Commands::Compact { session } => {
            match runner::request_compact(&state_path, &session) {
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
