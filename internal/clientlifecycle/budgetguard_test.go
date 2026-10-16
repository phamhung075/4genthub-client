// The budget guard's DECISION is what these tests pin, one pass at a time and with no rig, no socket and no
// clock: a seat over its USD ceiling is recorded, stopped and named on the rail exactly once; a seat over the
// call ceiling is parked only after the breach held for a second pass; limits that are off measure nothing; a
// record that could not be written leaves the seat running and is retried; and a stop the runtime refused still
// leaves a park, because the record is the park.
package clientlifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

type budgetFixture struct {
	guard       *BudgetGuard
	limits      roombudget.Limits
	usage       map[string]roombudget.Usage
	seats       []string
	refuse      map[string]bool
	recordFails bool
	parks       []string
	records     map[string]roombudget.Parked
	rows        []clientruntime.QueueCreateInput
	logs        []string
	beats       int
}

// newBudgetFixture wires a guard to the test's own room: the seats it measures, their usage, the parks the
// runtime accepts (or refuses, by seat), and the writes it makes.
func newBudgetFixture(t *testing.T) *budgetFixture {
	t.Helper()
	f := &budgetFixture{
		usage:   map[string]roombudget.Usage{},
		seats:   []string{"dev", "fe"},
		refuse:  map[string]bool{},
		records: map[string]roombudget.Parked{},
		limits:  roombudget.Limits{Enabled: true, USDPerSeat24h: 25, CallsPerSeatMin: 300},
	}
	guard := &BudgetGuard{Rig: "room-a", Lead: "lead", over: map[string]int{}}
	guard.Now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	guard.Seats = func(string) []string { return f.seats }
	guard.Limits = func(string) (roombudget.Limits, error) { return f.limits, nil }
	guard.Usage = func(_, seat string) (roombudget.Usage, bool) { return f.usage[seat], true }
	guard.Park = func(_, seat string) error {
		if f.refuse[seat] {
			return &clientruntime.DaemonRefused{Reason: "cannot stop '" + seat + "': it is not running"}
		}
		f.parks = append(f.parks, seat)
		// A parked seat leaves the room's running list, which is what StopSeat does and why a second pass
		// cannot park it twice.
		kept := f.seats[:0]
		for _, running := range f.seats {
			if running != seat {
				kept = append(kept, running)
			}
		}
		f.seats = kept
		return nil
	}
	guard.Record = func(_, seat string, parked roombudget.Parked) error {
		if f.recordFails {
			return errors.New("the state root is not writable")
		}
		f.records[seat] = parked
		return nil
	}
	guard.Row = func(_ string, row clientruntime.QueueCreateInput) error {
		f.rows = append(f.rows, row)
		return nil
	}
	guard.Log = func(text string) { f.logs = append(f.logs, text) }
	guard.SeatDir = func(_, seat string) string {
		return "/home/daihu/.config/4genthub/rooms/room-a/seats/" + seat
	}
	guard.Parked = func(_, seat string) (roombudget.Parked, bool) {
		parked, ok := f.records[seat]
		return parked, ok
	}
	f.guard = guard

	previous := clientservices.WriteHeartbeat
	clientservices.WriteHeartbeat = func(string, string) error { f.beats++; return nil }
	t.Cleanup(func() { clientservices.WriteHeartbeat = previous })
	return f
}

// TestASeatOverItsUSDBudgetIsParkedOnceAndNamedOnce is acceptance 1 of the budget subtask: the seat is stopped,
// and exactly ONE rail row carries it and the number that stopped it.
func TestASeatOverItsUSDBudgetIsParkedOnceAndNamedOnce(t *testing.T) {
	f := newBudgetFixture(t)
	f.usage["dev"] = roombudget.Usage{Cost: 31.4, Calls: 12}
	f.usage["fe"] = roombudget.Usage{Cost: 3, Calls: 40}

	f.guard.Step(context.Background())
	if len(f.parks) != 1 || f.parks[0] != "dev" {
		t.Fatalf("parks = %v, want exactly dev: fe is under both ceilings", f.parks)
	}
	if len(f.rows) != 1 {
		t.Fatalf("rows = %d, want exactly one: a park names itself once", len(f.rows))
	}
	row := f.rows[0]
	if row.SourceSession != "budget@room-a" || row.DestinationSession != "agenthub--room-a--lead" {
		t.Errorf("row is from %q to %q, want the safeguard's own name and the lead's session",
			row.SourceSession, row.DestinationSession)
	}
	if !strings.Contains(row.Summary, "dev") || !strings.Contains(row.Summary, "31.40") {
		t.Errorf("summary = %q, want the seat and the number", row.Summary)
	}
	if len(row.Summary) > 120 || len(row.Body) > 600 {
		t.Errorf("row is %d/%d chars, over the rail's own caps (120/600)", len(row.Summary), len(row.Body))
	}
	// The row must not send the reader to a reseat as the way back: the record is what refuses the seat work, so
	// removing it is the way out, and the row names the path it lives at.
	if !strings.Contains(row.Body, "Remove the record") || !strings.Contains(row.Body, "seats/dev/parked.json") {
		t.Errorf("body = %q, want the record named as the way back, at its path", row.Body)
	}
	parked, ok := f.records["dev"]
	if !ok {
		t.Fatal("the park must be recorded in the seat's own directory")
	}
	if parked.Seat != "dev" || parked.Cost != 31.4 || !strings.Contains(parked.Reason, "25.00") {
		t.Errorf("record = %+v, want the seat, the spend and the ceiling it crossed", parked)
	}
	if f.beats != 1 {
		t.Errorf("heartbeats = %d, want one per pass", f.beats)
	}

	// The next pass sees the seat gone from the running list: nothing new is parked, recorded or filed.
	f.guard.Step(context.Background())
	if len(f.parks) != 1 || len(f.rows) != 1 {
		t.Errorf("a second pass parked again: parks=%v rows=%d", f.parks, len(f.rows))
	}
}

// TestARateBreachIsHeldForASecondPass: the call ceiling is read over a minute, so one pass over it can be a
// healthy seat in a burst. The USD ceiling gets no such patience, and the difference is the point.
func TestARateBreachIsHeldForASecondPass(t *testing.T) {
	f := newBudgetFixture(t)
	f.usage["dev"] = roombudget.Usage{Cost: 1, Calls: 412}

	f.guard.Step(context.Background())
	if len(f.parks) != 0 {
		t.Fatalf("parks = %v, want none on the first pass: a rate breach must hold", f.parks)
	}
	f.guard.Step(context.Background())
	if len(f.parks) != 1 || f.parks[0] != "dev" {
		t.Fatalf("parks = %v, want dev on the second pass", f.parks)
	}
	if len(f.rows) != 1 || !strings.Contains(f.rows[0].Summary, "412") {
		t.Fatalf("rows = %+v, want one row naming 412 calls", f.rows)
	}
}

// TestLimitsThatAreOffMeasureNothing: the shipped state. The guard runs, beats, and stops nobody, whatever the
// file's numbers still say.
func TestLimitsThatAreOffMeasureNothing(t *testing.T) {
	f := newBudgetFixture(t)
	f.limits = roombudget.Limits{Enabled: false, USDPerSeat24h: 1, CallsPerSeatMin: 1}
	f.usage["dev"] = roombudget.Usage{Cost: 99, Calls: 999}

	f.guard.Step(context.Background())
	if len(f.parks) != 0 || len(f.rows) != 0 || len(f.records) != 0 {
		t.Fatalf("off limits acted: parks=%v rows=%d records=%d", f.parks, len(f.rows), len(f.records))
	}
	if f.beats != 1 {
		t.Errorf("heartbeats = %d: a guard that measures nothing still proves it is alive", f.beats)
	}
	// And the state is reported ONCE, when it changes, not once a pass.
	f.guard.Step(context.Background())
	if got := strings.Count(strings.Join(f.logs, "\n"), "limits are OFF"); got != 1 {
		t.Errorf("the OFF line was logged %d times, want once: a line a pass would bury the park it exists beside", got)
	}
}

// TestTheParkRowFitsTheRailsCapsAtItsWorstCase: the rail REFUSES a body over 600 and a summary over 120, so a
// park whose row cannot be filed is a park nobody is told about. The seat's own directory is what varies in the
// body (its length is the state root's, not ours) and the numbers grow with the spend, so the case is driven at
// a production-length seat dir and eleven-digit numbers rather than at the fixture's tidy ones. The reviewer's
// note on the budget change measured the old body as over long before that: the prose was 626 chars at a
// production path with an ORDINARY cost.
func TestTheParkRowFitsTheRailsCapsAtItsWorstCase(t *testing.T) {
	f := newBudgetFixture(t)
	f.usage["dev"] = roombudget.Usage{Cost: 99999999999.99, Calls: 999999999}

	f.guard.Step(context.Background())
	if len(f.rows) != 1 {
		t.Fatalf("rows = %d, want one", len(f.rows))
	}
	row := f.rows[0]
	if len(row.Summary) > 120 || len(row.Body) > 600 {
		t.Errorf("row is %d/%d chars, over the rail's own caps (120/600); the park would be recorded and the "+
			"lead never told:\n%s", len(row.Summary), len(row.Body), row.Body)
	}
}

// TestARefusedStopStillParksTheSeat: THE RECORD IS THE PARK, so the record and the row stand even when the
// runtime would not stop the seat - the seat is refused work either way, and a park that vanished because a
// stop failed is a park nobody can act on. What the refusal costs is named in the log.
func TestARefusedStopStillParksTheSeat(t *testing.T) {
	f := newBudgetFixture(t)
	f.refuse["dev"] = true
	f.usage["dev"] = roombudget.Usage{Cost: 40}

	f.guard.Step(context.Background())
	if got, ok := f.records["dev"]; !ok || got.Cost != 40 {
		t.Fatalf("records = %v, want dev recorded: the record is the park and the stop did not happen", f.records)
	}
	if len(f.rows) != 1 {
		t.Fatalf("rows = %d, want one: the lead is told about a park the stop refused", len(f.rows))
	}
	if f.parks == nil && len(f.parks) != 0 {
		t.Errorf("parks = %v, want none: the runtime refused it", f.parks)
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "the stop was refused") {
		t.Errorf("logs = %v, want the refusal named", f.logs)
	}

	// The seat is still running, and the next pass finds its record: it is not stopped or filed a second time.
	f.usage["dev"] = roombudget.Usage{Cost: 41}
	f.guard.Step(context.Background())
	if len(f.rows) != 1 {
		t.Errorf("rows = %d, want still one: a seat already carrying a record is left alone", len(f.rows))
	}
}

// TestAParkRecordThatCouldNotBeWrittenLeavesTheSeatRunningAndRetries: with no record there is no park - no route
// refuses the seat work and nobody can read why it stopped - so the seat keeps running and the next pass tries
// again, which is the only reason g.over is not cleared on this path.
func TestAParkRecordThatCouldNotBeWrittenLeavesTheSeatRunningAndRetries(t *testing.T) {
	f := newBudgetFixture(t)
	f.recordFails = true
	f.usage["dev"] = roombudget.Usage{Cost: 40, Calls: 12}

	f.guard.Step(context.Background())
	if len(f.parks) != 0 || len(f.rows) != 0 {
		t.Fatalf("parks=%v rows=%d, want none: the record is the park and it was not written", f.parks, len(f.rows))
	}
	if len(f.seats) != 2 {
		t.Errorf("seats = %v, want dev still running: a seat with no record is left alone", f.seats)
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "it was NOT stopped") {
		t.Errorf("logs = %v, want the failed write and the reason named", f.logs)
	}

	f.recordFails = false
	f.guard.Step(context.Background())
	if len(f.parks) != 1 || f.parks[0] != "dev" {
		t.Errorf("parks = %v, want dev parked on the pass after the record landed", f.parks)
	}
	if _, ok := f.records["dev"]; !ok {
		t.Errorf("records = %v, want dev recorded", f.records)
	}
}
