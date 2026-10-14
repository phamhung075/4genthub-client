package clientbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// The run loop is the half of the bridge that exists only while it is running: it decides WHEN a report
// goes out, what a failure costs, what the cloud's refusal of the text does to the next report, and
// which exit code a one-shot run earns. The Python specified all of it in tests/test_bridge.py; these
// are those behaviours, ported onto this package's seams.
//
// Nothing here touches a rig, a seat or a network, and nothing sleeps: the local tools are fixtures
// behind Runner, the transport is a script of answers and failures behind Send (or an in-process
// http.RoundTripper for the sender itself), and the wait the loop would take comes back from Cycle as a
// VALUE, so a test asserts it instead of living through it.

// fixtureNodes is one the rig orchestrator node in the shape `rig ps --json --nodes` prints. It carries a
// latestError on purpose: the seat's detail must be NON-EMPTY, or the 422 strip would be asserted
// against a field that could not have differed in the first place.
const fixtureNodes = `[{"rigName":"room-a","logicalId":"room-a.seat-a","runtime":"omp",` +
	`"sessionStatus":"running","agentActivity":{"state":"working"},"latestError":"boom in seat-a"}]`

// step is one scripted exchange: the status the transport answers and the body it answers with, OR the
// error of a transport that never answered. A failure is an ERROR and not a status, because the status
// is the cloud's answer and there was none - the shape the Python's sender raised in when its request
// timed out or the cloud was unreachable.
type step struct {
	code   int
	answer []byte
	err    error
}

// errNoAnswer is a transport that never answered, in the shape net/http hands one back: the URL it was
// trying, then the reason. It carries the URL and NOT the credential - the token lives in a header - so
// the loop can print it and the operator can see which cloud was unreachable.
var errNoAnswer = errors.New(`Post "http://bridge.invalid/api/v2/openrig/seat-status": dial tcp: connect: connection refused`)

type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) plusSeconds(seconds float64) {
	c.at = c.at.Add(time.Duration(seconds * float64(time.Second)))
}

type fixture struct {
	bridge  *Bridge
	clock   *fakeClock
	notes   []string
	rigJSON string
	bodies  [][]byte
	steps   []step
	// queueJSON is what the notification sweep's queue read answers. Empty means the rig fixtures
	// answer it instead, which parses to a rig with no rows - the sweep then does nothing.
	queueJSON string
	// frames are the notification frames the sweep posted, and notifySteps scripts their answers the
	// same way steps scripts the report's.
	frames      [][]byte
	notifySteps []step
	dir         string
	statePath   string
	// beats records every heartbeat seam call the cycles under test made, so a test can assert the
	// bridge beat without writing into the machine's own log directory.
	beats []string
}

// newFixture builds the bridge every test below drives: fixture local tools, a scripted transport, a
// clock the test advances, and the notes captured instead of printed.
func newFixture(t *testing.T, steps ...step) *fixture {
	t.Helper()
	f := &fixture{rigJSON: fixtureNodes, steps: steps}
	// Every cycle of the bridge now beats. The seam is replaced here, for every test that builds its
	// bridge through this fixture, so no test writes into the machine's own log directory; the beat is
	// recorded so a test can assert it happened.
	previousBeat := clientservices.WriteHeartbeat
	clientservices.WriteHeartbeat = func(safeguard, rig string) error {
		f.beats = append(f.beats, safeguard+"/"+rig)
		return nil
	}
	t.Cleanup(func() { clientservices.WriteHeartbeat = previousBeat })
	dir := t.TempDir()
	f.dir, f.statePath = dir, filepath.Join(dir, "sync.json")
	f.bridge = NewBridge("pc-1", time.Duration(DefaultInterval*float64(time.Second)), f.send, f.notify,
		secretscan.KnownValuesFromEnv(os.Environ()), dir, f.statePath,
		func(line string) { f.notes = append(f.notes, line) })
	f.bridge.Runner = func(_ context.Context, argv ...string) (string, error) {
		if argv[0] == "herdr" {
			// The machine the Python's fixtures described had no herdr either; an absent tool is an
			// empty source, not a failed report.
			return "", fmt.Errorf("herdr: not on PATH")
		}
		return "", fmt.Errorf("%s: not a tool this fixture runs", argv[0])
	}
	f.bridge.Nodes = func() ([]any, error) { return ParseRigNodes(f.rigJSON) }
	f.bridge.Links = func(string) ([]rigEdge, error) { return nil, nil }
	f.clock = &fakeClock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	f.bridge.clock = f.clock.now

	// THE RUNTIME THE NOTIFICATION SWEEP TALKS TO: it hosts the room the nodes describe, and its rail holds
	// the human rows the test scripts, so the sweep and the report talk about one room.
	f.bridge.Rooms = func() ([]string, error) { return []string{"room-a"}, nil }
	f.bridge.Queue = func(string) ([]any, error) {
		if f.queueJSON == "" {
			return nil, nil
		}
		return parseItems(f.queueJSON, "rig queue list")
	}
	return f
}

// notify is the fake notification transport: the frame is recorded as it was handed over and the next
// scripted answer is returned, defaulting to accepted.
func (f *fixture) notify(body []byte) (int, []byte, error) {
	f.frames = append(f.frames, append([]byte(nil), body...))
	if len(f.notifySteps) == 0 {
		return 200, nil, nil
	}
	next := f.notifySteps[0]
	f.notifySteps = f.notifySteps[1:]
	return next.code, next.answer, next.err
}

// send is the fake transport: it records the body as it was handed over and answers the next scripted
// step, defaulting to an accepted empty answer so a test only scripts what it is about.
func (f *fixture) send(body []byte) (int, []byte, error) {
	f.bodies = append(f.bodies, append([]byte(nil), body...))
	if len(f.steps) == 0 {
		return 200, nil, nil
	}
	next := f.steps[0]
	f.steps = f.steps[1:]
	return next.code, next.answer, next.err
}

// reported is the report the transport received at index i, decoded.
func (f *fixture) reported(t *testing.T, i int) Payload {
	t.Helper()
	if i >= len(f.bodies) {
		t.Fatalf("no report %d was sent: %d went out", i, len(f.bodies))
	}
	var payload Payload
	if err := json.Unmarshal(f.bodies[i], &payload); err != nil {
		t.Fatalf("report %d is not a report: %v", i, err)
	}
	return payload
}

func (f *fixture) cycle() time.Duration { return f.bridge.Cycle(context.Background()) }

func (f *fixture) logged() string { return strings.Join(f.notes, "\n") }

// A cycle sends when the report has CHANGED and otherwise only when the heartbeat is due. The Python
// decided this from the same two fields, and a loop that resent an unchanged payload every 20 seconds
// would be the cloud's traffic rather than this machine's news.
func TestCycleSendsOnChangeAndOnHeartbeat(t *testing.T) {
	f := newFixture(t)
	if got := f.cycle(); got != f.bridge.Interval {
		t.Fatalf("the first cycle waited %s, want the interval %s", got, f.bridge.Interval)
	}
	if len(f.bodies) != 1 {
		t.Fatalf("the first cycle sent %d reports, want 1", len(f.bodies))
	}

	f.clock.plusSeconds(HeartbeatSecs - 1)
	f.cycle()
	if len(f.bodies) != 1 {
		t.Errorf("an unchanged report was sent %d times before the heartbeat, want 1", len(f.bodies))
	}

	f.rigJSON = strings.Replace(fixtureNodes, "boom in seat-a", "moved on", 1)
	f.clock.plusSeconds(1)
	f.cycle()
	if len(f.bodies) != 2 {
		t.Errorf("a changed report was not sent: %d reports, want 2", len(f.bodies))
	}

	f.clock.plusSeconds(HeartbeatSecs + 1)
	f.cycle()
	if len(f.bodies) != 3 {
		t.Errorf("the heartbeat did not send an unchanged report: %d reports, want 3", len(f.bodies))
	}
}

// A cycle beats ONCE, as its first act, even when it finds nothing to send and returns early: a bridge
// that stopped between beats and a machine with no news must not look the same.
func TestCycleWritesOneHeartbeatPerCycle(t *testing.T) {
	f := newFixture(t)
	f.cycle()
	f.cycle() // the unchanged payload is not resent: this one returns early and must still have beaten

	if len(f.beats) != 2 {
		t.Fatalf("heartbeats = %d (%v), want one per cycle (2)", len(f.beats), f.beats)
	}
	for _, beat := range f.beats {
		if beat != "bridge/" {
			t.Errorf("heartbeat = %q, want bridge/ (the bridge is one per machine, so no rig)", beat)
		}
	}
}

// A 422 means the cloud refused the TEXT as secret-bearing. The answer is to drop detail from the NEXT
// report rather than to scrub harder, and the degradation ends as soon as an answer is accepted - so
// there are three distinct observables here: the flag, the stripped body, and detail coming back.
func TestCycle422StripsDetailThenRecovers(t *testing.T) {
	f := newFixture(t, step{code: 422}, step{code: 200}, step{code: 200})
	if got := f.cycle(); got != f.bridge.Interval {
		t.Errorf("the 422 cycle waited %s, want the interval %s: a refusal is not a backoff", got, f.bridge.Interval)
	}
	if !f.bridge.stripDetail {
		t.Error("a 422 did not set the detail strip, so the next report would repeat what the cloud refused")
	}
	first := f.reported(t, 0)
	if len(first.Seats) == 0 || first.Seats[0].Detail == "" {
		t.Fatalf("the first report carried no detail to drop: %+v", first.Seats)
	}

	f.cycle()
	stripped := f.reported(t, 1)
	if len(stripped.Seats) == 0 {
		t.Fatalf("the cycle after a 422 reported no seats: %+v", stripped.Seats)
	}
	for _, seat := range stripped.Seats {
		if seat.Detail != "" || !seat.Redacted {
			t.Errorf("the cycle after a 422 carried %s/%s detail=%q redacted=%v, want it stripped",
				seat.Room, seat.Seat, seat.Detail, seat.Redacted)
		}
	}
	if f.bridge.stripDetail {
		t.Error("an accepted answer did not end the detail strip")
	}

	f.cycle()
	recovered := f.reported(t, 2)
	if len(recovered.Seats) == 0 || recovered.Seats[0].Detail == "" {
		t.Errorf("detail did not come back after an accepted answer: %+v", recovered.Seats)
	}
}

// Every failure class costs the same first two waits: 400, 401, 500 and a transport that never
// answered all back the loop off by one interval and then two, and an accepted cycle resets it. The
// codes and the waits are the assertion; the sentences are not.
func TestCycleFailureClassesBackOffAndReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		step step
	}{
		{"a transport that never answered", step{err: errNoAnswer}},
		{"status_400", step{code: 400}},
		{"status_401", step{code: 401}},
		{"status_500", step{code: 500}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.step, tc.step, step{code: 200})
			if got := f.cycle(); got != f.bridge.Interval {
				t.Errorf("the first failure waited %s, want %s", got, f.bridge.Interval)
			}
			if got := f.cycle(); got != 2*f.bridge.Interval {
				t.Errorf("the second failure waited %s, want %s", got, 2*f.bridge.Interval)
			}
			if got := f.cycle(); got != f.bridge.Interval {
				t.Errorf("the accepted cycle waited %s, want the interval %s", got, f.bridge.Interval)
			}
			if f.bridge.backoff != 0 {
				t.Errorf("the accepted cycle left a backoff of %s, want none", f.bridge.backoff)
			}
		})
	}
}

// The Python's mixed chain (tests/test_bridge.py:566): 401, then 500, then a request that never answers,
// all in ONE loop, doubling 20, 40 and 80 and resetting on the accepted cycle that follows - the only
// place the transport failure's WAIT is pinned inside a chain rather than as a class of its own. The last
// cycle is the unchanged payload: it is not resent, and the loop still waits one interval. 4 requests.
func TestTheFailureChainDoublesThroughATransportFailureAndResets(t *testing.T) {
	f := newFixture(t, step{code: 401}, step{code: 500}, step{err: errNoAnswer}, step{code: 200})
	want := []time.Duration{
		20 * time.Second, 40 * time.Second, 80 * time.Second, 20 * time.Second, 20 * time.Second,
	}
	waits := make([]time.Duration, 0, len(want))
	for i, expected := range want {
		got := f.cycle()
		waits = append(waits, got)
		if got != expected {
			t.Fatalf("cycle %d waited %s, want %s", i+1, got, expected)
		}
	}
	if len(f.bodies) != 4 {
		t.Errorf("%d reports went out, want 4: the 401, the 500, the request that never answered and the accepted one", len(f.bodies))
	}
	if f.bridge.backoff != 0 {
		t.Errorf("the accepted cycle left a backoff of %s, want none", f.bridge.backoff)
	}
	for i, w := range waits {
		if w < f.bridge.Interval {
			t.Errorf("wait %d is %s, below the one interval %s: the loop must never retry faster", i+1, w, f.bridge.Interval)
		}
	}
}

// A transport that never answered earns a sentence about ITS failure, not about an answer. The Python
// named the reason (tests/test_bridge.py:587 asserts the words its urllib timeout prints) and never
// printed `server answered HTTP 0` for a request that never arrived; the reason now travels out of the
// sender as an error and the loop prints it. The 500 row is the contrast that keeps the two apart: a
// real answer still names its status, so the no-answer path cannot be reached with one.
func TestCycleNamesTheTransportFailureReason(t *testing.T) {
	f := newFixture(t, step{err: errNoAnswer}, step{code: 500})
	f.cycle()
	if got := f.logged(); !strings.Contains(got, "cycle failed: ") || !strings.Contains(got, "connection refused") {
		t.Errorf("the note does not name the transport reason: %q", got)
	}
	if got := f.logged(); strings.Contains(got, "answered HTTP") {
		t.Errorf("a transport that never answered was reported as an answer: %q", got)
	}

	f.cycle()
	if got := f.logged(); !strings.Contains(got, "server answered HTTP 500") {
		t.Errorf("a real answer is not named with its status: %q", got)
	}
}

// The backoff doubles to MaxBackoffSecs and STOPS there, and an accepted cycle clears it: the cap is
// what keeps a cloud outage from becoming an hourly report, and the reset is what keeps it from being
// permanent.
func TestBackoffDoublesToTheCapAndResets(t *testing.T) {
	f := newFixture(t,
		step{err: errNoAnswer}, step{err: errNoAnswer}, step{err: errNoAnswer},
		step{err: errNoAnswer}, step{err: errNoAnswer}, step{err: errNoAnswer}, step{code: 200})
	want := []time.Duration{
		20 * time.Second, 40 * time.Second, 80 * time.Second,
		120 * time.Second, 120 * time.Second, 120 * time.Second,
	}
	for i, expected := range want {
		if got := f.cycle(); got != expected {
			t.Fatalf("failure %d waited %s, want %s", i+1, got, expected)
		}
	}
	if got := f.cycle(); got != f.bridge.Interval {
		t.Errorf("the accepted cycle waited %s, want %s", got, f.bridge.Interval)
	}
	if f.bridge.backoff != 0 {
		t.Errorf("an accepted cycle left a backoff of %s, want none", f.bridge.backoff)
	}
	if cap := time.Duration(MaxBackoffSecs * float64(time.Second)); cap != 120*time.Second {
		t.Errorf("MaxBackoffSecs is %vs, want 120s", MaxBackoffSecs)
	}
}

// `bridge once` is a CHECK as well as a report: 0 only when the cycle was accepted and nothing is being
// degraded, and the remote failure otherwise. A refused text (422) counts as degraded even though the
// cloud answered, because the report that went out was not the report this machine meant to send.
func TestRunOnceExitCodes(t *testing.T) {
	cases := []struct {
		name string
		step step
		want int
	}{
		{"accepted", step{code: 200}, clientcmd.ExitOK},
		{"the text was refused", step{code: 422}, clientcmd.ExitRemote},
		{"the credential was refused", step{code: 401}, clientcmd.ExitRemote},
		{"the cloud failed", step{code: 500}, clientcmd.ExitRemote},
		{"the cloud did not answer", step{err: errNoAnswer}, clientcmd.ExitRemote},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.step)
			if got := f.bridge.Run(context.Background(), true); got != tc.want {
				t.Errorf("run once against %s exited %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// The loop is stopped by its context, not by a signal: an already-done context still runs ONE cycle -
// the report is why the process was started - and then returns without waiting out the interval.
func TestRunReturnsWhenTheContextIsDone(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan int, 1)
	go func() { done <- f.bridge.Run(ctx, false) }()
	select {
	case code := <-done:
		if code != clientcmd.ExitOK {
			t.Errorf("a stopped loop exited %d, want %d", code, clientcmd.ExitOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not return when its context was done")
	}
	if len(f.bodies) != 1 {
		t.Errorf("the stopped loop sent %d reports, want the one cycle it was started for", len(f.bodies))
	}
}

// The answer is the ONLY channel that knows the cloud's current expectation, so an in_sync verdict is
// recorded - on disk, because that is what a restart reads - and every other verdict is NAMED. A drift
// verdict does NOT advance the record: an expectation that moved is not a confirmation.
func TestApplyVerdictsRecordsInSyncOnDiskAndNamesTheRest(t *testing.T) {
	inSync := []byte(`{"verdicts":[{"room":"room-a","seat":"seat-a","expected_hash":"cloud456","sync":"in_sync"}]}`)
	f := newFixture(t, step{code: 200, answer: inSync})
	f.cycle()

	if got := f.bridge.state["pc-1/room-a/seat-a"]; got != "cloud456" {
		t.Errorf("the record reads %q, want cloud456", got)
	}
	if got := ReadSyncState(f.bridge.StatePath)["pc-1/room-a/seat-a"]; got != "cloud456" {
		t.Errorf("the record ON DISK reads %q, want cloud456: a restart reads this file", got)
	}

	drift := []byte(`{"verdicts":[{"room":"room-a","seat":"seat-a","expected_hash":"h2","sync":"drift"}]}`)
	g := newFixture(t, step{code: 200, answer: drift})
	seed := `{"machine_id":"pc-1","seats":{"pc-1/room-a/seat-a":"cloud456"}}`
	if err := os.WriteFile(g.bridge.StatePath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	g.bridge.state = ReadSyncState(g.bridge.StatePath)
	g.cycle()

	if !strings.Contains(g.logged(), "room-a/seat-a") {
		t.Errorf("a drift verdict was not named: %q", g.logged())
	}
	if !strings.Contains(g.logged(), "h2") {
		t.Errorf("the named drift does not carry the expectation: %q", g.logged())
	}
	if got := g.bridge.state["pc-1/room-a/seat-a"]; got != "cloud456" {
		t.Errorf("a drift verdict advanced the record to %q; it must not", got)
	}
	if got := ReadSyncState(g.bridge.StatePath)["pc-1/room-a/seat-a"]; got != "cloud456" {
		t.Errorf("a drift verdict rewrote the record on disk to %q; it must not", got)
	}
}

// An answer that carries no verdicts is not evidence that anything moved: absent, not JSON, or JSON
// without a verdicts list all leave the record exactly as it was, because the record is what a seat is
// compared against and a cleared one would report every seat as new.
func TestAnAnswerWithoutVerdictsLeavesTheRecordIntact(t *testing.T) {
	for _, answer := range [][]byte{nil, []byte("not json"), []byte(`{"success":true}`)} {
		f := newFixture(t, step{code: 200, answer: answer})
		seed := `{"machine_id":"pc-1","seats":{"pc-1/room-a/seat-a":"cloud456"}}`
		if err := os.WriteFile(f.bridge.StatePath, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
		f.bridge.state = ReadSyncState(f.bridge.StatePath)
		f.cycle()

		if got := f.bridge.state["pc-1/room-a/seat-a"]; got != "cloud456" {
			t.Errorf("answer %q left the record at %q, want cloud456", answer, got)
		}
		if got := ReadSyncState(f.bridge.StatePath)["pc-1/room-a/seat-a"]; got != "cloud456" {
			t.Errorf("answer %q rewrote the record on disk to %q", answer, got)
		}
	}
}

// A bridge whose own record is unreadable still reports: it reads as EMPTY rather than inventing a
// hash, and every seat it has no record for reads unknown - never unchanged by accident.
func TestAnUnreadableRecordReadsAsEmptyRatherThanInventingAHash(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.bridge.StatePath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadSyncState(f.bridge.StatePath); len(got) != 0 {
		t.Fatalf("an unreadable record read as %v, want empty", got)
	}
	f.bridge.state = ReadSyncState(f.bridge.StatePath)

	payload := f.bridge.BuildPayload()
	record := f.bridge.LocalRecord(payload.Seats)
	if len(record) == 0 {
		t.Fatalf("the fixture reported no seats to compare: %+v", payload.Seats)
	}
	for where, note := range record {
		if note.SinceLastInSync != "unknown" {
			t.Errorf("%s reads %q with no record, want unknown", where, note.SinceLastInSync)
		}
	}
}

// A node whose room or seat name fails this client's pattern is NOT reported, and the count is: the
// report is an allow-list, so an the rig orchestrator value this client does not understand must not travel as a
// field the cloud then has to defend against. The Python asserted both halves - only the good seat
// survives, and the caller says how many were skipped - and this is that case.
func TestBuildSeatsSkipsInvalidNamesAndCountsThem(t *testing.T) {
	fourBad := `[
		{"rigName":"bad room","logicalId":"x"},
		{"rigName":"eng","logicalId":"../etc"},
		{"rigName":"eng","logicalId":"dev.coder"},
		{"rigName":"ok","logicalId":7}
	]`
	threeBad := `[
		{"rigName":"eng","logicalId":"dev.coder"},
		{"rigName":"bad room","logicalId":"dev.x"},
		{"rigName":"eng","logicalId":"dev.../etc"}
	]`

	for _, tc := range []struct {
		name     string
		nodes    string
		wantNote string
	}{
		// The Python asserted the sentence on the three-node case (:271) and only the surviving seat on
		// the four-node case (:224). Both sentences are pinned here: the exact text is what the room
		// ruled the Go must keep, and asserting a Contains over a digit and the word invalid would pass
		// on almost any message that carried them, including one about something else.
		{"four bad nodes", fourBad, "skipped 3 seat(s) with invalid names"},
		{"three bad nodes", threeBad, "skipped 2 seat(s) with invalid names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, step{code: 200})
			f.rigJSON = tc.nodes
			f.cycle()

			report := f.reported(t, 0)
			rooms := make([]string, 0, len(report.Seats))
			for _, seat := range report.Seats {
				rooms = append(rooms, seat.Room+"/"+seat.Seat)
			}
			if len(rooms) != 1 || rooms[0] != "eng/coder" {
				t.Errorf("reported seats = %v, want only eng/coder: a name failing the pattern is not reported", rooms)
			}
			if !strings.Contains(f.logged(), tc.wantNote) {
				t.Errorf("the skip note is not %q: %q", tc.wantNote, f.logged())
			}
			// The invalid-name path and the duplicate path are two different sentences; neither may
			// borrow the other's words.
			if strings.Contains(f.logged(), "rename one") {
				t.Errorf("the invalid-name path printed the duplicate sentence: %q", f.logged())
			}
		})
	}
}

// The record is what makes a restart useful: a bridge that comes back still knows what the cloud last
// confirmed per seat, and a seat that has moved since then is named LOCALLY - here with the cloud
// unreachable, so nothing but the record on disk can be producing the words. The three words are this
// machine's own (changed/unchanged/unknown), never the cloud's in_sync.
func TestTheRecordSurvivesARestartAndNamesAChangedSeat(t *testing.T) {
	first := newFixture(t)
	pinDir := filepath.Join(first.bridge.PinsDir, "room-a", "seat-a")
	if err := os.MkdirAll(pinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// the seat was confirmed at cloud456, and the pin on disk has since moved on to run999
	if err := os.WriteFile(filepath.Join(pinDir, "pinned.json"), []byte(`{"hash":"run999"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := `{"machine_id":"pc-1","seats":{"pc-1/room-a/seat-a":"cloud456"}}`
	if err := os.WriteFile(first.bridge.StatePath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	// a restarted bridge reads the record from the file, exactly as a new process would, and the cloud
	// does not answer at all
	restarted := newFixture(t, step{err: errNoAnswer})
	restarted.bridge.PinsDir = first.bridge.PinsDir
	restarted.bridge.StatePath = first.bridge.StatePath
	restarted.bridge.state = ReadSyncState(first.bridge.StatePath)
	restarted.cycle()

	if got := restarted.bridge.state["pc-1/room-a/seat-a"]; got != "cloud456" {
		t.Errorf("the restarted bridge read %q, want cloud456 from its own record", got)
	}
	logged := restarted.logged()
	for _, want := range []string{"room-a/seat-a", "run999", "cloud456"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the changed seat's note does not carry %s: %q", want, logged)
		}
	}
	payload := restarted.bridge.BuildPayload()
	note := restarted.bridge.LocalRecord(payload.Seats)["room-a/seat-a"]
	if note.Running != "run999" || note.LastInSync != "cloud456" || note.SinceLastInSync != "changed" {
		t.Errorf("the local record reads %+v, want running=run999 last_in_sync=cloud456 since=changed", note)
	}
}

// A machine without herdr still reports its seats: an absent or broken local tool is an EMPTY source
// rather than a failed report, and its line is printed ONCE per distinct message rather than every
// twenty seconds - the dedupe is what keeps a 20-second loop from repeating one sentence forever.
func TestAbsentToolsAreEmptySourcesAndLoggedOnce(t *testing.T) {
	f := newFixture(t)
	f.cycle()
	f.cycle() // the unchanged payload is not resent, but the note sink is asked again all the same

	payload := f.reported(t, 0)
	if len(payload.Seats) != 1 || payload.Seats[0].Seat != "seat-a" {
		t.Errorf("the seats are not the rig fixture's: %+v", payload.Seats)
	}
	if len(payload.Agents) != 0 {
		t.Errorf("agents = %+v, want none out of an absent herdr", payload.Agents)
	}
	unavailable := 0
	for _, line := range f.notes {
		if strings.Contains(line, "herdr unavailable") {
			unavailable++
		}
	}
	if unavailable != 1 {
		t.Errorf("the absent tool was logged %d times, want once: %q", unavailable, f.notes)
	}
}

// roundTripFunc is an http.RoundTripper that answers in-process, so the sender's own code path - the
// URL, the bearer header, the status handling - is exercised with no network and no credential leaving
// this machine.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func withTransport(t *testing.T, round roundTripFunc) {
	t.Helper()
	restore := statusClient
	statusClient = &http.Client{Transport: round}
	t.Cleanup(func() { statusClient = restore })
}

// The push path itself: the sender reads the ONE credential from the environment, posts to the status
// path, and carries the token as a bearer credential. Replacing the transport is what makes this
// assertable offline, and it is the shape the Python's local HTTP server had.
func TestSenderPostsTheWireContractWithTheUserToken(t *testing.T) {
	const token = "user-token-abc"
	t.Setenv("AGENTHUB_URL", "http://bridge.invalid")
	t.Setenv("AGENTHUB_TOKEN", token)

	var got *http.Request
	var gotBody []byte
	withTransport(t, func(request *http.Request) (*http.Response, error) {
		got = request
		gotBody, _ = io.ReadAll(request.Body)
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"verdicts":[]}`)),
			Header:     http.Header{},
		}, nil
	})

	send, err := senderFromEnv()
	if err != nil {
		t.Fatalf("senderFromEnv: %v", err)
	}
	status, answer, sendErr := send([]byte(`{"machine_id":"pc-1"}`))
	if sendErr != nil {
		t.Fatalf("a transport that answered came back as an error: %v", sendErr)
	}
	if status != 200 || !strings.Contains(string(answer), "verdicts") {
		t.Errorf("the answer came back as %d %q, want the transport's own", status, answer)
	}
	if got == nil {
		t.Fatal("the sender issued no request")
	}
	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	if want := "http://bridge.invalid" + statusPath; got.URL.String() != want {
		t.Errorf("posted to %q, want %q", got.URL.String(), want)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer "+token {
		t.Errorf("Authorization = %q, want the user's token as a bearer credential", auth)
	}
	if contentType := got.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if string(gotBody) != `{"machine_id":"pc-1"}` {
		t.Errorf("body = %q, want the payload handed to the sender", gotBody)
	}
}

// A bridge with no credential cannot report at all, so it is a USAGE error that NAMES the variable
// rather than a loop that starts quietly and never delivers. Both halves are behaviour: the refusal
// names what is missing, and the verb turns it into exit 2.
func TestSenderWithoutTheTokenIsAUsageError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		token     string
		wantNamed string
	}{
		{"neither variable", "", "", "AGENTHUB_TOKEN"},
		{"no token", "http://bridge.invalid", "", "AGENTHUB_TOKEN"},
		{"no url", "", "user-token", "AGENTHUB_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTHUB_URL", tc.url)
			t.Setenv("AGENTHUB_TOKEN", tc.token)
			_, err := senderFromEnv()
			if err == nil {
				t.Fatal("a sender was built with no credential")
			}
			if !strings.Contains(err.Error(), tc.wantNamed) {
				t.Errorf("the refusal %q does not name %s", err, tc.wantNamed)
			}
		})
	}

	t.Setenv("AGENTHUB_URL", "http://bridge.invalid")
	t.Setenv("AGENTHUB_TOKEN", "")
	var out, errOut bytes.Buffer
	if code := (bridgeCommand{}).Run(context.Background(), []string{"once"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("bridge once without a credential exited %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "AGENTHUB_TOKEN") {
		t.Errorf("the verb's refusal does not name the variable it needs: %q", errOut.String())
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("a refused bridge once printed to stdout: %q", out.String())
	}
}

// The credential rows have one point between them: the token must be UNABLE to appear - in the report,
// in a note, or in the sender's own error text. The seat's detail CARRIES the token here, so a leak
// would be a redaction failure rather than a hypothetical; and the failures are still NAMED, because a
// bridge that says nothing is as broken as one that says the token.
func TestTheTokenNeverAppearsInANoteOrInTheError(t *testing.T) {
	const token = "s3cr3t-bridge-token"
	t.Setenv("AGENTHUB_URL", "http://bridge.invalid")
	t.Setenv("AGENTHUB_TOKEN", token)

	f := newFixture(t, step{code: 401}, step{code: 500}, step{err: errNoAnswer})
	f.rigJSON = strings.Replace(fixtureNodes, "boom in seat-a", "auth failed token="+token, 1)
	for i := 0; i < 3; i++ {
		f.cycle()
	}

	if len(f.bodies) != 3 {
		t.Fatalf("%d reports went out, want 3", len(f.bodies))
	}
	for i, body := range f.bodies {
		if strings.Contains(string(body), token) {
			t.Errorf("report %d carries the credential: %s", i, body)
		}
	}
	if !strings.Contains(f.logged(), "HTTP 401") {
		t.Errorf("the notes do not name the refused credential: %q", f.logged())
	}
	if !strings.Contains(f.logged(), "HTTP 500") {
		t.Errorf("the notes do not name the cloud failure: %q", f.logged())
	}
	if !strings.Contains(f.logged(), "AGENTHUB_TOKEN") {
		t.Errorf("a 401 must name the VARIABLE the operator has to check: %q", f.logged())
	}
	if strings.Contains(f.logged(), token) {
		t.Errorf("a note carries the credential: %q", f.logged())
	}

	// And the sender's OWN error text cannot carry it either: a transport that fails with the URL in
	// its message - the shape net/http uses - must not put the credential in front of the operator,
	// neither in what the sender returns nor in the note the loop writes. The failure here is the
	// spec's own: a request that never answers (tests/test_bridge.py:581), which asserted the timeout's
	// own words reach the output. Go's words for that event are the client timeout, and it is THE
	// REASON reaching the operator that is asserted, not a particular spelling of it.
	calls := 0
	withTransport(t, func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("Post %q: context deadline exceeded (Client.Timeout exceeded while awaiting headers)", request.URL.String())
	})
	send, err := senderFromEnv()
	if err != nil {
		t.Fatalf("senderFromEnv: %v", err)
	}
	status, answer, sendErr := send([]byte(`{"machine_id":"pc-1"}`))
	if sendErr == nil {
		t.Errorf("a transport that never answered came back as %d %q with no error, want the failure", status, answer)
	}
	if !strings.Contains(sendErr.Error(), "Client.Timeout exceeded") {
		t.Errorf("the sender's error does not name the reason: %q", sendErr)
	}
	if strings.Contains(sendErr.Error(), token) {
		t.Errorf("the sender's error text carries the credential: %q", sendErr)
	}

	calls = 0 // counted again below, for the loop's own request only
	g := newFixture(t)
	g.rigJSON = f.rigJSON
	g.bridge.Send = send
	g.cycle()
	if calls != 1 {
		t.Errorf("the loop made %d requests, want 1", calls)
	}
	if !strings.Contains(g.logged(), "Client.Timeout exceeded") {
		t.Errorf("the note does not name the reason the transport failed: %q", g.logged())
	}
	if strings.Contains(g.logged(), "answered HTTP") {
		t.Errorf("a transport that never answered was reported as an answer: %q", g.logged())
	}
	if strings.Contains(g.logged(), token) {
		t.Errorf("the transport failure put the credential in a note: %q", g.logged())
	}
	if g.bridge.backoff == 0 {
		t.Error("a transport failure cost no backoff, so the loop would retry at full speed")
	}
}
