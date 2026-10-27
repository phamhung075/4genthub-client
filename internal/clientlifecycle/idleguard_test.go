package clientlifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
	"github.com/phamhung075/4genthub-client/internal/clientjev"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

type guardRoom struct {
	guard    *IdleGuard
	said     []string
	asked    int
	now      float64
	modified map[string]float64
}

func newGuardRoom(t *testing.T, idle map[string]bool, pick clientjev.Selection) *guardRoom {
	t.Helper()
	r := &guardRoom{now: 10000}
	r.guard = &IdleGuard{Room: "demo", Lead: "lead", Quiet: 300, Stall: 900, State: map[string]*stallState{}}
	r.guard.Now = func() float64 { return r.now }
	r.guard.Seats = func(string) []string { return []string{"lead", "go-dev"} }
	r.guard.SeatLog = func(_, seat string) (string, string) { return "/log/" + seat, "omp" }
	r.guard.Idle = func(_, seat string) bool { return idle[seat] }
	r.guard.Say = func(_, seat, text string, _ bool) { r.said = append(r.said, seat+": "+text) }
	r.guard.Interrupt = func(string, string) {}
	r.guard.Active = func(string) ([]clientruntime.QueueRow, error) {
		return []clientruntime.QueueRow{{QitemID: "q1", State: "pending", DestinationSession: "go-dev", Summary: "ship it"}}, nil
	}
	r.guard.Ask = func(context.Context, string, string, clientdecisions.Question) (clientjev.Selection, float64, error) {
		r.asked++
		return pick, 0.8, nil
	}
	r.guard.Wake = func(string) (bool, string) { return true, "" }
	r.guard.Log = func(string) {}
	r.guard.Modified = func(path string) float64 { return r.modified[path] }
	r.modified = map[string]float64{"/log/lead": 9000, "/log/go-dev": 9000}
	return r
}

func TestAnIdleRoomIsAskedOnceAndTheLeadToldToContinue(t *testing.T) {
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": true}, clientjev.Selection{OptionID: "continue", Confidence: 0.9})
	r.guard.Step(context.Background())
	r.guard.Step(context.Background())
	if r.asked != 1 {
		t.Errorf("Jev asked %d times for one quiet, want 1", r.asked)
	}
	if len(r.said) != 1 || !strings.HasPrefix(r.said[0], "lead: ") || !strings.Contains(r.said[0], "CONTINUE") || !strings.Contains(r.said[0], "q1") {
		t.Errorf("said = %q", r.said)
	}
}

func TestALowConfidenceAnswerSendsTheLeadToTheOwner(t *testing.T) {
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": true}, clientjev.Selection{OptionID: "continue", Confidence: 0.5})
	r.guard.Step(context.Background())
	if len(r.said) != 1 || !strings.Contains(r.said[0], "ask the owner") {
		t.Errorf("said = %q", r.said)
	}
}

func TestTheSameOpenRailIsNotAskedAgainWhenTheLeadsOwnReplyMakesANewQuiet(t *testing.T) {
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": true}, clientjev.Selection{OptionID: "continue", Confidence: 0.5})
	r.guard.Step(context.Background())
	r.modified["/log/lead"] = 9500 // the lead answered the message: a later log time, so a new quiet
	r.now = 10000
	r.guard.Step(context.Background())
	if r.asked != 1 {
		t.Fatalf("Jev asked %d times for an unchanged rail, want 1", r.asked)
	}
	r.modified["/log/lead"] = 9700
	r.guard.Active = func(string) ([]clientruntime.QueueRow, error) {
		return []clientruntime.QueueRow{{QitemID: "q2", State: "pending", DestinationSession: "go-dev", Summary: "new"}}, nil
	}
	r.guard.Step(context.Background())
	if r.asked != 2 {
		t.Errorf("a changed rail must be asked again, asked = %d", r.asked)
	}
}

func TestAWorkingSeatMeansTheRoomIsNotAsked(t *testing.T) {
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": false}, clientjev.Selection{OptionID: "stop", Confidence: 0.9})
	r.modified["/log/go-dev"] = 9900
	r.guard.Step(context.Background())
	if r.asked != 0 || len(r.said) != 0 {
		t.Errorf("asked %d, said %q while a seat was working", r.asked, r.said)
	}
}

func TestAStuckSeatIsToldToContinueThenTheLead(t *testing.T) {
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": false}, clientjev.Selection{})
	for range stallNudges + 2 {
		r.guard.Step(context.Background())
		r.now += 1000
		r.modified["/log/go-dev"] = r.now - 2000
		r.modified["/log/lead"] = r.now
	}
	var toSeat, toLead int
	for _, said := range r.said {
		switch {
		case strings.HasPrefix(said, "go-dev: "):
			toSeat++
		case strings.HasPrefix(said, "lead: ") && strings.Contains(said, "go-dev has logged nothing"):
			toLead++
		}
	}
	if toSeat != stallNudges || toLead != 1 {
		t.Errorf("seat nudged %d times, lead told %d times; said %q", toSeat, toLead, r.said)
	}
}

func TestAnIdleRoomWithActiveRailReSignalsLeadAfterQuietTimeout(t *testing.T) {
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": true}, clientjev.Selection{OptionID: "continue", Confidence: 0.9})
	r.guard.Step(context.Background())
	if len(r.said) != 1 {
		t.Fatalf("want 1 initial message, got %d (%v)", len(r.said), r.said)
	}
	// Before 2*Quiet elapses, no new reminder is sent
	r.now += r.guard.Quiet
	r.guard.Step(context.Background())
	if len(r.said) != 1 {
		t.Fatalf("expected no reminder before 2*Quiet, got %d (%v)", len(r.said), r.said)
	}
	// After 2*Quiet, the reminder is sent to the lead
	r.now += r.guard.Quiet + 1
	r.guard.Step(context.Background())
	if len(r.said) != 2 || !strings.Contains(r.said[1], "still idle with 1 open rail items") {
		t.Fatalf("expected reminder to lead after 2*Quiet, got %d (%v)", len(r.said), r.said)
	}
}

func TestAnIdleSeatWithInProgressWorkIsNudged(t *testing.T) {
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": true}, clientjev.Selection{})
	r.guard.Active = func(string) ([]clientruntime.QueueRow, error) {
		return []clientruntime.QueueRow{{QitemID: "q1", State: "in-progress", DestinationSession: "go-dev", Summary: "ship it"}}, nil
	}
	r.modified["/log/go-dev"] = r.now - 2000
	r.guard.Step(context.Background())
	var nudged bool
	for _, said := range r.said {
		if strings.HasPrefix(said, "go-dev: ") && strings.Contains(said, "Continue where you stopped") {
			nudged = true
			break
		}
	}
	if !nudged {
		t.Errorf("idle seat with in-progress work was not nudged, said: %v", r.said)
	}
}
