package clientlifecycle

import (
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// statesOf is what the socket half would publish for one safeguard from one drain of pending transitions.
func statesOf(rows []clientservices.Health, safeguard, room string) []clientservices.Health {
	var out []clientservices.Health
	for _, r := range rows {
		if r.Safeguard == safeguard && r.Room == room {
			out = append(out, r)
		}
	}
	return out
}

// TestAKilledChildPublishesStoppedThenRunningOnly: the hub alerts on running -> stopped/silent and on restarts
// going up. A kill used to publish stopped, then silent (the new child had not beaten yet, the file named
// the dead pid), then running: two alerts and a "silent (last beat 0s ago)" row. It must publish exactly
// stopped, then running with the restart counted.
func TestAKilledChildPublishesStoppedThenRunningOnly(t *testing.T) {
	f := newChildrenFixture(t, "demo")
	f.everythingRunning()
	budget := clientservices.Pattern(clientservices.Budget, "demo")
	spawnChild = func(args []string, _ string) (supervisorChild, error) {
		f.spawned = append(f.spawned, args)
		f.live[budget] = []int{4243}
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		return survivedChild{release: release}, nil
	}
	f.manager.pass()
	f.manager.take()

	delete(f.live, budget) // the kill
	f.advance(time.Second)
	f.manager.pass()
	got := statesOf(f.manager.take(), clientservices.Budget, "demo")
	if len(got) != 1 || got[0].State != clientservices.Stopped {
		t.Fatalf("after the kill published %+v, want one stopped row", got)
	}

	var published []clientservices.Health
	for i := 0; i < 5; i++ {
		f.advance(time.Second)
		f.manager.pass()
		published = append(published, statesOf(f.manager.take(), clientservices.Budget, "demo")...)
	}
	if len(published) != 1 || published[0].State != clientservices.Running || published[0].Restarts != 1 || published[0].PID != 4243 {
		t.Fatalf("after the restart published %+v, want exactly one running row, restarts 1, pid 4243", published)
	}
}

// TestAPausedHostPublishesNoSilence: a suspend of the host left every beat minutes old, and the first pass
// after it published every row `silent` - agent itself included - and every row `running` a second later
// (alerts.log 2026-10-27T04:15:26Z: agent 548s, bridge 541s, watchdog 537s). After a pause, silence is
// measured from the resume, so nothing is published while the children catch up; a child that still does
// not beat within its threshold of the resume is silent.
func TestAPausedHostPublishesNoSilence(t *testing.T) {
	f := newChildrenFixture(t, "demo")
	f.survives()
	f.everythingRunning()
	f.manager.pass()
	f.manager.take()

	f.advance(548 * time.Second) // the host slept; no beat was written by anyone
	f.manager.pass()
	for _, r := range f.manager.take() {
		if r.State != clientservices.Running {
			t.Errorf("the first pass after a pause published %+v", r)
		}
	}

	// Everyone but compact beats again; compact stays quiet past its 30 s threshold from the resume. The
	// loop runs its one-second passes again, so no second pause is seen.
	var got []clientservices.Health
	for i := 0; i < 31; i++ {
		f.advance(time.Second)
		for _, s := range []struct {
			name, room string
			pid        int
		}{{clientservices.Watchdog, "demo", 4102}, {clientservices.Messages, "demo", 4105}, {clientservices.RoomGuard, "demo", 4106},
			{clientservices.Budget, "demo", 4107}, {clientservices.Bridge, "", 4103}, {clientservices.Agent, "", 4104}} {
			f.beat(s.name, s.room, 0, s.pid)
		}
		f.manager.pass()
		got = append(got, f.manager.take()...)
	}
	if len(got) != 1 || got[0].Safeguard != clientservices.Compact || got[0].State != clientservices.Silent {
		t.Fatalf("a threshold after the resume published %+v, want compact silent only", got)
	}
}
