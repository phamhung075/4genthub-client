package clientruntime

import "encoding/json"

// The room's durable rail over the runtime socket. The daemon owns each room's queue store and is its only
// writer - the client renders rows and never opens the file, because two writers is a defect class this team
// has already paid for. Every verb below is therefore a frame the daemon answers with the row RE-READ from
// its store, so the state a caller is told is the state the next read shows.
//
// The shapes are the daemon's own (`rust/agenthub-runtime/src/queue/mod.rs`), and two rules come from Rust's
// attributes rather than from taste:
//
//   - a field Rust declared `Option` is a pointer here, so "unset" (nil, key absent, Rust reads None) and
//     "set to empty" (a pointer to "") stay different questions;
//   - `omitempty` rides exactly the fields Rust accepts absence for - an `Option`, or a field declared
//     `#[serde(default)]`. Everything else always rides the frame, because the daemon's deserializer treats
//     those as required and a shorter struct is a different request than the caller meant.

// QueueStates are the five states a row may occupy, in the daemon's own order: handed-off is intermediate
// (acceptance pending), done is terminal.
var QueueStates = []string{"pending", "in-progress", "blocked", "handed-off", "done"}

// QueueClosureReasons is the exact set - and the only set - a closure reason may come from.
var QueueClosureReasons = []string{
	"handed_off_to",
	"blocked_on",
	"denied",
	"canceled",
	"no-follow-on",
	"escalation",
	"superseded",
}

// QueueTerminalStates are the states a row rests in for good: a handoff's SOURCE rests at handed-off while the
// work continues in the successor row it created, so the source is never reopened and never closable again.
var QueueTerminalStates = []string{"done", "handed-off"}

// QueueRow is one row of a room's rail as the daemon serializes it.
type QueueRow struct {
	QitemID            string            `json:"qitem_id"`
	State              string            `json:"state"`
	Summary            string            `json:"summary"`
	Body               string            `json:"body"`
	SourceSession      string            `json:"source_session"`
	DestinationSession string            `json:"destination_session"`
	ChainOfRecord      []string          `json:"chain_of_record"`
	ReplyTo            *string           `json:"reply_to,omitempty"`
	BlockedOn          *string           `json:"blocked_on,omitempty"`
	WakeAfterSeconds   *int64            `json:"wake_after_seconds,omitempty"`
	ClosureReason      *string           `json:"closure_reason,omitempty"`
	ClosureTarget      *string           `json:"closure_target,omitempty"`
	Priority           string            `json:"priority"`
	Tags               []string          `json:"tags"`
	Tier               *string           `json:"tier,omitempty"`
	TsCreated          string            `json:"ts_created"`
	TsUpdated          string            `json:"ts_updated"`
	Transitions        []QueueTransition `json:"transitions"`
}

// QueueTransition is one append-only transition: who moved the row, when, from where to where, and why. It is
// the audit trail, and it is why a QueueRow carries its transitions - a row's note is legible nowhere else.
type QueueTransition struct {
	TransitionID   int64   `json:"transition_id"`
	QitemID        string  `json:"qitem_id"`
	Ts             string  `json:"ts"`
	State          string  `json:"state"`
	FromState      *string `json:"from_state,omitempty"`
	ActorSession   string  `json:"actor_session"`
	TransitionNote string  `json:"transition_note"`
	ClosureReason  *string `json:"closure_reason,omitempty"`
	ClosureTarget  *string `json:"closure_target,omitempty"`
}

// QueueCreateInput creates a row. The store refuses an empty SourceSession or DestinationSession; Priority is
// "normal" when unset. QitemID is retry identity: reuse it for the same create after an unknown outcome, and
// the daemon answers with the row that already exists instead of a second one.
type QueueCreateInput struct {
	SourceSession      string   `json:"source_session"`
	DestinationSession string   `json:"destination_session"`
	Summary            string   `json:"summary"`
	Body               string   `json:"body,omitempty"`
	Priority           *string  `json:"priority,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	Tier               *string  `json:"tier,omitempty"`
	ReplyTo            *string  `json:"reply_to,omitempty"`
	ChainOfRecord      []string `json:"chain_of_record,omitempty"`
	BlockedOn          *string  `json:"blocked_on,omitempty"`
	WakeAfterSeconds   *int64   `json:"wake_after_seconds,omitempty"`
	QitemID            *string  `json:"qitem_id,omitempty"`
}

// QueueListFilter narrows a list read; its zero value narrows nothing. The frame carries the filter even when
// it is the zero filter, because the daemon requires the key: an omitted filter is not the same request.
type QueueListFilter struct {
	DestinationSession *string  `json:"destination_session,omitempty"`
	SourceSession      *string  `json:"source_session,omitempty"`
	State              []string `json:"state,omitempty"`
	// ActiveOnly reads the states an owner still owes something on: pending, in-progress, blocked.
	ActiveOnly bool   `json:"active_only,omitempty"`
	Limit      *int64 `json:"limit,omitempty"`
}

// QueueUpdateInput appends a transition to a row and returns the row as the store now holds it. A
// TransitionNote with no State is a state-preserving append: the note is the point, not the move.
type QueueUpdateInput struct {
	QitemID          string  `json:"qitem_id"`
	ActorSession     string  `json:"actor_session"`
	State            *string `json:"state,omitempty"`
	TransitionNote   *string `json:"transition_note,omitempty"`
	ClosureReason    *string `json:"closure_reason,omitempty"`
	ClosureTarget    *string `json:"closure_target,omitempty"`
	BlockedOn        *string `json:"blocked_on,omitempty"`
	WakeAfterSeconds *int64  `json:"wake_after_seconds,omitempty"`
	Summary          *string `json:"summary,omitempty"`
}

// QueueCloseInput closes a row. ClosureReason is required and must come from QueueClosureReasons - the daemon
// refuses an unknown reason by name - and handed_off_to needs a ClosureTarget to hand the work to.
type QueueCloseInput struct {
	QitemID        string  `json:"qitem_id"`
	ActorSession   string  `json:"actor_session"`
	ClosureReason  string  `json:"closure_reason"`
	ClosureTarget  *string `json:"closure_target,omitempty"`
	TransitionNote string  `json:"transition_note,omitempty"`
}

// QueueCreate appends a row to the room's rail and returns it as the store now holds it.
func (c *Client) QueueCreate(room string, in QueueCreateInput) (*QueueRow, error) {
	resp, err := c.Call("QueueCreate", map[string]any{"room": room, "input": in})
	if err != nil {
		return nil, err
	}
	row := queueRowFrom(resp)
	return &row, nil
}

// QueueList reads the room's rail through a filter, newest first as the store orders it.
func (c *Client) QueueList(room string, filter QueueListFilter) ([]QueueRow, error) {
	resp, err := c.Call("QueueList", map[string]any{"room": room, "filter": filter})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(resp.Data)
	var rows []QueueRow
	_ = json.Unmarshal(raw, &rows)
	return rows, nil
}

// QueueShow reads one row of the room's rail, transitions included.
func (c *Client) QueueShow(room, qitemID string) (*QueueRow, error) {
	resp, err := c.Call("QueueShow", map[string]string{"room": room, "qitem_id": qitemID})
	if err != nil {
		return nil, err
	}
	row := queueRowFrom(resp)
	return &row, nil
}

// QueueUpdate appends a transition to a row and returns the row as the store now holds it.
func (c *Client) QueueUpdate(room string, in QueueUpdateInput) (*QueueRow, error) {
	resp, err := c.Call("QueueUpdate", map[string]any{"room": room, "input": in})
	if err != nil {
		return nil, err
	}
	row := queueRowFrom(resp)
	return &row, nil
}

// QueueClaim takes a pending row for actorSession: the row moves to in-progress and the claim is recorded as
// a transition, so who took it is legible afterwards. Claiming a row that is not pending is refused by name.
func (c *Client) QueueClaim(room, qitemID, actorSession string) (*QueueRow, error) {
	resp, err := c.Call("QueueClaim", map[string]string{
		"room":          room,
		"qitem_id":      qitemID,
		"actor_session": actorSession,
	})
	if err != nil {
		return nil, err
	}
	row := queueRowFrom(resp)
	return &row, nil
}

// QueueClose closes a row with a reason from QueueClosureReasons and returns it as the store now holds it.
func (c *Client) QueueClose(room string, in QueueCloseInput) (*QueueRow, error) {
	resp, err := c.Call("QueueClose", map[string]any{"room": room, "input": in})
	if err != nil {
		return nil, err
	}
	row := queueRowFrom(resp)
	return &row, nil
}

// ListAllRooms names every room the daemon has a directory for.
//
// The action takes no parameters, and the payload is sent as an explicit `null` rather than left out: the
// daemon's frame is adjacently tagged, so `payload` is a sibling of `action` and its own serialization of a
// parameterless variant is `"payload":null`. `IpcRequest.Payload` carries `omitempty`, which would drop the
// key instead, so the null is written here rather than left to the encoder's own opinion.
func (c *Client) ListAllRooms() ([]string, error) {
	resp, err := c.Call("ListAllRooms", json.RawMessage("null"))
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(resp.Data)
	var rooms []string
	_ = json.Unmarshal(raw, &rooms)
	return rooms, nil
}

// queueRowFrom reads the one row a row verb answers with. The daemon re-reads the row from its store before
// answering, so what lands here is what the next read of the rail shows.
func queueRowFrom(resp *IpcResponse) QueueRow {
	raw, _ := json.Marshal(resp.Data)
	var row QueueRow
	_ = json.Unmarshal(raw, &row)
	return row
}
