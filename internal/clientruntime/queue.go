package clientruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

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

// QueueTags are the tags a NEW row may carry, the daemon's own list (queue/mod.rs ALLOWED_TAGS). The four
// low-value tags - status, ack, checkpoint, report - were retired on 2026-10-17: the daemon refuses a create
// that carries one, naming it and this list. A row written before the retirement keeps the tag it carries;
// only a create is refused, because a row is a record and this is a rule about what may be added.
var QueueTags = []string{"prod-break", "gate", "ruling", "owner-decision", "finding", "guide", "docs", "defect", "skill-check", "landing", "assignment", "handoff", "board", "reseat"}

// QueueStates are the five states a row may occupy, in the daemon's own order: handed-off is intermediate
// (acceptance pending), done is terminal.
var QueueStates = []string{"pending", "in-progress", "blocked", "handed-off", "done"}

// QueueClosureReasons is the exact set - and the only set - a closure reason may come from. `gate` is the
// reviewer's verdict close: its transition note carries PASS/FAIL and the commands that were run.
var QueueClosureReasons = []string{
	"handed_off_to",
	"blocked_on",
	"denied",
	"canceled",
	"no-follow-on",
	"escalation",
	"superseded",
	"gate",
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
	// RequesterSession is the seat asking; set, the daemon rate-limits an identical repeat inside 5 minutes.
	RequesterSession *string `json:"requester_session,omitempty"`
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
	if err := validateCreateRow(in); err != nil {
		return nil, err
	}
	resp, err := c.Call("QueueCreate", map[string]any{"room": room, "input": in})
	if err != nil {
		return nil, err
	}
	row, err := queueRowFrom("QueueCreate", resp)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// RowSchemaRefused is the rail's create-time shape rule (architect ruling 3/8): the body is four fixed lines
// and a create carries at least one tag. The message is the daemon's own sentence - the daemon refuses the
// same input in `rust/agenthub-runtime/src/queue/mod.rs`, and ONE wording serves both seams.
type RowSchemaRefused struct{ Message string }

func (e *RowSchemaRefused) Error() string { return e.Message }

// BodyFieldCap is one body line's value cap: the key and the chars its value may take.
type BodyFieldCap struct {
	Key string
	Cap int
}

// bodyFields is the ruled grammar: keys in order and the per-field value caps. The prefixes and newlines
// cost 32 chars, so the caps (280+110+100+78 = 568) sum with them to the store's 600-char body cap exactly.
var bodyFields = []BodyFieldCap{{"facts", 280}, {"rule", 110}, {"decision", 100}, {"next", 78}}

// BodyFieldCaps is bodyFields for a caller that RENDERS the rule rather than enforcing it - the queue help
// prints these numbers, and a help that carries its own copy drifts from the refusal. The slice is a copy.
func BodyFieldCaps() []BodyFieldCap {
	return append([]BodyFieldCap(nil), bodyFields...)
}

// validateCreateRow is the client half of the create-time rule. It runs BEFORE the round trip so a seat is
// told in one line and writes nothing; the daemon checks the same rule as the authority over its store.
func validateCreateRow(in QueueCreateInput) error {
	if len(in.Tags) == 0 {
		return &RowSchemaRefused{Message: "tag rule: a create carries at least one allowed tag"}
	}
	if err := validateBodySchema(in.Body); err != nil {
		return &RowSchemaRefused{Message: err.Error()}
	}
	return nil
}

// validateBodySchema is the four-line grammar, character for character the Rust function's twin.
func validateBodySchema(body string) error {
	var lines []string
	if body != "" {
		lines = strings.Split(body, "\n")
	}
	seen := make(map[string]bool, len(bodyFields))
	for index, line := range lines {
		key, _, found := strings.Cut(line, ": ")
		position := -1
		for i, field := range bodyFields {
			if field.Key == key {
				position = i
				break
			}
		}
		if !found || position < 0 {
			return unexpectedBodyLine(index, line)
		}
		if seen[key] {
			return fmt.Errorf("body schema: field '%s' appears twice", key)
		}
		if position != index {
			return unexpectedBodyLine(index, line)
		}
		seen[key] = true
	}
	if len(lines) < len(bodyFields) {
		missing := make([]string, 0, len(bodyFields)-len(lines))
		for _, field := range bodyFields[len(lines):] {
			missing = append(missing, fmt.Sprintf("body schema: field '%s' is missing", field.Key))
		}
		return errors.New(strings.Join(missing, "\n"))
	}
	var over []string
	for index, field := range bodyFields {
		value := strings.TrimPrefix(lines[index], field.Key+": ")
		if value == "" {
			return fmt.Errorf("body schema: field '%s' is empty", field.Key)
		}
		if chars := utf8.RuneCountInString(value); chars > field.Cap {
			over = append(over, fmt.Sprintf("body field '%s' cap: %d chars exceeds the %d-char cap", field.Key, chars, field.Cap))
		}
	}
	if len(over) > 0 {
		return errors.New(strings.Join(over, "\n"))
	}
	return nil
}

func unexpectedBodyLine(index int, line string) error {
	return fmt.Errorf("body schema: unexpected line %d ('%s'); the body is facts, rule, decision, next, in that order", index+1, line)
}

// QueueList reads the room's rail through a filter, newest first as the store orders it.
func (c *Client) QueueList(room string, filter QueueListFilter) ([]QueueRow, error) {
	resp, err := c.Call("QueueList", map[string]any{"room": room, "filter": filter})
	if err != nil {
		return nil, err
	}
	var rows []QueueRow
	if err := decodeAnswer("QueueList", resp.Data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// QueueShow reads one row of the room's rail, transitions included.
func (c *Client) QueueShow(room, qitemID string) (*QueueRow, error) {
	resp, err := c.Call("QueueShow", map[string]string{"room": room, "qitem_id": qitemID})
	if err != nil {
		return nil, err
	}
	row, err := queueRowFrom("QueueShow", resp)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// QueueUpdate appends a transition to a row and returns the row as the store now holds it.
func (c *Client) QueueUpdate(room string, in QueueUpdateInput) (*QueueRow, error) {
	resp, err := c.Call("QueueUpdate", map[string]any{"room": room, "input": in})
	if err != nil {
		return nil, err
	}
	row, err := queueRowFrom("QueueUpdate", resp)
	if err != nil {
		return nil, err
	}
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
	row, err := queueRowFrom("QueueClaim", resp)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// QueueClose closes a row with a reason from QueueClosureReasons and returns it as the store now holds it.
func (c *Client) QueueClose(room string, in QueueCloseInput) (*QueueRow, error) {
	resp, err := c.Call("QueueClose", map[string]any{"room": room, "input": in})
	if err != nil {
		return nil, err
	}
	row, err := queueRowFrom("QueueClose", resp)
	if err != nil {
		return nil, err
	}
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
	var rooms []string
	if err := decodeAnswer("ListAllRooms", resp.Data, &rooms); err != nil {
		return nil, err
	}
	return rooms, nil
}

// queueRowFrom reads the one row a row verb answers with. The daemon re-reads the row from its store before
// answering, so what lands here is what the next read of the rail shows - and a document that is not a row
// is REFUSED rather than flattened into a zero row. A zero QueueRow is the most dangerous answer this file
// can produce: every field is empty, so a refused claim would render as a row with no id, no state and no
// owner, which is exactly the "a write that never happened" the daemon's own refusal exists to prevent.
func queueRowFrom(action string, resp *IpcResponse) (QueueRow, error) {
	var row QueueRow
	if err := decodeAnswer(action, resp.Data, &row); err != nil {
		return QueueRow{}, err
	}
	return row, nil
}
