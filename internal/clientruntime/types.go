package clientruntime

import "time"

// SeatStatus is the live status of a seat reported by agenthub-runtime.
type SeatStatus struct {
	Room        string  `json:"room"`
	Seat        string  `json:"seat"`
	SessionName string  `json:"session_name"`
	Runtime     string  `json:"runtime"`
	Model       *string `json:"model,omitempty"`
	Pid         *uint32 `json:"pid,omitempty"`
	State       string  `json:"state"`
	// Detail is WHICH EVIDENCE the state rests on - the agent's own handshake, the grace a non-omp pane
	// outlived, or the death the state file names. The runtime OMITS it when it has nothing to say
	// (`skip_serializing_if` on `SeatStatus::detail` in rust), so it is a POINTER: nil is "no evidence given",
	// never an empty one. It is rendered by `up` beside the state, so the operator sees the proof and not just
	// the verdict.
	Detail         *string    `json:"detail,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	TokenEstimate  int        `json:"token_estimate"`
	LastActive     *time.Time `json:"last_active,omitempty"`
	TranscriptPath *string    `json:"transcript_path,omitempty"`
	// Alert is the runtime's advisory for this seat, and it is OPTIONAL: the runtime OMITS the key entirely
	// when the seat carries none, so the field is a POINTER - nil is "no alert", and it must never be read
	// as an alert with nothing in it. The runtime's own rendering of the same decision is
	// `Option<SeatAlert>` with `skip_serializing_if` in rust/agenthub-runtime/src/types.rs.
	Alert *SeatAlert `json:"alert,omitempty"`
}

// SeatAlert is the advisory a seat carries when the runtime has JOINED the seat's own freshness with the
// room's rail: the seat is up, and the rail is holding active work addressed to it that nobody has picked
// up. Neither half means this on its own - the rail alone says work exists, the seat's state file alone
// says the seat is quiet - which is why the join is computed by the runtime and carried here as one fact.
//
// THE FIELD'S PRESENCE IS THE SIGNAL, on both levels, and that is what the pointers are for:
//   - an absent `alert` key is NO ALERT, so SeatStatus.Alert is nil rather than a zero-valued struct;
//   - an absent `quiet_seconds` is a seat that published NO freshness stamp, so how long it has been quiet
//     is UNKNOWN - not zero. A pointer is the only Go shape that keeps "unknown" and "zero seconds" apart,
//     and the two are opposites from a reader's side: 0 is a seat that acted a moment ago.
//
// Every other key of SeatStatus is unchanged, and this one is additive: a reader that does not know it
// decodes the same payload it always did. Unknown fields are ignored deliberately (no DisallowUnknownFields
// anywhere in this package), so a runtime that learns a new key before the client does costs nothing.
type SeatAlert struct {
	// Kind is the stable word a client branches on. `seat_up_with_queued_work` is the one the runtime
	// defines today: the session is alive and the room's rail holds active rows addressed to this seat.
	Kind string `json:"kind"`
	// QuietSeconds is how long the seat has been quiet - seconds since its own freshness stamp (the runner
	// state file's updated_at), floored at 0 by the runtime. nil means the seat published no stamp at all,
	// so the quiet time is unknown and this says unknown rather than inventing a zero.
	QuietSeconds *int64 `json:"quiet_seconds,omitempty"`
	// WaitingQitemIDs are the active (pending|in-progress) qitem ids the rail is holding for this seat. A
	// BLOCKED row is not here: it waits on the row its blocked_on names, not on this seat, so it neither
	// alerts nor nudges until its blocker closes (measured 2026-10-17, task 005e0aad). Plain, not omitempty:
	// the runtime sends the list whenever it sends an alert at all.
	WaitingQitemIDs []string `json:"waiting_qitem_ids"`
}

// SeatSpec is one seat in a room spec.
type SeatSpec struct {
	SeatKey  string  `json:"seat_key"`
	SeatType string  `json:"seat_type"`
	Runtime  string  `json:"runtime"`
	Model    *string `json:"model,omitempty"`
	// PermissionPolicy is the ONE field where absent and present-but-empty must not collapse: an omitted key
	// means the documented unattended default, and an empty string is refused BY NAME by the runtime. A plain
	// string with omitempty cannot say both on the wire, so this is a pointer: nil omits the key, and a
	// pointer to "" sends it.
	PermissionPolicy *string `json:"permission_policy,omitempty"`
	Cwd              *string `json:"cwd,omitempty"`
	AgentDir         *string `json:"agent_dir,omitempty"`
	// PolicyDocument is the document the runtime installs as this seat's own `config.yml`: the hub's rendered
	// `runtime/omp-config.yml` for the seat, with the client's own non-policy keys added (see clientpolicy
	// and clientsync.SeatRender). nil omits the key, and a seat with no document gets NO file - the absence
	// rule the seat dir already follows for mcp.json. It rides the spec because the hub's render is fetched by
	// the client and the runtime must not derive a second copy of it.
	PolicyDocument *string           `json:"policy_document,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

// SeatLink is an edge between seats.
type SeatLink struct {
	From string `json:"from"`
	Kind string `json:"kind"`
	To   string `json:"to"`
}

// RoomSpec defines a complete room to start.
type RoomSpec struct {
	Room     string              `json:"room"`
	Name     *string             `json:"name,omitempty"`
	Cwd      *string             `json:"cwd,omitempty"`
	Seats    []SeatSpec          `json:"seats"`
	Links    []SeatLink          `json:"links,omitempty"`
	Overlays map[string][]string `json:"overlays,omitempty"`
}

// WhoamiResponse is the exact JSON returned to a seat for Rule 1 compatibility.
type WhoamiResponse struct {
	ResolvedBy string           `json:"resolved_by"`
	Identity   WhoamiIdentity   `json:"identity"`
	Peers      []WhoamiPeer     `json:"peers"`
	Edges      WhoamiEdges      `json:"edges"`
	Transcript WhoamiTranscript `json:"transcript"`
}

type WhoamiIdentity struct {
	RigName     string  `json:"rigName"`
	MemberId    string  `json:"memberId"`
	Runtime     string  `json:"runtime"`
	SessionName string  `json:"sessionName"`
	LogicalId   string  `json:"logical_id"`
	Role        string  `json:"role"`
	Model       *string `json:"model,omitempty"`
}

type WhoamiPeer struct {
	LogicalId   string `json:"logicalId"`
	SessionName string `json:"sessionName"`
	Runtime     string `json:"runtime"`
}

type WhoamiEdges struct {
	Outgoing []WhoamiEdgeTarget `json:"outgoing"`
	Incoming []WhoamiEdgeTarget `json:"incoming"`
}

type WhoamiEdgeTarget struct {
	Kind string          `json:"kind"`
	To   *WhoamiEndpoint `json:"to,omitempty"`
	From *WhoamiEndpoint `json:"from,omitempty"`
}

type WhoamiEndpoint struct {
	LogicalId   string `json:"logicalId"`
	SessionName string `json:"sessionName"`
}

type WhoamiTranscript struct {
	Enabled     bool    `json:"enabled"`
	Path        *string `json:"path,omitempty"`
	TailCommand *string `json:"tailCommand,omitempty"`
}

type IpcRequest struct {
	Action  string `json:"action"`
	Payload any    `json:"payload,omitempty"`
}

type IpcResponse struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}
