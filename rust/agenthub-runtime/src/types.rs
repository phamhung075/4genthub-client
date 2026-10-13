use crate::queue::{CloseInput, CreateInput, ListFilter, UpdateInput};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

/// Permission policy posture for seats.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum PermissionPolicy {
    Locked,
    Standard,
    Open,
    Yolo,
    None,
}

impl Default for PermissionPolicy {
    fn default() -> Self {
        PermissionPolicy::Standard
    }
}

impl PermissionPolicy {
    pub fn from_str_loose(s: &str) -> Self {
        match s.trim().to_lowercase().as_str() {
            "locked" | "builtin:locked" => PermissionPolicy::Locked,
            "open" | "builtin:open" => PermissionPolicy::Open,
            "yolo" | "builtin:yolo" => PermissionPolicy::Yolo,
            "none" => PermissionPolicy::None,
            _ => PermissionPolicy::Standard,
        }
    }
}

/// Agent runtime identifier.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum AgentRuntime {
    Omp,
    ClaudeCode,
    Agy,
    Codex,
    Custom(String),
}

impl AgentRuntime {
    pub fn from_str_loose(s: &str) -> Self {
        match s.trim().to_lowercase().as_str() {
            "omp" => AgentRuntime::Omp,
            "claude-code" | "claude" => AgentRuntime::ClaudeCode,
            "agy" => AgentRuntime::Agy,
            "codex" => AgentRuntime::Codex,
            other => AgentRuntime::Custom(other.to_string()),
        }
    }

    pub fn as_str(&self) -> &str {
        match self {
            AgentRuntime::Omp => "omp",
            AgentRuntime::ClaudeCode => "claude-code",
            AgentRuntime::Agy => "agy",
            AgentRuntime::Codex => "codex",
            AgentRuntime::Custom(s) => s.as_str(),
        }
    }
}

/// Seat link connection kind between seats.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum LinkKind {
    DelegatesTo,
    CollaboratesWith,
    EscalatesTo,
    SpawnedBy,
    CanObserve,
}

impl LinkKind {
    pub fn from_str_loose(s: &str) -> Option<Self> {
        match s.trim().to_lowercase().as_str() {
            "delegates_to" => Some(LinkKind::DelegatesTo),
            "collaborates_with" => Some(LinkKind::CollaboratesWith),
            "escalates_to" => Some(LinkKind::EscalatesTo),
            "spawned_by" => Some(LinkKind::SpawnedBy),
            "can_observe" => Some(LinkKind::CanObserve),
            _ => None,
        }
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            LinkKind::DelegatesTo => "delegates_to",
            LinkKind::CollaboratesWith => "collaborates_with",
            LinkKind::EscalatesTo => "escalates_to",
            LinkKind::SpawnedBy => "spawned_by",
            LinkKind::CanObserve => "can_observe",
        }
    }
}

/// Edge linking two seats.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SeatLink {
    pub from: String,
    pub kind: String,
    pub to: String,
}

/// Specification for a single seat in a room.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SeatSpec {
    pub seat_key: String,
    pub seat_type: String,
    pub runtime: String,
    pub model: Option<String>,
    /// `None` means the key was ABSENT, which takes the documented unattended default; `Some("")` means it was
    /// present and blank, which `approval_mode` refuses by name. The difference has to live in the TYPE: a
    /// plain `String` with `#[serde(default)]` folds an absent key into `""`, so the empty-refusal swallowed
    /// the omitted case and both spellings were refused. The client sends the pair distinctly for this reason
    /// (`clientruntime.SeatSpec.PermissionPolicy` is a pointer).
    pub permission_policy: Option<String>,
    pub cwd: Option<String>,
    pub agent_dir: Option<String>,
    #[serde(default)]
    pub env: HashMap<String, String>,
}

/// Specification for a whole room.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RoomSpec {
    pub room: String,
    pub name: Option<String>,
    pub cwd: Option<String>,
    #[serde(default)]
    pub seats: Vec<SeatSpec>,
    #[serde(default)]
    pub links: Vec<SeatLink>,
    #[serde(default)]
    pub overlays: HashMap<String, Vec<String>>,
}

/// Live status of a seat.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SeatLifecycleState {
    Starting,
    Running,
    Idle,
    NeedsInput,
    Blocked,
    Stopped,
    Dead,
}

/// The advisory a seat carries when the daemon has joined the seat's freshness with the room's rail: the
/// seat is up, and the rail is holding work addressed to it that nobody has picked up.
///
/// It is a *join*, not a reading of either side alone (design §4 row 4): the rail alone says work exists,
/// the seat's own state file alone says the seat is quiet, and only together do they say "this live seat
/// is owed something and has not done it". The field is advisory on purpose - a client that does not know
/// it renders the same status it always did, and a client that does can raise it to a human.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SeatAlert {
    /// A stable word a client branches on. `seat_up_with_queued_work`: the session is alive and the room's
    /// rail holds active rows addressed to it.
    pub kind: String,
    /// How long the seat has been quiet: seconds since its own freshness stamp (the runner state file's
    /// `updated_at`), floored at 0. `None` when the seat published no stamp at all - "how long quiet" is
    /// then unknown, and this says unknown rather than inventing a zero.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub quiet_seconds: Option<i64>,
    /// The active (`pending|in-progress|blocked`) qitem ids the rail is holding for this seat.
    pub waiting_qitem_ids: Vec<String>,
}

/// Runtime snapshot of a seat for `whoami`, `ps`, and telemetry.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SeatStatus {
    pub room: String,
    pub seat: String,
    pub session_name: String,
    pub runtime: String,
    pub model: Option<String>,
    /// The pid tmux returned when it created the session - **a record, not proof of liveness**. A seat that
    /// dies keeps this number in whatever was recorded about it; what proves a seat is up is its state file
    /// (`runner::state_file_path`) for an omp seat, or its session still existing for the other runtimes.
    pub pid: Option<u32>,
    pub state: SeatLifecycleState,
    /// Why a seat is not running, when it is not. A start that failed names its reason here instead of
    /// leaving a caller to guess from a `stopped` with no explanation.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
    pub started_at: Option<DateTime<Utc>>,
    pub token_estimate: usize,
    pub last_active: Option<DateTime<Utc>>,
    pub transcript_path: Option<String>,
    /// The freshness/rail join, set only when a seat is up with active rows waiting for it. Omitted from
    /// the wire when there is nothing to say, so every existing reader of this payload sees exactly what
    /// it saw before (design §4 row 4).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub alert: Option<SeatAlert>,
}

/// The exact payload schema returned by `whoami --json` (Rule 1 compatibility).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WhoamiResponse {
    pub resolved_by: String,
    pub identity: WhoamiIdentity,
    pub peers: Vec<WhoamiPeer>,
    pub edges: WhoamiEdges,
    pub transcript: WhoamiTranscript,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WhoamiIdentity {
    #[serde(rename = "rigName")]
    pub rig_name: String,
    #[serde(rename = "memberId")]
    pub member_id: String,
    pub runtime: String,
    #[serde(rename = "sessionName")]
    pub session_name: String,
    pub logical_id: String,
    pub role: String,
    pub model: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WhoamiPeer {
    #[serde(rename = "logicalId")]
    pub logical_id: String,
    #[serde(rename = "sessionName")]
    pub session_name: String,
    pub runtime: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WhoamiEdges {
    pub outgoing: Vec<WhoamiEdgeTarget>,
    pub incoming: Vec<WhoamiEdgeTarget>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WhoamiEdgeTarget {
    pub kind: String,
    pub to: Option<WhoamiEndpoint>,
    pub from: Option<WhoamiEndpoint>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WhoamiEndpoint {
    #[serde(rename = "logicalId")]
    pub logical_id: String,
    #[serde(rename = "sessionName")]
    pub session_name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WhoamiTranscript {
    pub enabled: bool,
    pub path: Option<String>,
    #[serde(rename = "tailCommand")]
    pub tail_command: Option<String>,
}

/// IPC Request frames over the local Unix domain socket.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "action", content = "payload")]
pub enum IpcRequest {
    StartRoom(RoomSpec),
    StopRoom { room: String },
    StopSeat { room: String, seat: String },
    RestartSeat { room: String, seat: String },
    SendText { session: String, text: String },
    GetRoomStatus { room: String },
    ListAllRooms,
    Whoami { session: Option<String>, pid: Option<u32> },
    ForceCompact { session: String },
    Ping,
    /// The room's durable rail (design §3). The room names which store answers; the client renders the
    /// rows and never opens the file, so the daemon stays the only writer.
    QueueCreate { room: String, input: CreateInput },
    QueueList { room: String, filter: ListFilter },
    QueueShow { room: String, qitem_id: String },
    QueueUpdate { room: String, input: UpdateInput },
    QueueClaim { room: String, qitem_id: String, actor_session: String },
    QueueClose { room: String, input: CloseInput },
}

/// IPC Response frames.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IpcResponse {
    pub ok: bool,
    pub error: Option<String>,
    pub data: Option<serde_json::Value>,
}

impl IpcResponse {
    pub fn success(data: serde_json::Value) -> Self {
        Self {
            ok: true,
            error: None,
            data: Some(data),
        }
    }

    pub fn err(msg: impl Into<String>) -> Self {
        Self {
            ok: false,
            error: Some(msg.into()),
            data: None,
        }
    }
}
