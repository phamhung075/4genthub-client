package clientbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// D3's producer: the bridge reads the OpenRig rows addressed to a rig's human and posts ONE
// notification frame per row. These cases drive the sweep through the package's own seams - the local
// tools are fixtures behind Runner, the notification transport is a script of answers - so nothing
// here touches a rig, a seat or a network, and nothing sleeps.

// fixtureHumanQueue is one human-addressed row in the shape `rig queue list --json` prints.
const fixtureHumanQueue = `[{"qitemId":"qitem-20261010120000-aaaa1111","state":"pending",` +
	`"sourceSession":"room-a-lead@room-a","destinationSession":"human@room-a",` +
	`"summary":"the cloud refused the report text as secret-bearing",` +
	`"body":"Body text is the fallback only when the summary is empty."}]`

func decodeFrame(t *testing.T, raw []byte) (map[string]any, map[string]any) {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("the posted frame is not JSON: %v (%s)", err, string(raw))
	}
	data, ok := frame["data"].(map[string]any)
	if !ok {
		t.Fatalf("the posted frame carries no data object: %s", string(raw))
	}
	return frame, data
}

// TestHumanDestinationAddressesTheRig pins the address form the daemon's store really uses, because
// the whole read depends on it: human@<rig name> (measured against a live store, one entry per rig).
func TestHumanDestinationAddressesTheRig(t *testing.T) {
	if got := HumanDestination("room-a"); got != "human@room-a" {
		t.Fatalf("HumanDestination(room-a) = %q, want human@room-a", got)
	}
}

// TestListingParsersAcceptBothShapes: the rig tool prints a bare list, and its JSON may arrive wrapped
// as {"items": [...]}; a reader that understood only one of them would silently see no rows.
func TestListingParsersAcceptBothShapes(t *testing.T) {
	for name, output := range map[string]string{
		"list":  `[{"qitemId":"a"}]`,
		"items": `{"items":[{"qitemId":"a"}]}`,
	} {
		rows, err := parseItems(output, "rig queue list")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(rows) != 1 {
			t.Fatalf("%s: parsed %d rows, want 1", name, len(rows))
		}
	}
	if _, err := parseItems(`{"rows":[]}`, "rig queue list"); err == nil {
		t.Fatal("a shape carrying no items must be an error, not an empty listing")
	}
}

// TestNotifyPostsOneFrameForAHumanRow is the acceptance's core: a human-addressed row becomes exactly
// one frame with the contract the consumer reads - entity_type/event_type notification, entity_id the
// STABLE row id (the dashboard's dedupe key), and data.primary carrying the message the human sees.
func TestNotifyPostsOneFrameForAHumanRow(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = fixtureHumanQueue

	f.bridge.Cycle(context.Background())

	if len(f.frames) != 1 {
		t.Fatalf("posted %d frames, want exactly 1", len(f.frames))
	}
	frame, data := decodeFrame(t, f.frames[0])
	if frame["event_type"] != "notification" || frame["entity_type"] != "notification" {
		t.Fatalf("entity/event type = %v/%v, want notification/notification", frame["entity_type"], frame["event_type"])
	}
	if frame["entity_id"] != "qitem-20261010120000-aaaa1111" {
		t.Fatalf("entity_id = %v, want the row id", frame["entity_id"])
	}
	if _, sent := frame["user_id"]; sent {
		t.Fatal("the frame carries a user_id: the route takes the target from the token, and a body user_id is ignored")
	}
	if _, sent := frame["metadata"]; sent {
		t.Fatal("the frame carries metadata: the route passes it through, and the store path reads metadata.user_id as another target")
	}
	if data["message"] != "the cloud refused the report text as secret-bearing" {
		t.Fatalf("message = %v, want the row's summary", data["message"])
	}
	if data["from"] != "room-a-lead@room-a" {
		t.Fatalf("from = %v, want the source seat key", data["from"])
	}
	if data["room"] != "room-a" || data["seat"] != "room-a-lead" {
		t.Fatalf("room/seat = %v/%v, want room-a/room-a-lead", data["room"], data["seat"])
	}
	if data["kind"] != "notice" {
		t.Fatalf("kind = %v, want notice", data["kind"])
	}
	if got := ReadNotifiedState(f.statePath)["room-a/qitem-20261010120000-aaaa1111"]; got == "" {
		t.Fatal("the sent id was not recorded, so the next cycle would post it again")
	}
}

// TestNotifyRunsEvenWhenTheCycleSkipsItsReport: the cycle returns early when nothing changed and the
// heartbeat is not due - which is most cycles - and a row addressed to the human is news on its own, so
// the sweep must not sit behind that decision. The plausible failure is a notification that only goes
// out when a report does, i.e. one that arrives on the heartbeat rather than when it happened.
func TestNotifyRunsEvenWhenTheCycleSkipsItsReport(t *testing.T) {
	f := newFixture(t)
	f.cycle()
	if len(f.bodies) != 1 {
		t.Fatalf("the first cycle sent %d reports, want 1", len(f.bodies))
	}

	// A row arrives and NOTHING else changes: the same seats, and not yet the heartbeat.
	f.queueJSON = `[{"qitemId":"qitem-20261010120000-late0001","state":"pending",` +
		`"destinationSession":"human@room-a","summary":"a row that arrived while nothing else changed"}]`
	f.clock.plusSeconds(HeartbeatSecs - 1)

	f.cycle()

	if len(f.bodies) != 1 {
		t.Fatalf("reports sent = %d, want still 1: this case needs the cycle to skip its report", len(f.bodies))
	}
	if len(f.frames) != 1 {
		t.Fatalf("notification frames = %d, want 1: the sweep sits behind the report's early return", len(f.frames))
	}
	if _, data := decodeFrame(t, f.frames[0]); data["message"] != "a row that arrived while nothing else changed" {
		t.Fatalf("message = %v, want the late row's summary", data["message"])
	}
}

// TestNotifyDoesNotPostTheSameRowTwice: the store grows one row per post, so a sweep that re-posts a row
// it already sent is the difference between a bounded record and an inbox that fills up by itself.
func TestNotifyDoesNotPostTheSameRowTwice(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = fixtureHumanQueue

	f.bridge.Cycle(context.Background())
	f.clock.plusSeconds(60)
	f.bridge.Cycle(context.Background())

	if len(f.frames) != 1 {
		t.Fatalf("posted %d frames over two cycles, want 1: the second cycle re-sent a row it had sent", len(f.frames))
	}
}

// TestNotifyStateSurvivesARestart: the record is the file, not the process. A restarted bridge must not
// re-notify the human about rows the previous process already sent.
func TestNotifyStateSurvivesARestart(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = fixtureHumanQueue
	f.bridge.Cycle(context.Background())

	var restarted [][]byte
	next := NewBridge("pc-1", time.Second, nil,
		func(body []byte) (int, []byte, error) {
			restarted = append(restarted, append([]byte(nil), body...))
			return 200, nil, nil
		}, nil, f.dir, f.statePath, nil)
	next.Runner = f.bridge.Runner
	next.Rooms, next.Queue = f.bridge.Rooms, f.bridge.Queue
	next.clock = f.clock.now
	next.notifyHumans()

	if len(restarted) != 0 {
		t.Fatalf("a restarted bridge posted %d frames, want 0: the sent-id record did not survive", len(restarted))
	}
}

// TestNotifyRetriesARefusedPost is the difference between "posted" and "accepted": an id is recorded
// only after the cloud accepted the frame, so a refusal is retried rather than silently swallowed.
func TestNotifyRetriesARefusedPost(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = fixtureHumanQueue
	f.notifySteps = []step{{code: 500, answer: []byte(`{"detail":"no"}`)}}

	f.bridge.Cycle(context.Background())
	if len(f.frames) != 1 {
		t.Fatalf("posted %d frames, want 1 attempt", len(f.frames))
	}
	if got := ReadNotifiedState(f.statePath)["room-a/qitem-20261010120000-aaaa1111"]; got != "" {
		t.Fatal("a refused frame was recorded as delivered, so it would never be retried")
	}

	f.clock.plusSeconds(60)
	f.bridge.Cycle(context.Background())
	if len(f.frames) != 2 {
		t.Fatalf("posted %d frames, want the refused row retried on the next cycle", len(f.frames))
	}
	if got := ReadNotifiedState(f.statePath)["room-a/qitem-20261010120000-aaaa1111"]; got == "" {
		t.Fatal("the accepted frame was not recorded")
	}
}

// TestNotifySkipsARowWithNoText: the dashboard DROPS a frame whose message is empty, so posting one
// would be a silent no-op - and recording the id would mean the row is never announced once it does
// carry text.
func TestNotifySkipsARowWithNoText(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = `[{"qitemId":"qitem-empty","state":"pending","destinationSession":"human@room-a",` +
		`"summary":"  ","body":"\n  \n"}]`

	f.bridge.Cycle(context.Background())

	if len(f.frames) != 0 {
		t.Fatalf("posted %d frames for a row with no text, want 0", len(f.frames))
	}
	if _, recorded := ReadNotifiedState(f.statePath)["room-a/qitem-empty"]; recorded {
		t.Fatal("a row with no text was recorded as sent, so it could never be announced later")
	}
}

// TestNotifyIgnoresARowAddressedToASeat: the read is destination-filtered, and this is the second line
// of defence - a change to the filter must not be able to turn another seat's rows into the human's
// notifications.
func TestNotifyIgnoresARowAddressedToASeat(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = `[{"qitemId":"qitem-seat","state":"pending","destinationSession":"room-a-reviewer@room-a",` +
		`"summary":"this one is not the human's"}]`

	f.bridge.Cycle(context.Background())

	if len(f.frames) != 0 {
		t.Fatalf("posted %d frames for a row addressed to a seat, want 0", len(f.frames))
	}
}

// TestNotifyDoesNothingWithoutASender: a bridge built to dump a payload (once --print, the parity
// fixtures) has nowhere to post, and must say so by posting nothing rather than by failing the cycle.
func TestNotifyDoesNothingWithoutASender(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = fixtureHumanQueue
	f.bridge.Notify = nil

	delay := f.bridge.Cycle(context.Background())

	if len(f.frames) != 0 {
		t.Fatalf("posted %d frames with no notification sender, want 0", len(f.frames))
	}
	if delay != f.bridge.Interval {
		t.Fatalf("delay = %v, want the normal interval %v", delay, f.bridge.Interval)
	}
}

// TestNotifyNotesARoomListItCouldNotRead: the sweep's room list is a runtime read, so the failure it must
// survive is that read's. It cannot name a room to walk, so it posts nothing and says so; the cycle still
// returns on the normal interval, because a notification the sweep could not assemble never fails the report
// beside it.
func TestNotifyNotesARoomListItCouldNotRead(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = fixtureHumanQueue
	f.bridge.Rooms = func() ([]string, error) { return nil, errors.New("the runtime answered HTTP 500") }

	delay := f.bridge.Cycle(context.Background())

	if len(f.frames) != 0 {
		t.Fatalf("posted %d frames with no room list to walk, want 0", len(f.frames))
	}
	if !strings.Contains(f.logged(), "runtime unavailable: the runtime answered HTTP 500") {
		t.Errorf("the note is missing the read's own sentence:\n%s", f.logged())
	}
	if delay != f.bridge.Interval {
		t.Fatalf("delay = %v, want the normal interval %v", delay, f.bridge.Interval)
	}
}

// TestPruneNotifiedKeepsTheNewest bounds the state file: the oldest ids go first, and the newest stay,
// because only the newest can still be posted again.
func TestPruneNotifiedKeepsTheNewest(t *testing.T) {
	sent := map[string]string{
		"room-a/old":    "2026-10-01T00:00:00Z",
		"room-a/middle": "2026-10-05T00:00:00Z",
		"room-a/new":    "2026-10-10T00:00:00Z",
	}
	pruneNotified(sent, 2)
	if len(sent) != 2 {
		t.Fatalf("kept %d entries, want 2", len(sent))
	}
	if _, kept := sent["room-a/old"]; kept {
		t.Fatal("the oldest entry was kept and a newer one dropped")
	}
}

// TestNotifyRedactsAKnownSecretBeforeItLeavesTheMachine: this path is an EGRESS, like the report's, and
// it was the one path that copied a row's text into the cloud raw. The row here quotes a value the
// bridge holds, and what must go out is the marker naming the VARIABLE, never the value: the row lives
// in the local daemon's queue, the frame is stored in the cloud's notification store, and the receiving
// route does not scan either.
func TestNotifyRedactsAKnownSecretBeforeItLeavesTheMachine(t *testing.T) {
	const value = "demo-secret-value-0123456789"
	f := newFixture(t)
	f.bridge.Known = map[string]string{"DEMO_TOKEN": value}
	f.queueJSON = `[{"qitemId":"qitem-secret","state":"pending","destinationSession":"human@room-a",` +
		`"sourceSession":"room-a-lead@room-a",` +
		`"summary":"the token is ` + value + ` - rotate it"}]`

	f.bridge.Cycle(context.Background())

	if len(f.frames) != 1 {
		t.Fatalf("posted %d frames, want exactly 1", len(f.frames))
	}
	_, data := decodeFrame(t, f.frames[0])
	message, _ := data["message"].(string)
	if strings.Contains(message, value) {
		t.Fatalf("the notification carried the secret value itself: %q", message)
	}
	if !strings.Contains(message, "[REDACTED:DEMO_TOKEN]") {
		t.Errorf("the marker naming the variable is missing, so nothing says what was removed: %q", message)
	}
	if !strings.Contains(message, "rotate it") {
		t.Errorf("the redaction took the rest of the message with it: %q", message)
	}
}

// TestNotifyDropsARowTheCloudWouldRefuseForever: a 4xx naming the REQUEST will be answered the same way
// on every cycle, so posting it again is not a retry - it is the same refusal once per cycle, forever.
// The row is recorded as handled and the note carries the status, so the drop is visible, not silent.
func TestNotifyDropsARowTheCloudWouldRefuseForever(t *testing.T) {
	f := newFixture(t)
	f.queueJSON = fixtureHumanQueue
	f.notifySteps = []step{{code: 422, answer: []byte(`{"detail":"the payload is not a notification"}`)}}

	f.bridge.Cycle(context.Background())

	if len(f.frames) != 1 {
		t.Fatalf("posted %d frames, want 1 attempt", len(f.frames))
	}
	key := "room-a/qitem-20261010120000-aaaa1111"
	if got := ReadNotifiedState(f.statePath)[key]; got == "" {
		t.Fatal("a terminal refusal was not recorded, so the same 4xx would be posted on every cycle forever")
	}
	if !strings.Contains(f.logged(), "HTTP 422") || !strings.Contains(f.logged(), "not retried") {
		t.Errorf("the record does not say why the row was dropped:\n%s", f.logged())
	}

	f.clock.plusSeconds(60)
	f.bridge.Cycle(context.Background())
	if len(f.frames) != 1 {
		t.Fatalf("the terminally refused row was posted again: %d frames over two cycles, want 1", len(f.frames))
	}
}

// TestNotifyTerminalTellsRetryableFromFinal pins the classification, because it is what decides whether
// a human's message is thrown away: only an answer that names THIS request is final.
func TestNotifyTerminalTellsRetryableFromFinal(t *testing.T) {
	for _, status := range []int{400, 404, 409, 410, 413, 415, 422} {
		if !notifyTerminal(status) {
			t.Errorf("HTTP %d names the request itself, so it must be final", status)
		}
	}
	for _, status := range []int{100, 200, 204, 301, 401, 403, 408, 429, 500, 502, 503} {
		if notifyTerminal(status) {
			t.Errorf("HTTP %d can be answered differently later, so it must stay retryable", status)
		}
	}
}
