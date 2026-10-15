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
/// agent a live rig's addressing and session/node ids, and an agent process can then reach that rig from
/// inside a scratch room.
///
/// `RIGGED_` is denied because the client still reads `RIGGED_SESSION_NAME` as `legacySessionEnv` (a
/// client-side constant), so an agent that inherits a live rig's pre-rename session name is handed an
/// identity that is not its seat's - exactly the falsification this list exists to stop.
///
/// `AGENTHUB_` is deliberately NOT denied, and the boundary is a decision rather than an oversight. The
/// supervisor inserts this room's own `AGENTHUB_ROOM`, `AGENTHUB_SEAT`, `AGENTHUB_ROLE`, `AGENTHUB_RUNTIME`,
/// `AGENTHUB_SESSION_NAME` and `AGENTHUB_MODEL` *after* the scrub, so those would survive a prefix denial - but
/// the client also reads `AGENTHUB_TOKEN` and `AGENTHUB_URL`, which nothing re-inserts, and whether a room's
/// agent may hold an inherited cloud token is a design question. Named on U6's row, not decided here.
pub const ENV_DENY_PREFIXES: [&str; 1] = ["RIGGED_"];

/// Keys denied by their EXACT name, because a prefix cannot name a credential without over-removing.
///
/// `AGENTHUB_CONNECTOR_TOKEN` is the pairing token the local client uses against the cloud connector, and
/// nothing a seat does reads it: a seat addresses its own daemon (`AGENTHUB_URL`, `AGENTHUB_TOKEN`). Handing
/// every seat in a room the connector credential is a leak with no consumer, and it left the room twice over,
/// measured on this box 2026-10-15 - on the `tmux new-session … -e AGENTHUB_CONNECTOR_TOKEN=<value>` argv, and
/// in the tmux server's global environment, where both `ps -eo args` and `tmux show-environment -g` read it.
///
/// The boundary is exact-name and deliberate: `AGENTHUB_TOKEN` (the seat's own MCP credential, named by its
/// mcp.json) and `DEEPSEEK_API_KEY` (the provider key its model calls need) are NOT denied - a seat breaks
/// without them, so what is wrong there is the argv class, not the key. Named on U6's row, not decided here.
pub const ENV_DENY_NAMES: [&str; 1] = ["AGENTHUB_CONNECTOR_TOKEN"];

pub fn env_is_denied(key: &str) -> bool {
    ENV_DENY_PREFIXES.iter().any(|p| key.starts_with(p)) || ENV_DENY_NAMES.contains(&key)
}

/// The denied keys actually present in this process's environment, for the exec wrapper.
///
/// Used to launch a seat's pane through `env -u <key> …`, which is the only mechanism that satisfies the
/// design's predicate — "no orchestrator state path in any room pid's argv or environ" — for *every* process the
/// room owns, not just the agent: `/proc/<pid>/environ` is the environment the kernel was given at `exec`,
/// so removing a variable from a running process would not be visible where the predicate is measured, and
/// tmux gives a pane its session environment from the tmux *server* when the variable is not in the client's.
///
/// PREFIX keys only. An exact-name credential is removed before the pane exists — out of the seat's own
/// environment and off the tmux server's global one, see `denied_name_keys` — so there is nothing left for a
/// wrapper to unset, and naming it here would print the name on the pane's command line for the seat's whole
/// life, which is the surface the owner's own measurement reads.
pub fn denied_env_keys() -> Vec<String> {
    std::env::vars()
        .map(|(key, _)| key)
        .filter(|key| ENV_DENY_PREFIXES.iter().any(|p| key.starts_with(p)))
        .collect()
}

/// The exact-name denied keys present in this process's environment: the credential class, cleaned off the
/// tmux server's global environment before a pane is created.
///
/// A server that already exists keeps the environment it was created with, and a pane takes the SERVER's
/// environment - so dropping the key from the seat's env map and from the `-e` list is not enough: measured on
/// this box 2026-10-15, `tmux show-environment -g` held `AGENTHUB_CONNECTOR_TOKEN` while this runtime was the
/// only thing creating sessions, and the panes inherited it from there.
pub fn denied_name_keys() -> Vec<String> {
    std::env::vars()
        .map(|(key, _)| key)
        .filter(|key| ENV_DENY_NAMES.contains(&key.as_str()))
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

/// The tmux root this runtime owns: `<state dir>/tmux`.
///
/// Derived rather than configured, so the path is discoverable from the same place the rooms are: the
/// operator's attach path and this runtime agree without either memorising a name.
pub fn tmux_root_for(state_dir: &std::path::Path) -> std::path::PathBuf {
    state_dir.join("tmux")
}

/// Point every tmux client this runtime starts at a server it owns, by setting `TMUX_TMPDIR` to the path
/// `tmux_root_for` derives, once, before anything can call tmux.
///
/// WHY THE SERVER HAD TO BECOME OURS, measured on this machine rather than reasoned: a pane takes its
/// environment from the tmux SERVER it is created on, so a scratch room started from a completely poisoned
/// daemon still produced a pane holding six host variables, none of which came from the daemon that was
/// started. The host's server held thirteen foreign keys GLOBALLY, and `scrub_env` on the client cannot reach them: it removes what the daemon
/// inherited, and tmux propagates exactly those, so anything living only in the server survives. The
/// scrub and this function are one change rather than two: the server takes the environment of whoever
/// creates it, and the client that creates ours is already scrubbed.
///
/// A failure here is logged, not fatal: a runtime that cannot write its state directory has larger
/// problems, and refusing to start would be a worse answer than starting on the host's server.
pub fn own_tmux_root(state_dir: &std::path::Path) -> std::io::Result<std::path::PathBuf> {
    let root = tmux_root_for(state_dir);
    std::fs::create_dir_all(&root)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&root, std::fs::Permissions::from_mode(0o700));
    }
    std::env::set_var("TMUX_TMPDIR", &root);
    Ok(root)
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

    // Before anything can start tmux: every room's panes live on a server THIS runtime owns, created
    // through an already-scrubbed client. See `own_tmux_root` for the measurement that made it necessary.
    match own_tmux_root(&state_path) {
        Ok(root) => info!("tmux root owned by this runtime: {}", root.display()),
        Err(e) => tracing::warn!("could not own a tmux root under {}: {e}; sessions will use the host's server", state_path.display()),
    }

    let hook_bus = HookBus::new(state_path.join("audit"), 1024);
    // The socket is resolved BEFORE the supervisor exists, because the supervisor hands it to every seat it
    // starts: a seat's own client reaches its daemon through `$AGENTHUB_RUNTIME_SOCKET`, and only the daemon
    // knows which path it is answering on.
    let socket_path = cli.socket.unwrap_or_else(IpcServer::default_socket_path);
    let supervisor = Arc::new(RoomSupervisor::new(
        &state_path,
        hook_bus,
        socket_path.clone(),
    ));

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
            let answer = supervisor
                .deliver_text(&session, &text)
                .map_err(|e| format!("send error: {e}"))?;
            println!("{}", send_receipt(&session, &answer));
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

/// The one line `agenthub-runtime send` prints for the answer the supervisor produced.
///
/// ONLY THE OUTCOME THAT PROVES A SUBMISSION IS A DELIVERY. The runtime has two delivery paths and two
/// vocabularies: a seat it hosts no runner for gets a pane paste, answered `submitted` (the pane changed
/// after Enter) or `unconfirmed` (it did not); a runner seat answers `relayed` the moment the runner wrote
/// the frame into the agent's channel - which is NOT the agent having taken it, its own answer going to the
/// pane and never back here. This printed `delivered to` for EVERY answer except `unconfirmed`, the refusal
/// included. `4genteam send` carried the same rule and was cured of it; this is the second consumer of that
/// document, so it follows the same rule: everything but `submitted` says only what is known.
fn send_receipt(session: &str, answer: &serde_json::Value) -> String {
    match answer["outcome"].as_str() {
        Some("submitted") => format!("delivered to {session}"),
        Some("relayed") => format!(
            "relayed to {session}, submission unconfirmed: the runner wrote the frame into the agent's channel and the agent's own answer is not reported back"
        ),
        Some("unconfirmed") => format!("pasted to {session}, submission unconfirmed"),
        Some(other) => format!(
            "submission unconfirmed for {session}: the runtime answered {other:?}, which is no submission verdict"
        ),
        None => format!(
            "submission unconfirmed for {session}: the runtime's answer carries no submission verdict"
        ),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;

    /// The rule the client's own receipt was cured of, applied to the runtime's second consumer of the same
    /// document: only the outcome that proves a submission is a delivery.
    #[test]
    fn only_a_submission_is_a_delivery() {
        let of = |doc: &str| send_receipt("room-a-dev", &serde_json::from_str(doc).unwrap());
        assert_eq!(
            of(r#"{"delivered":"room-a-dev","outcome":"submitted"}"#),
            "delivered to room-a-dev"
        );
        assert!(
            of(r#"{"delivered":"room-a-dev","outcome":"relayed","runner":{"relayed":"prompt"}}"#)
                .starts_with("relayed to room-a-dev, submission unconfirmed"),
            "a relayed frame is the runner having taken it, not the agent"
        );
        assert_eq!(
            of(r#"{"delivered":"room-a-dev","outcome":"unconfirmed"}"#),
            "pasted to room-a-dev, submission unconfirmed"
        );
        for other in [
            r#"{"delivered":"room-a-dev"}"#,
            r#"{"delivered":"room-a-dev","outcome":"queued"}"#,
        ] {
            let line = of(other);
            assert!(
                line.starts_with("submission unconfirmed for room-a-dev"),
                "{line}"
            );
            assert!(
                !line.contains("delivered to"),
                "a delivery was claimed for {other}: {line}"
            );
        }
    }

    /// The list is a list of *prefixes* because the addresses this runtime must not pass on change names over
    /// time, and the rename left an alias the client still honours. The second half is the boundary: removing
    /// too much is its own defect, and `HOME`/`PATH`/`XDG_RUNTIME_DIR` are what a seat legitimately needs.
    #[test]
    fn the_deny_list_covers_every_prefix_that_addresses_a_rig() {
        for denied in [
            "RIGGED_SESSION_NAME",
        ] {
            assert!(env_is_denied(denied), "{denied} addresses a rig and must be denied");
        }
        for kept in [
            "AGENTHUB_ROOM",
            "AGENTHUB_SEAT",
            "AGENTHUB_SESSION_NAME",
            "HOME",
            "PATH",
            "XDG_RUNTIME_DIR",
        ] {
            assert!(
                !env_is_denied(kept),
                "{kept} is either this room's own or not ours to remove"
            );
        }
    }

    /// The exact-name half of the predicate. The connector credential is denied by name - and the keys a seat
    /// actually reads are not, which is why this is a name list and not a wider prefix: `AGENTHUB_TOKEN` is
    /// named by the seat's own mcp.json and `DEEPSEEK_API_KEY` is what its model calls carry.
    #[test]
    fn the_connector_token_is_denied_by_name_while_a_seats_own_keys_are_not() {
        assert!(env_is_denied("AGENTHUB_CONNECTOR_TOKEN"));
        for kept in [
            "AGENTHUB_TOKEN",
            "AGENTHUB_URL",
            "AGENTHUB_ROOM",
            "DEEPSEEK_API_KEY",
            "HOME",
        ] {
            assert!(!env_is_denied(kept), "{kept} is what a seat works with");
        }
    }

    /// The two classes are handled by two mechanisms, and a key that reached the wrong one would break the
    /// owner's measurement: the credential is cleaned off the tmux server and must never be WRAPPED, because a
    /// wrapper prints the name on the pane's own command line for the seat's whole life, while a prefix key is
    /// wrapped and published nowhere.
    #[test]
    fn the_credential_is_removed_by_the_server_and_never_by_a_wrapper() {
        std::env::set_var("AGENTHUB_CONNECTOR_TOKEN", "sentinel");
        std::env::set_var("RIGGED_TESTONLY_CLASS", "poison");
        let named = denied_name_keys();
        let wrapped = denied_env_keys();
        std::env::remove_var("AGENTHUB_CONNECTOR_TOKEN");
        std::env::remove_var("RIGGED_TESTONLY_CLASS");

        assert!(named.contains(&"AGENTHUB_CONNECTOR_TOKEN".to_string()), "{named:?}");
        assert!(
            !wrapped.contains(&"AGENTHUB_CONNECTOR_TOKEN".to_string()),
            "the credential would be named on every pane's command line: {wrapped:?}"
        );
        assert!(wrapped.contains(&"RIGGED_TESTONLY_CLASS".to_string()), "{wrapped:?}");
    }

    /// The mechanism rather than the list: a denied variable is REMOVED from the child's environment, and a
    /// variable that is not denied is left alone. Synthetic names, so this cannot disturb the environment the
    /// other tests in this binary read - and it asserts the removal is *explicit* (`env_remove`), because
    /// `/proc/<pid>/environ` is what the design's predicate is measured against.
    #[test]
    fn scrub_removes_denied_keys_and_leaves_the_rest() {
        std::env::set_var("RIGGED_TESTONLY_SCRUB", "poison");
        std::env::set_var("NOT_OURS_TESTONLY_SCRUB", "keep");

        let mut cmd = Command::new("true");
        scrub_env(&mut cmd);
        let envs: Vec<(String, Option<String>)> = cmd
            .get_envs()
            .map(|(k, v)| {
                (
                    k.to_string_lossy().into_owned(),
                    v.map(|v| v.to_string_lossy().into_owned()),
                )
            })
            .collect();

        std::env::remove_var("RIGGED_TESTONLY_SCRUB");
        std::env::remove_var("NOT_OURS_TESTONLY_SCRUB");

        for denied in ["RIGGED_TESTONLY_SCRUB"] {
            assert!(
                envs.iter().any(|(k, v)| k == denied && v.is_none()),
                "{denied} must arrive at the child as an explicit removal, not as an absent variable"
            );
        }
        assert!(
            !envs.iter().any(|(k, _)| k == "NOT_OURS_TESTONLY_SCRUB"),
            "a variable that is not denied must not be touched by the scrub"
        );
    }

    /// The tmux root is DERIVED from the state directory - the interface the operator's attach path and this
    /// runtime share - and it is created without group or other access, because it is where one runtime's
    /// panes live. Setting `TMUX_TMPDIR` is the mechanism, so it is asserted directly rather than by opening
    /// a pane: it is what every tmux client this runtime starts inherits.
    #[test]
    fn the_tmux_root_is_derived_from_the_state_dir_and_created_private() {
        // The lock first: this test moves the process-wide `TMUX_TMPDIR` to its own directory and deletes that
        // directory at the end, which is exactly the window in which another test's tmux clients would resolve
        // a server that no longer exists. See `pty::test_servers` for the measurement.
        let (_tmux, _server) = crate::pty::test_servers::own("tmux-root");
        let dir = std::env::temp_dir().join(format!("runtime-tmux-root-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("scratch state dir");

        let root = tmux_root_for(&dir);
        assert_eq!(root, dir.join("tmux"), "the name is derived, not configured");
        assert!(!root.exists(), "deriving the path creates nothing");

        let returned = own_tmux_root(&dir).expect("owning the root");
        assert_eq!(returned, root);
        assert!(root.is_dir(), "owning it means creating it");
        assert_eq!(
            std::env::var("TMUX_TMPDIR").ok().as_deref(),
            root.to_str(),
            "every tmux client this runtime starts must inherit the runtime's own server directory"
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&root).expect("root exists").permissions().mode() & 0o777;
            assert_eq!(mode, 0o700, "one runtime's panes are nobody else's to read");
        }

        let _ = std::fs::remove_dir_all(&dir);
    }
}
