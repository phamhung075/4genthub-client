package clientoutbox

import "time"

// FactKinds and DecisionKinds split the ledger's ENFORCEMENT list into its two ordering halves: the
// four kinds a client may push (agenthub_go/fastmcp/ledger/domain/entities/intent.go, `IntentKinds`) -
// facts `progress` and `handover`, decisions `status_changed` and `gate_verdict`. OrderedForDrain
// returns facts first, then decisions, then everything else: the two named groups are sorted by
// CreatedAt, the remainder keeps the store's insertion order (store.go, OrderedForDrain).
//
// They are ORDERING groups, not a filter, and they MIRROR the server rather than widen it: Enqueue
// appends whatever it is given without consulting either list, and Drain removes every row the server
// answered - accepted or rejected - after printing the refusal. So a kind named here that `IntentKinds`
// does not carry is not harmless: it orders a push the wire answers unsupported_kind and the drain then
// drops, which is why the maps hold exactly the pushable four.
//
// EIGHT NAMES ARE NOT IN THESE MAPS, on purpose. Two are kinds of the ledger's own table that no client
// writes: `context_updated`, and `evidence_submitted`, which left `IntentKinds` because evidence has one
// writer - the controller, whose 409 keeps a head_sha's account fixed - and a fact intent owes no
// comparison, so the evidence verb POSTs that route directly (clientevidence, one request, no queue) and
// a submission made offline fails there rather than waiting for a later drain. The other six are not
// kinds the table admits at all - `assigned`, `claimed`, `delivered`, `context_loaded`, `escalated` and
// `human_decision` - because assignment is the task's assignees column, a claim is the `status_changed`
// write, a delivery is a queue row, and an owner decision is a queue row.
var FactKinds = map[string]bool{
	"progress": true,
	"handover": true,
}

// DecisionKinds is the list of intents that transition or judge work, and so owe a base_seq.
var DecisionKinds = map[string]bool{
	"status_changed": true,
	"gate_verdict":   true,
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
