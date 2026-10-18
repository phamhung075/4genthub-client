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
	room, main, seatRoot, previous := worktreeRoomDir, worktreeMainRoot, worktreeSeatRoot, worktreeEnsure
	worktreeRoomDir = func(string) (string, error) { return "/state/rooms/demo", nil }
	worktreeMainRoot = func(string) (string, error) { return "/main", nil }
	// No seat directory is readable unless a case says so: the default answer is "the definition names
	// no repository", which keeps every case that is not about the root on the working directory's one.
	worktreeSeatRoot = func([]string) (string, bool, error) { return "", false, nil }
	worktreeEnsure = ensure
	t.Cleanup(func() {
		worktreeRoomDir, worktreeMainRoot, worktreeSeatRoot, worktreeEnsure = room, main, seatRoot, previous
	})
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

// THE MEASURED DEFECT: `up` was run from a seat's client worktree, so the working directory named the
// CLIENT repository, and every derived path was refused as "not a worktree registered in `git worktree
// list` of <client repo>" - a refusal naming a repository the paths never belonged to. The definition
// records where the seats live, so the step derives from that root instead and says which it used.
func TestUpWorktreesStepDerivesFromTheRoomsOwnRepositoryNotTheWorkingDirectorys(t *testing.T) {
	var gotMain string
	stubWorktreeStepSeams(t, func(mainRoot, _, _ string, _ []clientservices.SeatType, _ io.Writer) error {
		gotMain = mainRoot
		return nil
	})
	seatCwd := "/project"
	worktreeSeatRoot = func(cwds []string) (string, bool, error) {
		if len(cwds) != 1 || cwds[0] != seatCwd {
			t.Errorf("the seat directories read as %q, want the spec's own %q", cwds, seatCwd)
		}
		return "/project", true, nil
	}
	spec := clientruntime.RoomSpec{Room: "demo", Seats: []clientruntime.SeatSpec{
		{SeatKey: "lead", SeatType: "lead", Cwd: &seatCwd},
	}}

	var out, errOut bytes.Buffer
	if code := ensureWorktreesImpl("demo", spec, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("the step exited %d (stderr %q)", code, errOut.String())
	}
	if gotMain != "/project" {
		t.Errorf("the step derived from %q, want the definition's repository %q", gotMain, "/project")
	}
	for _, want := range []string{"/project", "/main"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("the line does not name %q, so an operator cannot tell which root answered: %q", want, out.String())
		}
	}
}

// No readable seat directory - a fresh room whose paths do not exist yet, or a definition without cwds -
// leaves the working directory's answer in place: the fix must not invent a root.
func TestUpWorktreesStepKeepsTheWorkingDirectorysRootWhenNoSeatDirectoryAnswers(t *testing.T) {
	var gotMain string
	stubWorktreeStepSeams(t, func(mainRoot, _, _ string, _ []clientservices.SeatType, _ io.Writer) error {
		gotMain = mainRoot
		return nil
	})
	worktreeSeatRoot = func([]string) (string, bool, error) { return "", false, nil }
	spec := clientruntime.RoomSpec{Room: "demo", Seats: []clientruntime.SeatSpec{{SeatKey: "lead", SeatType: "lead"}}}

	var out, errOut bytes.Buffer
	if code := ensureWorktreesImpl("demo", spec, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("the step exited %d (stderr %q)", code, errOut.String())
	}
	if gotMain != "/main" {
		t.Errorf("the step derived from %q, want the working directory's %q", gotMain, "/main")
	}
	if out.Len() != 0 {
		t.Errorf("the step said %q about a root nobody named", out.String())
	}
}

// Seats that span two repositories cannot be derived from one root. Guessing either would derive into a
// tree the other half does not live in, so the working directory's answer stands and the disagreement
// is said: it is the operator's fact to fix, not a number to pick.
func TestUpWorktreesStepKeepsItsRootAndSaysSoWhenTheSeatsSpanTwoRepositories(t *testing.T) {
	var gotMain string
	stubWorktreeStepSeams(t, func(mainRoot, _, _ string, _ []clientservices.SeatType, _ io.Writer) error {
		gotMain = mainRoot
		return nil
	})
	worktreeSeatRoot = func([]string) (string, bool, error) {
		return "", false, errors.New("the seat directories belong to /one and /two")
	}
	spec := clientruntime.RoomSpec{Room: "demo", Seats: []clientruntime.SeatSpec{{SeatKey: "lead", SeatType: "lead"}}}

	var out, errOut bytes.Buffer
	if code := ensureWorktreesImpl("demo", spec, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("the step exited %d (stderr %q)", code, errOut.String())
	}
	if gotMain != "/main" {
		t.Errorf("the step derived from %q, want the working directory's %q", gotMain, "/main")
	}
	if !bytes.Contains(errOut.Bytes(), []byte("/one and /two")) {
		t.Errorf("the disagreement is not named: %q", errOut.String())
	}
}
