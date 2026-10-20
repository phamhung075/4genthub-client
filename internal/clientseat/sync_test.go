package clientseat

import (
	"errors"
	"strings"
	"testing"
)

// The relaunch conversion, pinned where it can fail: with NO `rig` and NO `tmux` on PATH the seat must still be
// relaunched, and the runtime must be asked for the exact room and seat. A shell-out that came back would fail to
// find its tool here - which is also the shape of the post-cutover machine.
func TestRelaunchAsksTheRuntimeForTheSeatAndNeverShellsOut(t *testing.T) {
	previousRestart := restartSeat
	t.Cleanup(func() { restartSeat = previousRestart })

	restarts := 0
	var gotRoom, gotSeat string
	restartSeat = func(room, seat string) error { restarts++; gotRoom, gotSeat = room, seat; return nil }
	t.Setenv("PATH", t.TempDir())

	if err := relaunch("room1", "alpha"); err != nil {
		t.Fatalf("relaunch = %v, want nil with the runtime answering: a shell-out would have found no `rig` on PATH", err)
	}
	if restarts != 1 {
		t.Fatalf("runtime restarts = %d, want 1", restarts)
	}
	if gotRoom != "room1" || gotSeat != "alpha" {
		t.Errorf("the runtime was asked for %s/%s, want room1/alpha", gotRoom, gotSeat)
	}
}

// A refusal from the runtime is reported as a failure naming the seat, rather than swallowed: the old code's
// tolerance for "did not respond in time" is gone with the CLI that produced it.
func TestRelaunchReportsTheRuntimesRefusal(t *testing.T) {
	previousRestart := restartSeat
	t.Cleanup(func() { restartSeat = previousRestart })

	restartSeat = func(room, seat string) error { return errors.New("seat alpha is not in room room1") }

	err := relaunch("room1", "alpha")
	if err == nil {
		t.Fatal("relaunch = nil, want the runtime's refusal reported")
	}
	if got := err.Error(); !strings.Contains(got, "room1/alpha") || !strings.Contains(got, "not in room") {
		t.Errorf("relaunch error = %q, want it to name the seat and carry the runtime's reason", got)
	}
}
