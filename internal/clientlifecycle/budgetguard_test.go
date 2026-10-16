// The budget guard's DECISION is what these tests pin, one pass at a time and with no rig, no socket and no
// clock: a seat over its USD ceiling is stopped, recorded and named on the rail exactly once; a seat over the
// call ceiling is stopped only after the breach held for a second pass; limits that are off measure nothing;
// and a stop the runtime refused is not recorded as a park.
package clientlifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

type budgetFixture struct {
	guard   *BudgetGuard
	limits  roombudget.Limits
	usage   map[string]roombudget.Usage
	seats   []string
	refuse  map[string]bool
	parks   []string
	records map[string]roombudget.Parked
	rows    []clientruntime.QueueCreateInput
	logs    []string
	beats   int
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
		f.records[seat] = parked
		return nil
	}
	guard.Row = func(_ string, row clientruntime.QueueCreateInput) error {
		f.rows = append(f.rows, row)
		return nil
	}
	guard.Log = func(text string) { f.logs = append(f.logs, text) }
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

// TestARefusedStopIsNeitherRecordedNorFiled: the order is the contract. A seat the runtime would not stop is a
// seat that was NOT parked, so the record and the row would be a claim about a stop that did not happen.
func TestARefusedStopIsNeitherRecordedNorFiled(t *testing.T) {
	f := newBudgetFixture(t)
	f.refuse["dev"] = true
	f.usage["dev"] = roombudget.Usage{Cost: 40}

	f.guard.Step(context.Background())
	if len(f.records) != 0 || len(f.rows) != 0 {
		t.Fatalf("a refused stop was recorded or filed: records=%v rows=%d", f.records, len(f.rows))
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "the stop was refused") {
		t.Errorf("logs = %v, want the refusal named", f.logs)
	}
}
