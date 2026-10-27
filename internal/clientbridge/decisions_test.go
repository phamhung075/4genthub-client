package clientbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// The owner-decision crossing, driven through the bridge's seams: a fake hub that keeps the hub's own
// state machine (pending -> answered -> resolved/superseded, an answer handed out until it is acked) and a
// fake rail holding one owner ask. Nothing here touches a room or a network, and nothing sleeps.

const askQitem = "qitem-20261027084120-a14eb5d43248"

type fakeHubDecision struct {
	id, qitem, state string
	answers          map[string]string
	acked            bool
}

type fakeHub struct {
	calls     []string
	bodies    map[string][]byte
	decisions map[string]*fakeHubDecision
	failNext  map[string]step
}

func (h *fakeHub) call(method, path string, body []byte) (int, []byte, error) {
	route := method + " " + path
	h.calls = append(h.calls, route)
	h.bodies[route] = body
	if scripted, ok := h.failNext[route]; ok {
		delete(h.failNext, route)
		return scripted.code, scripted.answer, scripted.err
	}
	switch {
	case route == "POST /api/v2/decisions":
		var ask struct {
			QitemID        string `json:"qitem_id"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(body, &ask)
		if ask.IdempotencyKey != "agenthub:decision:"+ask.QitemID {
			return 400, []byte(`{"detail":"idempotency_key must be agenthub:decision:<qitem_id>"}`), nil
		}
		for _, d := range h.decisions {
			if d.qitem == ask.QitemID {
				return 200, []byte(`{"success":true,"decision":{"id":"` + d.id + `"},"created":false}`), nil
			}
		}
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", len(h.decisions)+1)
		h.decisions[id] = &fakeHubDecision{id: id, qitem: ask.QitemID, state: "pending"}
		return 200, []byte(`{"success":true,"decision":{"id":"` + id + `"},"created":true}`), nil
	case strings.HasPrefix(route, "GET /api/v2/decisions/answers?room="):
		var rows []map[string]any
		for _, d := range h.decisions {
			if d.state == "answered" && !d.acked {
				rows = append(rows, map[string]any{"decision_id": d.id, "qitem_id": d.qitem, "room": "room-a", "answers": d.answers})
			}
		}
		out, _ := json.Marshal(map[string]any{"success": true, "answers": rows, "cursor": ""})
		return 200, out, nil
	case strings.HasPrefix(route, "POST /api/v2/decisions/"):
		rest := strings.TrimPrefix(route, "POST /api/v2/decisions/")
		id, leg, _ := strings.Cut(rest, "/")
		d, ok := h.decisions[id]
		if !ok {
			return 404, []byte(`{"detail":"no decision"}`), nil
		}
		var payload map[string]string
		_ = json.Unmarshal(body, &payload)
		if leg == "ack" {
			if d.state != "answered" || d.acked {
				return 409, []byte(`{"detail":"not awaiting an ack"}`), nil
			}
			d.acked = true
			if payload["outcome"] == "applied" {
				d.state = "resolved"
			}
			return 200, []byte(`{"success":true}`), nil
		}
		if d.state != "pending" && d.state != "answered" {
			return 409, []byte(`{"detail":"state-` + d.state + `"}`), nil
		}
		d.state = payload["state"]
		return 200, []byte(`{"success":true}`), nil
	}
	return 500, []byte(`{"detail":"unscripted"}`), nil
}

// ownerAnswers is the dashboard click: the hub records the answer and the row becomes `answered`.
func (h *fakeHub) ownerAnswers(question, option string) {
	for _, d := range h.decisions {
		d.answers = map[string]string{question: option}
		d.state = "answered"
	}
}

type decisionFixture struct {
	bridge *Bridge
	hub    *fakeHub
	clock  *fakeClock
	row    clientruntime.QueueRow
	closes []clientruntime.QueueCloseInput
	notes  []string
}

func newDecisionFixture(t *testing.T) *decisionFixture {
	t.Helper()
	f := &decisionFixture{
		hub: &fakeHub{bodies: map[string][]byte{}, decisions: map[string]*fakeHubDecision{}, failNext: map[string]step{}},
		row: clientruntime.QueueRow{QitemID: askQitem, State: "pending", Summary: "Phase 1 exit rule",
			Body: "facts: x\nrule: y\ndecision: z\nnext: w", SourceSession: "agenthub--room-a--lead",
			DestinationSession: "human@room-a", Tags: []string{"owner-decision"}},
		clock: &fakeClock{at: time.Date(2026, 10, 27, 20, 0, 0, 0, time.UTC)},
	}
	b := NewBridge("pc-1", t.TempDir(), time.Second, nil, nil, nil, t.TempDir(), "", func(line string) { f.notes = append(f.notes, line) })
	b.clock = f.clock.now
	b.Hub = f.hub.call
	b.Rooms = func() ([]string, error) { return []string{"room-a"}, nil }
	b.Show = func(room, qitem string) (*clientruntime.QueueRow, error) {
		if qitem != f.row.QitemID {
			return nil, errors.New("queue row not found")
		}
		row := f.row
		return &row, nil
	}
	b.Close = func(room string, in clientruntime.QueueCloseInput) error {
		if queueStateTerminal(f.row.State) {
			return fmt.Errorf("queue row '%s' is already in terminal state '%s'", in.QitemID, f.row.State)
		}
		f.closes = append(f.closes, in)
		f.row.State = "handed-off"
		return nil
	}
	f.bridge = b
	questions, err := clientdecisions.ParseQuestions([]byte(`[{"id":"exit_rule","question":"Which Phase 1 exit rule?",` +
		`"options":[{"id":"short","label":"Short gate","recommended":true},{"id":"full","label":"Full gate"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := clientdecisions.Write(b.Root, clientdecisions.Record{QitemID: askQitem, Room: "room-a", Questions: questions}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *decisionFixture) records(t *testing.T) []clientdecisions.Record {
	t.Helper()
	records, err := clientdecisions.List(f.bridge.Root, "room-a")
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func (f *decisionFixture) count(route string) int {
	n := 0
	for _, call := range f.hub.calls {
		if strings.HasPrefix(call, route) {
			n++
		}
	}
	return n
}

// The whole round trip: the ask is mirrored once, the answer is pulled no more than once a minute, it
// reaches the asking seat as ONE handoff carrying the chosen label, the hub's row is resolved, and a room
// with nothing open costs no request at all.
func TestTheOwnersDashboardAnswerReachesTheAskingSeatOnce(t *testing.T) {
	f := newDecisionFixture(t)

	f.bridge.syncDecisions()
	if f.count("POST /api/v2/decisions") != 1 {
		t.Fatalf("calls = %v, want the ask mirrored", f.hub.calls)
	}
	var ask map[string]any
	_ = json.Unmarshal(f.hub.bodies["POST /api/v2/decisions"], &ask)
	if ask["room"] != "room-a" || ask["author_seat_key"] != "agenthub--room-a--lead" || ask["destination"] != "human@room-a" {
		t.Fatalf("ask = %v", ask)
	}
	if f.records(t)[0].DecisionID == "" {
		t.Fatal("the hub's decision id was not recorded")
	}

	f.clock.plusSeconds(20)
	f.bridge.syncDecisions()
	f.clock.plusSeconds(20)
	f.bridge.syncDecisions()
	if got := f.count("GET /api/v2/decisions/answers"); got != 1 {
		t.Fatalf("pulled %d times in 40s, want 1: the pull is bounded to once a minute", got)
	}
	if f.count("POST /api/v2/decisions") != 1 {
		t.Fatal("the ask was mirrored again")
	}

	f.hub.ownerAnswers("exit_rule", "full")
	f.clock.plusSeconds(60)
	f.bridge.syncDecisions()
	if len(f.closes) != 1 {
		t.Fatalf("closes = %d, want the ask closed once", len(f.closes))
	}
	closed := f.closes[0]
	if closed.ActorSession != "human@room-a" || closed.ClosureReason != "handed_off_to" ||
		closed.ClosureTarget == nil || *closed.ClosureTarget != "agenthub--room-a--lead" {
		t.Fatalf("close = %+v, want handed_off_to the lead as the room's human", closed)
	}
	if !strings.Contains(closed.TransitionNote, "Which Phase 1 exit rule? -> Full gate [full]") {
		t.Fatalf("note = %q, want the chosen label", closed.TransitionNote)
	}
	for _, d := range f.hub.decisions {
		if d.state != "resolved" {
			t.Fatalf("hub row is %s, want resolved by the applied ack", d.state)
		}
	}
	if len(f.records(t)) != 0 {
		t.Fatal("the record outlived its resolved decision")
	}

	calls := len(f.hub.calls)
	for i := 0; i < 5; i++ {
		f.clock.plusSeconds(60)
		f.bridge.syncDecisions()
	}
	if len(f.hub.calls) != calls || len(f.closes) != 1 {
		t.Fatalf("a room with no open ask made %d more hub calls and %d closes", len(f.hub.calls)-calls, len(f.closes)-1)
	}
}

// A bridge that applied the answer but could not ack it acks on the next cycle and never closes again.
func TestAnAppliedAnswerWhoseAckFailedIsAckedNotReapplied(t *testing.T) {
	f := newDecisionFixture(t)
	f.bridge.syncDecisions()
	f.hub.ownerAnswers("exit_rule", "short")
	id := f.records(t)[0].DecisionID
	f.hub.failNext["POST /api/v2/decisions/"+id+"/ack"] = step{err: errNoAnswer}
	f.clock.plusSeconds(60)
	f.bridge.syncDecisions()
	if len(f.closes) != 1 || !f.records(t)[0].Applied {
		t.Fatalf("closes = %d, record = %+v; want applied and the ack owed", len(f.closes), f.records(t))
	}

	f.clock.plusSeconds(20)
	f.bridge.syncDecisions()
	if len(f.closes) != 1 {
		t.Fatalf("the answer was applied %d times", len(f.closes))
	}
	if f.hub.decisions[id].state != "resolved" || len(f.records(t)) != 0 {
		t.Fatalf("hub state %s, records %d; want resolved and forgotten", f.hub.decisions[id].state, len(f.records(t)))
	}
}

// The owner answered on the rail instead: the panel is told the ask is superseded, and nothing is pulled.
func TestAnAskClosedOnTheRailIsSupersededOnTheHub(t *testing.T) {
	f := newDecisionFixture(t)
	f.bridge.syncDecisions()
	id := f.records(t)[0].DecisionID
	f.row.State = "handed-off"
	f.clock.plusSeconds(60)
	f.bridge.syncDecisions()
	if f.hub.decisions[id].state != "superseded" || f.count("GET ") != 0 || len(f.records(t)) != 0 {
		t.Fatalf("hub %s, calls %v, records %d", f.hub.decisions[id].state, f.hub.calls, len(f.records(t)))
	}
}

// A hub that will never take the ask (the room is not on it) is not asked again every cycle.
func TestARefusedAskIsNotRetried(t *testing.T) {
	f := newDecisionFixture(t)
	f.hub.failNext["POST /api/v2/decisions"] = step{code: 404, answer: []byte(`{"detail":"room \"room-a\" not found"}`)}
	f.bridge.syncDecisions()
	f.clock.plusSeconds(60)
	f.bridge.syncDecisions()
	if got := f.count("POST /api/v2/decisions"); got != 1 {
		t.Fatalf("asked %d times, want 1", got)
	}
	if !strings.Contains(f.records(t)[0].Refused, "404") {
		t.Fatalf("record = %+v, want the refusal kept", f.records(t)[0])
	}
}
