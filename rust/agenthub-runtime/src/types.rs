use crate::queue::{CloseInput, CreateInput, ListFilter, UpdateInput};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

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

    /// Whether the seat's pane runs this binary's runner, which owns the agent's channel and publishes the
    /// seat's state file. The other runtimes run their own command in the pane and publish nothing.
    pub fn has_runner(&self) -> bool {
        matches!(self, AgentRuntime::Omp | AgentRuntime::ClaudeCode)
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
    /// The document to install as this seat's own `config.yml`: the hub's rendered `runtime/omp-config.yml`,
    /// which the client fetched with the rest of the spec. The client adds its own non-policy keys to the
    /// text and the runtime installs it verbatim, so the file, the hub's render and the seat's own limits
    /// text cannot disagree about what the seat may do. `None` means NO document was delivered - a seat whose
    /// render carries none, or a room whose spec was written before this field existed - and no file is
    /// written. It travels in the spec rather than being re-derived here: the render is the hub's, and a
    /// second derivation is the second source of truth this field exists to remove.
    pub policy_document: Option<String>,
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

/// THE ACTIVITY WORD: whether the agent in a seat is working, waiting for a person, at its prompt, or
/// something this runtime cannot tell. It is the vocabulary the daemon already defines and the ruling
/// already fixes - `working | needs_input | idle | unknown` (`seat-restart-in-place.ts:121`) - reused
/// word for word, because a second vocabulary is the defect this type exists to remove.
///
/// A DIFFERENT AXIS FROM `SeatLifecycleState`, and folding them together is how a gate goes wrong: a seat
/// is routinely `Running` (its pane is up, its agent handshook) and `Idle` (no turn in flight) at the same
/// moment, and it is the second answer a quiet wait or a reseat needs. `Idle` here is the ONLY word that
/// may authorise a destroyer; `Unknown` is NEVER EVIDENCE and may never authorise one.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ActivityWord {
    /// A turn is in flight, or about to be: the agent is working, retrying, or blocked by something that
    /// is not a person.
    Working,
    /// Mid-turn AND waiting for a person - a dialog this pane cannot answer. A destroyer here eats a turn
    /// that was about to continue, which is why this word must never release one.
    NeedsInput,
    /// At the prompt with no turn in flight. The only word that authorises.
    Idle,
    /// Not evidence of anything: nothing was published, the agent is gone, or the word is one this build
    /// does not know. Refuses by definition rather than by accident.
    Unknown,
}

impl ActivityWord {
    /// The daemon's own spelling, so a reader comparing this against `agentActivity.state` compares like
    /// with like instead of against a private synonym.
    pub fn as_str(self) -> &'static str {
        match self {
            ActivityWord::Working => "working",
            ActivityWord::NeedsInput => "needs_input",
            ActivityWord::Idle => "idle",
            ActivityWord::Unknown => "unknown",
        }
    }

    /// What the runner's OWN published state is in the activity vocabulary - one mapping, in one place,
    /// because the runner is the producer of both words and they must never be derived twice.
    ///
    /// The left-hand words are the runner's (`runner::write_state`), the right-hand ones are the daemon's.
    /// Read each line as a claim about the AGENT:
    ///
    ///   * `idle` - the turn ended and nothing is in flight: at the prompt.
    ///   * `needs_input` - a dialog this pane cannot answer: mid-turn, blocked on a person.
    ///   * `retrying` / `blocked` / `working` - work is in flight or is about to be retried.
    ///   * every death (`agent_exited`, `agent_stdio_eof`, `agent_socket_closed`) and every word not
    ///     listed - `Unknown`. A dead agent must never read as `idle`: the seat is not at a prompt, its
    ///     agent is gone, and `Unknown` is what says so while refusing to authorise anything.
    pub fn from_runner_state(state: &str) -> ActivityWord {
        match state {
            "idle" => ActivityWord::Idle,
            "needs_input" => ActivityWord::NeedsInput,
            "working" | "retrying" | "blocked" => ActivityWord::Working,
            _ => ActivityWord::Unknown,
        }
    }

    /// The word back from its own spelling: the reader's side of `as_str`, so a word read off the wire is
    /// the same value a producer wrote, and a synonym this build does not know is `Unknown` rather than a
    /// silent guess.
    pub fn from_str_loose(word: &str) -> ActivityWord {
        match word {
            "idle" => ActivityWord::Idle,
            "needs_input" => ActivityWord::NeedsInput,
            "working" => ActivityWord::Working,
            _ => ActivityWord::Unknown,
        }
    }

    /// The word for a seat's published state document, `Unknown` when there is no document at all or the
    /// document carries no word. Both cases are "a report that cannot tell", which is what `Unknown`
    /// means; neither may be read as "not running".
    pub fn from_state_document(document: Option<&serde_json::Value>) -> ActivityWord {
        document
            .and_then(|d| d.get("activity"))
            .and_then(|a| a.as_str())
            .map(ActivityWord::from_str_loose)
            .unwrap_or(ActivityWord::Unknown)
    }
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
    /// THE WORD A DESTROYER READS, on its own axis from `state`: parsed from the seat's own published
    /// state document (`runner::state_file_path`) - the runner writes it beside `state` - and `Unknown`
    /// when the seat published nothing or no word. Never a guess, and never a word that authorises.
    pub activity: ActivityWord,
    /// The runner's own word this activity was derived from, so a reader sees the evidence instead of
    /// trusting the derivation. Omitted when the seat published nothing, because there is no source then.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub activity_source: Option<String>,
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

/// One entry of the session list (`ListSessions`): one seat of one hosted room, in the CLI's OWN
/// camelCase spellings so the client's existing session-list parser reads either source unchanged.
///
/// THE FIELD NAMES ARE THE CONTRACT, not a preference. `clientsync.parseRigPsSessions` reads
/// `canonicalSessionName` FIRST - the name a person passes to `--session` - then `rigName` and
/// `logicalId` (whose member after the FIRST dot is the seat key) and `sessionStatus`, and it SKIPS an
/// entry with no usable name. Serving `SeatStatus`'s snake_case keys here instead would make every native
/// session invisible to that parser while looking like a working answer.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionEntry {
    /// `<room>-<seat>@<room>`: the room IS the rig for a native room, the rule the client's own
    /// `SeatSessionName` doc states, and the name a person addresses a seat by.
    #[serde(rename = "canonicalSessionName")]
    pub canonical_session_name: String,
    /// The runtime's own session name (`agenthub--<room>--<seat>`): what tmux is addressed by.
    #[serde(rename = "sessionName")]
    pub session_name: String,
    #[serde(rename = "rigName")]
    pub rig_name: String,
    /// `<room>.<seat>`: the client recovers the seat key from the member after the first dot, and a bare
    /// seat key names no member to that reader.
    #[serde(rename = "logicalId")]
    pub logical_id: String,
    /// `running` or `stopped`, and nothing else. The client reduces every status to those two values, so
    /// the reduction happens once, here, where the evidence is - a third word would be reduced anyway.
    #[serde(rename = "sessionStatus")]
    pub session_status: String,
    #[serde(rename = "agentActivity")]
    pub agent_activity: SessionActivity,
    pub runtime: String,
    pub model: Option<String>,
    #[serde(rename = "transcriptPath")]
    pub transcript_path: Option<String>,
    /// Why the seat is not running, when it is not: the status bridge reports it as the seat's diagnostic text.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// The activity half of a session entry, nested at `agentActivity.state` because that is where the
/// client's node parser looks for it. `source` is the seat's own published word, so a reader sees the
/// evidence the state was derived from instead of trusting the derivation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionActivity {
    pub state: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,
}

/// A transcript read: the text, and the file it came from. The path travels with the text because a
/// reader who gets text that looks wrong needs to know which file produced it.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TranscriptAnswer {
    pub session: String,
    pub path: String,
    pub content: String,
}

/// A pane capture: the text, and the seat it came from.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CaptureAnswer {
    pub session: String,
    pub content: String,
}

/// IPC Request frames over the local Unix domain socket.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "action", content = "payload")]
pub enum IpcRequest {
    StartRoom(RoomSpec),
    StopRoom { room: String },
    StopSeat { room: String, seat: String },
    /// `fresh` drops the seat's saved agent sessions before the start, so the new agent begins without the
    /// old one's context; absent means a plain restart (the wire older callers already write).
    ///
    /// `policy_document` is the seat's `config.yml` when the CALLER has a fresh one: the client fetched the
    /// hub's render for this restart, and the document is written into the room's own spec BEFORE the seat
    /// starts from it, so every later route that starts this seat - a heal, a restore, a restart from a
    /// caller with no document - installs the same file instead of the one the room was last defined with.
    /// Absent leaves the room's record alone, which is what a caller that has nothing newer must send.
    RestartSeat {
        room: String,
        seat: String,
        #[serde(default)]
        fresh: bool,
        policy_document: Option<String>,
    },
    /// Record the model a seat starts with. It is written to the room's own spec, so it applies to the
    /// seat's NEXT start (a running agent keeps the model it was spawned with).
    SetSeatModel { room: String, seat: String, model: String },
    /// One message to one seat, with the origin the runner must name if it cannot deliver or cannot keep it:
    /// the caller's session name and the verb that carried the text. Absent fields stay absent - a caller
    /// that does not say who it is is named as unnamed rather than guessed at.
    SendText {
        session: String,
        text: String,
        #[serde(default)]
        from: Option<String>,
        #[serde(default)]
        verb: Option<String>,
    },
    GetRoomStatus { room: String },
    /// The links a room's spec declares between its seats, for the status bridge's topology report.
    GetRoomLinks { room: String },
    ListAllRooms,
    /// The session list this daemon can answer for: every seat of every room it hosts, in the CLI's own
    /// keys (`SessionEntry`). A `room` narrows it to one room; `None` means every room this daemon hosts.
    ///
    /// It exists so the connector and agent stop exec-ing `rig ps --json`: the client's own session-list
    /// parser reads this answer unchanged, which is what makes the CLI and the runtime comparable in one
    /// test instead of described in two.
    ListSessions {
        room: Option<String>,
    },
    /// A seat's transcript, read from the file its runner publishes (`transcript_path`). `lines` bounds
    /// it to the newest lines; `None` reads the file whole.
    Transcript {
        session: String,
        lines: Option<usize>,
    },
    /// A seat's pane, taken through the same `TerminalManager` every other tmux call in this crate uses.
    Capture {
        session: String,
        lines: Option<usize>,
    },
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

#[cfg(test)]
mod tests {
    use super::{ActivityWord, IpcRequest, SeatSpec};

    /// THE VOCABULARY IS THE DAEMON'S, spelled the same way, because a second vocabulary is the defect this
    /// type exists to remove: these four strings are what a reader compares against `agentActivity.state`,
    /// and the ruling fixes them (`seat-restart-in-place.ts:121`).
    #[test]
    fn the_four_words_are_the_daemons_four_words() {
        assert_eq!(ActivityWord::Working.as_str(), "working");
        assert_eq!(ActivityWord::NeedsInput.as_str(), "needs_input");
        assert_eq!(ActivityWord::Idle.as_str(), "idle");
        assert_eq!(ActivityWord::Unknown.as_str(), "unknown");
        // AND THE WIRE AGREES WITH THE ACCESSOR, because a word that serializes differently from `as_str` is
        // two vocabularies wearing one name - exactly what a reader comparing the two would trip on.
        for word in [
            ActivityWord::Working,
            ActivityWord::NeedsInput,
            ActivityWord::Idle,
            ActivityWord::Unknown,
        ] {
            assert_eq!(
                serde_json::to_string(&word).expect("encode"),
                format!("\"{}\"", word.as_str())
            );
        }
    }

    /// WHAT THE RUNNER'S OWN PUBLISHED STATES ARE, one case per word `runner::write_state` writes.
    #[test]
    fn the_runner_states_map_to_the_daemons_vocabulary() {
        for (state, want) in [
            ("idle", ActivityWord::Idle),
            ("needs_input", ActivityWord::NeedsInput),
            ("working", ActivityWord::Working),
            ("retrying", ActivityWord::Working),
            ("blocked", ActivityWord::Working),
            // EVERY DEATH IS UNKNOWN, NEVER IDLE: the agent is gone, the seat is not at its prompt, and a
            // word that authorised here would let a reseat read "nothing to lose" off a dead pane.
            ("agent_exited", ActivityWord::Unknown),
            ("agent_stdio_eof", ActivityWord::Unknown),
            ("agent_socket_closed", ActivityWord::Unknown),
            // And a word nobody here knows is not evidence of anything.
            ("drifting", ActivityWord::Unknown),
            ("", ActivityWord::Unknown),
        ] {
            assert_eq!(
                ActivityWord::from_runner_state(state),
                want,
                "state {state:?}"
            );
        }
    }

    /// THE PARSE SIDE ROUND-TRIPS the four words, and anything else - a synonym some future producer
    /// invents, a case the daemon never serves - lands on `Unknown` rather than being guessed at.
    #[test]
    fn the_word_round_trips_and_a_synonym_is_unknown() {
        for word in [
            ActivityWord::Working,
            ActivityWord::NeedsInput,
            ActivityWord::Idle,
            ActivityWord::Unknown,
        ] {
            assert_eq!(ActivityWord::from_str_loose(word.as_str()), word);
        }
        assert_eq!(ActivityWord::from_str_loose("busy"), ActivityWord::Unknown);
        assert_eq!(ActivityWord::from_str_loose("IDLE"), ActivityWord::Unknown);
    }

    /// A SEAT THAT PUBLISHED NOTHING, OR PUBLISHED NO WORD, IS `Unknown`: the two cases that must never be
    /// read as "not running", and the reason this is parsed rather than re-derived from `state`.
    #[test]
    fn a_document_without_a_word_is_unknown() {
        assert_eq!(
            ActivityWord::from_state_document(None),
            ActivityWord::Unknown
        );
        let without =
            serde_json::json!({"state": "idle", "classification": "", "resume_attempts": 0});
        assert_eq!(
            ActivityWord::from_state_document(Some(&without)),
            ActivityWord::Unknown
        );
        let with = serde_json::json!({"state": "idle", "activity": "idle"});
        assert_eq!(
            ActivityWord::from_state_document(Some(&with)),
            ActivityWord::Idle
        );
    }

    /// THE WIRE NAME IS THE CONTRACT, and no test on either side can see a rename on the other: the client
    /// writes `policy_document` in Go and this parses it in Rust, so a field renamed here and not there is a
    /// document that silently never arrives - every unit test still green, every seat ungoverned. This parses
    /// the frames the client ACTUALLY writes, both spellings of the restart.
    #[test]
    fn the_policy_document_travels_under_the_clients_own_key() {
        let document = "tools:\n  approval:\n    edit: deny\n";
        let spec: SeatSpec = serde_json::from_str(
            r#"{"seat_key":"dev","seat_type":"developer","runtime":"omp","model":null,
                "permission_policy":null,"cwd":null,"agent_dir":null,"env":{},
                "policy_document":"tools:\n  approval:\n    edit: deny\n"}"#,
        )
        .expect("a spec carrying a document");
        assert_eq!(spec.policy_document.as_deref(), Some(document));

        let older: SeatSpec = serde_json::from_str(
            r#"{"seat_key":"dev","seat_type":"developer","runtime":"omp","model":null,
                "permission_policy":null,"cwd":null,"agent_dir":null,"env":{}}"#,
        )
        .expect("a spec written before this field existed must still parse");
        assert_eq!(older.policy_document, None);

        let frame: IpcRequest = serde_json::from_str(
            r#"{"action":"RestartSeat","payload":{"room":"room1","seat":"dev",
                "policy_document":"tools:\n  approval:\n    edit: deny\n"}}"#,
        )
        .expect("the restart frame the client writes");
        match frame {
            IpcRequest::RestartSeat {
                room,
                seat,
                fresh,
                policy_document,
            } => {
                assert_eq!((room.as_str(), seat.as_str()), ("room1", "dev"));
                assert!(!fresh, "an absent `fresh` key is a plain restart");
                assert_eq!(policy_document.as_deref(), Some(document));
            }
            other => panic!("expected RestartSeat, got {other:?}"),
        }

        // AND THE FRAME WITH NO DOCUMENT: the key is absent rather than null, which is what leaves the room's
        // own record alone instead of overwriting it with an empty document.
        let plain: IpcRequest =
            serde_json::from_str(r#"{"action":"RestartSeat","payload":{"room":"room1","seat":"dev"}}"#)
                .expect("the restart frame a caller with nothing newer writes");
        match plain {
            IpcRequest::RestartSeat { policy_document, .. } => assert_eq!(policy_document, None),
            other => panic!("expected RestartSeat, got {other:?}"),
        }
    }
}
