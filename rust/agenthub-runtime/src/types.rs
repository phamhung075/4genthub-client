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
    #[serde(default)]
    pub permission_policy: String,
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

/// Runtime snapshot of a seat for `whoami`, `ps`, and telemetry.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SeatStatus {
    pub room: String,
    pub seat: String,
    pub session_name: String,
    pub runtime: String,
    pub model: Option<String>,
    pub pid: Option<u32>,
    pub state: SeatLifecycleState,
    pub started_at: Option<DateTime<Utc>>,
    pub token_estimate: usize,
    pub last_active: Option<DateTime<Utc>>,
    pub transcript_path: Option<String>,
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
