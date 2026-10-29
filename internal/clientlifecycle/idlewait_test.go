package clientlifecycle

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientjev"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/seatwait"
)

// waitRoom is a guard room whose go-dev holds an in-progress row and has been silent past the stall bound, so
// without a wait it is nudged.
func waitRoom(t *testing.T) (*guardRoom, string, *[]string) {
	t.Helper()
	r := newGuardRoom(t, map[string]bool{"lead": true, "go-dev": true}, clientjev.Selection{OptionID: "stop", Confidence: 0.9})
	root := t.TempDir()
	r.guard.Root = root
	r.guard.Active = func(string) ([]clientruntime.QueueRow, error) {
		return []clientruntime.QueueRow{{QitemID: "q1", State: "in-progress", DestinationSession: "go-dev", Summary: "fit math"}}, nil
	}
	var alerts []string
	r.guard.Escalate = func(room, summary, body string) error {
		alerts = append(alerts, summary+"\n"+body)
		return nil
	}
	return r, root, &alerts
}

func declare(t *testing.T, root, seat, kind, target string, at float64, life time.Duration) {
	t.Helper()
	started := unixTime(at)
	rec := seatwait.Record{Room: "demo", Seat: seat, Kind: kind, Target: target, Started: started, Deadline: started.Add(life)}
	if err := seatwait.Snapshot(&rec, []clientruntime.QueueRow{{QitemID: "q1", State: "in-progress", DestinationSession: "go-dev"}}); err != nil {
		t.Fatal(err)
	}
	if err := seatwait.Start(root, rec); err != nil {
		t.Fatal(err)
	}
}

func TestTheIdleGuardNudgesASilentSeatThatDeclaredNoWait(t *testing.T) {
	r, _, _ := waitRoom(t)
	r.guard.Step(context.Background())
	if len(r.said) == 0 || !strings.HasPrefix(r.said[0], "go-dev: ") || !strings.Contains(r.said[0], "went quiet") {
		t.Fatalf("baseline: a silent seat with an in-progress row must be nudged, said = %q", r.said)
	}
}

func TestTheIdleGuardSkipsAWaitingSeatAndDoesNotCallTheRoomQuiet(t *testing.T) {
	r, root, alerts := waitRoom(t)
	declare(t, root, "go-dev", "review", "q1", r.now-60, time.Hour)
	r.guard.Step(context.Background())
	r.guard.Step(context.Background())
	if len(r.said) != 0 || r.asked != 0 {
		t.Errorf("a waiting seat was nudged or the room asked: said %q, asked %d", r.said, r.asked)
	}
	if len(*alerts) != 0 {
		t.Errorf("a review wait is not on the director, yet the owner was alerted: %q", *alerts)
	}
	if _, ok := seatwait.Read(root, "demo", "go-dev"); !ok {
		t.Error("the wait was ended with nothing to end it")
	}
}

func TestAnExpiredWaitWakesTheSeatOnceAndIsLogged(t *testing.T) {
	r, root, _ := waitRoom(t)
	declare(t, root, "go-dev", "merge", "step6@f85361d", r.now-3600, time.Hour)
	r.guard.Step(context.Background())
	if len(r.said) != 1 || r.said[0] != "go-dev: wait expired after 1h00m: merge:step6@f85361d still open - pick a backup item or ask" {
		t.Fatalf("said = %q", r.said)
	}
	if _, ok := seatwait.Read(root, "demo", "go-dev"); ok {
		t.Error("the expired wait is still recorded")
	}
	log, _ := os.ReadFile(seatwait.LogPath(root, "demo"))
	if !strings.Contains(string(log), `"how":"expired"`) {
		t.Errorf("log = %s", log)
	}
}

func TestANewAssignedItemCancelsTheWaitWithoutAMessage(t *testing.T) {
	r, root, _ := waitRoom(t)
	declare(t, root, "go-dev", "review", "q1", r.now-60, time.Hour)
	r.guard.Active = func(string) ([]clientruntime.QueueRow, error) {
		return []clientruntime.QueueRow{
			{QitemID: "q1", State: "in-progress", DestinationSession: "go-dev"},
			{QitemID: "q2", State: "pending", DestinationSession: "go-dev"},
		}, nil
	}
	r.guard.Step(context.Background())
	if _, ok := seatwait.Read(root, "demo", "go-dev"); ok {
		t.Fatal("a new row for the seat did not end its wait")
	}
	for _, line := range r.said {
		if strings.Contains(line, "wait ") {
			t.Errorf("a new item is its own wake, yet the seat was told %q", line)
		}
	}
}

func TestAMessageToTheSeatEndsTheWait(t *testing.T) {
	r, root, _ := waitRoom(t)
	declare(t, root, "go-dev", "answer", "QUESTION-3", r.now-60, time.Hour)
	r.guard.Step(context.Background()) // settles: the declaring turn has ended
	r.modified["/log/go-dev"] = r.now + 10
	r.now += 30
	r.guard.Step(context.Background())
	if _, ok := seatwait.Read(root, "demo", "go-dev"); ok {
		t.Fatal("a new turn did not end the wait")
	}
	log, _ := os.ReadFile(seatwait.LogPath(root, "demo"))
	if !strings.Contains(string(log), `"how":"message"`) {
		t.Errorf("log = %s", log)
	}
}

func TestTheAwaitedMergeEndsTheWaitWithOneLine(t *testing.T) {
	r, root, _ := waitRoom(t)
	started := unixTime(r.now - 60)
	rec := seatwait.Record{Room: "demo", Seat: "go-dev", Kind: "merge", Target: "step6@f85361d", Repo: "/repo", SHA: "f85361d",
		Started: started, Deadline: started.Add(time.Hour), Snapshotted: true, Covered: []string{"q1:in-progress"}}
	if err := seatwait.Start(root, rec); err != nil {
		t.Fatal(err)
	}
	r.guard.WaitProbes = seatwait.Probes{MergedAs: func(string, string) (string, bool) { return "abc1234", true }}
	r.guard.Step(context.Background())
	if len(r.said) != 1 || r.said[0] != "go-dev: wait ended: merge:step6@f85361d merged as abc1234" {
		t.Errorf("said = %q", r.said)
	}
}

func TestEveryoneWaitingOnTheDirectorAlertsTheOwnerOncePerEpisode(t *testing.T) {
	r, root, alerts := waitRoom(t)
	declare(t, root, "go-dev", "merge", "step6@f85361d", r.now-60, 4*time.Hour)
	r.guard.Step(context.Background())
	r.now += 30
	r.guard.Step(context.Background())
	if len(*alerts) != 1 || !strings.Contains((*alerts)[0], "go-dev=merge:step6@f85361d") {
		t.Fatalf("alerts = %q", *alerts)
	}
	// A guard restarted inside the same episode does not raise it again.
	r.guard.State = map[string]*stallState{}
	r.guard.Step(context.Background())
	if len(*alerts) != 1 {
		t.Fatalf("the same episode alerted %d times", len(*alerts))
	}
	// The lead takes up work: the episode ends; when the room blocks again it is a new episode.
	r.guard.Idle = func(_, seat string) bool { return seat != "lead" }
	r.guard.Step(context.Background())
	r.guard.Idle = func(string, string) bool { return true }
	r.guard.Step(context.Background())
	if len(*alerts) != 2 {
		t.Errorf("a new episode must alert again, alerts = %d", len(*alerts))
	}
}
