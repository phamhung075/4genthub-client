package clientruntime

import "time"

// SeatStatus is the live status of a seat reported by agenthub-runtime.
type SeatStatus struct {
	Room           string     `json:"room"`
	Seat           string     `json:"seat"`
	SessionName    string     `json:"session_name"`
	Runtime        string     `json:"runtime"`
	Model          *string    `json:"model,omitempty"`
	Pid            *uint32    `json:"pid,omitempty"`
	State          string     `json:"state"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	TokenEstimate  int        `json:"token_estimate"`
	LastActive     *time.Time `json:"last_active,omitempty"`
	TranscriptPath *string    `json:"transcript_path,omitempty"`
}

// SeatSpec is one seat in a room spec.
type SeatSpec struct {
	SeatKey          string            `json:"seat_key"`
	SeatType         string            `json:"seat_type"`
	Runtime          string            `json:"runtime"`
	Model            *string           `json:"model,omitempty"`
	PermissionPolicy string            `json:"permission_policy,omitempty"`
	Cwd              *string           `json:"cwd,omitempty"`
	AgentDir         *string           `json:"agent_dir,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
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
