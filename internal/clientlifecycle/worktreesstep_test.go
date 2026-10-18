// The worktrees step `up` runs: it derives from the seats of the spec THIS START is about - never a
// second read of the definition - and a refusal from the derivation is `up`'s own refusal, naming the
// paths. Both are asserted here with the derivation and the root stubbed, so no case needs a git
// repository on the machine.
package clientlifecycle

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

func stubWorktreeStepSeams(t *testing.T, ensure func(string, string, string, []clientservices.SeatType, io.Writer) error) {
	t.Helper()
	room, main, previous := worktreeRoomDir, worktreeMainRoot, worktreeEnsure
	worktreeRoomDir = func(string) (string, error) { return "/state/rooms/demo", nil }
	worktreeMainRoot = func(string) (string, error) { return "/main", nil }
	worktreeEnsure = ensure
	t.Cleanup(func() { worktreeRoomDir, worktreeMainRoot, worktreeEnsure = room, main, previous })
}

func TestUpWorktreesStepDerivesTheSeatsOfThisStart(t *testing.T) {
	var (
		gotMain, gotRoom, gotRig string
		gotSeats                 []clientservices.SeatType
	)
	stubWorktreeStepSeams(t, func(mainRoot, roomDir, rig string, seats []clientservices.SeatType, _ io.Writer) error {
		gotMain, gotRoom, gotRig, gotSeats = mainRoot, roomDir, rig, seats
		return nil
	})
	spec := clientruntime.RoomSpec{Room: "demo", Seats: []clientruntime.SeatSpec{
		{SeatKey: "lead", SeatType: "lead"},
		{SeatKey: "go-dev", SeatType: "developer"},
	}}

	var out, errOut bytes.Buffer
	if code := ensureWorktreesImpl("demo", spec, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("the step exited %d (stderr %q)", code, errOut.String())
	}
	if gotMain != "/main" || gotRoom != "/state/rooms/demo" || gotRig != "demo" {
		t.Errorf("ensure called with (%q, %q, %q), want (/main, /state/rooms/demo, demo)", gotMain, gotRoom, gotRig)
	}
	want := []clientservices.SeatType{{Key: "lead", Type: "lead"}, {Key: "go-dev", Type: "developer"}}
	if len(gotSeats) != len(want) || gotSeats[0] != want[0] || gotSeats[1] != want[1] {
		t.Errorf("seats = %+v, want the spec's own in its order: %+v", gotSeats, want)
	}
}

// A derivation that refuses any of its paths refuses the start: the alternative is a seat launched in
// the room, editing the shared checkout, which is the defect the ruling removes.
func TestUpWorktreesStepRefusesTheStartWhenTheDerivationRefuses(t *testing.T) {
	stubWorktreeStepSeams(t, func(string, string, string, []clientservices.SeatType, io.Writer) error {
		return errors.New("refused 1 of 1 derived worktree(s): /state/rooms/demo/wt/lead (held by another branch)")
	})
	spec := clientruntime.RoomSpec{Room: "demo", Seats: []clientruntime.SeatSpec{{SeatKey: "lead", SeatType: "lead"}}}

	var out, errOut bytes.Buffer
	if code := ensureWorktreesImpl("demo", spec, &out, &errOut); code != clientcmd.ExitUnavailable {
		t.Fatalf("the step exited %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("/state/rooms/demo/wt/lead")) {
		t.Errorf("the refusal does not name the path: %q", errOut.String())
	}
}
