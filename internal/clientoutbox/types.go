package clientoutbox

import "time"

// FactKinds is the list of append-only observations that can be recorded offline.
var FactKinds = map[string]bool{
	"progress":           true,
	"evidence_submitted": true,
	"handover":           true,
	"context_loaded":     true,
}

// DecisionKinds is the list of intents that claim, transition or judge work.
var DecisionKinds = map[string]bool{
	"claimed":        true,
	"status_changed": true,
	"gate_verdict":   true,
	"assigned":       true,
	"human_decision": true,
}

// IsFact reports whether kind is an append-only fact.
func IsFact(kind string) bool {
	return FactKinds[kind]
}

// IsDecision reports whether kind is a sequenced decision.
func IsDecision(kind string) bool {
	return DecisionKinds[kind]
}

// Intent represents a client-side queued action to be submitted to the ledger.
type Intent struct {
	ClientEventID string         `json:"client_event_id"`
	TaskID        string         `json:"task_id"`
	SubtaskID     *string        `json:"subtask_id,omitempty"`
	Kind          string         `json:"kind"`
	BaseSeq       *int           `json:"base_seq,omitempty"`
	Payload       map[string]any `json:"payload"`
	CreatedAt     time.Time      `json:"created_at"`
}

// Answer is what the cloud answers for one intent item.
type Answer struct {
	Accepted bool   `json:"accepted,omitempty"`
	UserSeq  int64  `json:"user_seq,omitempty"`
	Seq      int    `json:"seq,omitempty"`
	Rejected bool   `json:"rejected,omitempty"`
	Reason   string `json:"reason,omitempty"`
	TaskSeq  int    `json:"task_seq,omitempty"`
}

// LedgerEvent is an event entry returned from GET /api/v2/ledger.
type LedgerEvent struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	SubtaskID *string        `json:"subtask_id"`
	Seq       int            `json:"seq"`
	UserSeq   int64          `json:"user_seq"`
	Kind      string         `json:"kind"`
	ActorKind string         `json:"actor_kind"`
	ActorID   string         `json:"actor_id"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at"`
}

// LedgerPage is the response payload from GET /api/v2/ledger.
type LedgerPage struct {
	Success bool          `json:"success"`
	Events  []LedgerEvent `json:"events"`
	Count   int           `json:"count"`
	After   int64         `json:"after"`
	Tip     int64         `json:"tip"`
}

// State holds the persistent cursor and pending outbox intents.
type State struct {
	Cursor  int64    `json:"cursor"`
	Intents []Intent `json:"intents"`
}
