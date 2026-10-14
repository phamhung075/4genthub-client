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
	previousRestart, previousSkills := restartSeat, deliverSeatSkills
	t.Cleanup(func() { restartSeat, deliverSeatSkills = previousRestart, previousSkills })

	skills, restarts := 0, 0
	deliverSeatSkills = func(rig, seat string) error { skills++; return nil }
	var gotRig, gotSeat string
	restartSeat = func(rig, seat string) error { restarts++; gotRig, gotSeat = rig, seat; return nil }
	t.Setenv("PATH", t.TempDir())

	if err := relaunch("room1", "alpha"); err != nil {
		t.Fatalf("relaunch = %v, want nil with the runtime answering: a shell-out would have found no `rig` on PATH", err)
	}
	if skills != 1 || restarts != 1 {
		t.Fatalf("skills = %d, runtime restarts = %d, want 1 and 1", skills, restarts)
	}
	if gotRig != "room1" || gotSeat != "alpha" {
		t.Errorf("the runtime was asked for %s/%s, want room1/alpha", gotRig, gotSeat)
	}
}

// The ORDER is the property, not the plumbing: the seat's skills land BEFORE the runtime is asked to start it, and a
// delivery that fails must not start the seat at all - it would come up without the skills it was relaunched for.
func TestRelaunchDeliversSkillsBeforeItStartsTheSeat(t *testing.T) {
	previousRestart, previousSkills := restartSeat, deliverSeatSkills
	t.Cleanup(func() { restartSeat, deliverSeatSkills = previousRestart, previousSkills })

	refused := errors.New("the seat store cannot be read")
	deliverSeatSkills = func(rig, seat string) error { return refused }
	restartSeat = func(rig, seat string) error {
		t.Fatal("the seat was started although its skills were never delivered")
		return nil
	}

	if err := relaunch("room1", "alpha"); !errors.Is(err, refused) {
		t.Fatalf("relaunch = %v, want the delivery's own error", err)
	}
}

// A refusal from the runtime is reported as a failure naming the seat, rather than swallowed: the old code's
// tolerance for "did not respond in time" is gone with the CLI that produced it.
func TestRelaunchReportsTheRuntimesRefusal(t *testing.T) {
	previousRestart, previousSkills := restartSeat, deliverSeatSkills
	t.Cleanup(func() { restartSeat, deliverSeatSkills = previousRestart, previousSkills })

	deliverSeatSkills = func(rig, seat string) error { return nil }
	restartSeat = func(rig, seat string) error { return errors.New("seat alpha is not in room room1") }

	err := relaunch("room1", "alpha")
	if err == nil {
		t.Fatal("relaunch = nil, want the runtime's refusal reported")
	}
	if got := err.Error(); !strings.Contains(got, "room1/alpha") || !strings.Contains(got, "not in room") {
		t.Errorf("relaunch error = %q, want it to name the seat and carry the runtime's reason", got)
	}
}
