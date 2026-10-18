package clientworktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// The ruling's Check, one bucket per state, over a real fixture: right, missing, present on another
// branch, and a foreign entry that must not count as a failure.
func TestCheckSortsTheDerivedWorktreesIntoBuckets(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{
		{Key: "lead", Type: "lead"},
		{Key: "go-dev", Type: "developer"},
		{Key: "review", Type: "reviewer"},
	})
	stubLinkProviderKey(t)
	var ignored strings.Builder
	if err := fixture.ensure(&ignored); err != nil {
		t.Fatalf("Ensure: %v\n%s", err, ignored.String())
	}

	// A derived worktree taken away: not in `git worktree list`, not on disk.
	missing := filepath.Join(fixture.room, "wt", "go-dev")
	if err := os.RemoveAll(missing); err != nil {
		t.Fatalf("remove %s: %v", missing, err)
	}
	gitIn(t, fixture.main, "worktree", "prune")
	// A derived worktree on another branch: registered, and on the wrong one.
	wrong := filepath.Join(fixture.room, "wt", "review")
	gitIn(t, wrong, "checkout", "-q", "-b", "somebody-elses-branch")
	// An entry that is not derived at all.
	foreign := filepath.Join(t.TempDir(), "somebodys-checkout")
	gitIn(t, fixture.main, "worktree", "add", "--detach", foreign, "main")
	removeTmpWorktree(t, fixture.main, foreign)

	verdict, err := Check(fixture.main, fixture.room, fixture.rig, fixture.seats)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	wantRegistered := []string{
		filepath.Join(fixture.room, "wt", "lead"),
		filepath.Join(fixture.room, "wt", "integration"),
	}
	if !equalStrings(verdict.Registered, wantRegistered) {
		t.Errorf("Registered = %v, want %v", verdict.Registered, wantRegistered)
	}
	if !equalStrings(verdict.Missing, []string{missing}) {
		t.Errorf("Missing = %v, want %v", verdict.Missing, []string{missing})
	}
	if !equalStrings(verdict.Wrong, []string{wrong}) {
		t.Errorf("Wrong = %v, want %v", verdict.Wrong, []string{wrong})
	}
	for _, want := range []string{fixture.main, foreign} {
		if !containsString(verdict.Foreign, want) {
			t.Errorf("Foreign = %v, want it to hold %s", verdict.Foreign, want)
		}
	}
	if len(verdict.Foreign) != 2 {
		t.Errorf("Foreign = %v, want only the main checkout and the foreign worktree", verdict.Foreign)
	}
}

// A derived path that exists on disk but is not a worktree is Wrong, not Missing: a start step refuses
// it rather than creating it. Its own case, because the two readings are easy to confuse.
func TestCheckCallsAPathThatIsNotAWorktreeWrong(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	path := fixture.derived()[0].Path
	mkdirAll(t, path)
	writeFile(t, filepath.Join(path, "operator.txt"), "an operator's own directory\n")

	verdict, err := Check(fixture.main, fixture.room, fixture.rig, fixture.seats)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !equalStrings(verdict.Wrong, []string{path}) {
		t.Errorf("Wrong = %v, want %v", verdict.Wrong, []string{path})
	}
	if len(verdict.Missing) != 0 || len(verdict.Registered) != 0 {
		t.Errorf("Missing = %v and Registered = %v, want both empty", verdict.Missing, verdict.Registered)
	}
}

// A room whose worktrees were never started is Missing, not an error: "not started yet" is the state
// `up` exists to change.
func TestCheckReportsARoomThatWasNeverStarted(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "lead", Type: "lead"}})
	verdict, err := Check(fixture.main, fixture.room, fixture.rig, fixture.seats)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := []string{
		filepath.Join(fixture.room, "wt", "lead"),
		filepath.Join(fixture.room, "wt", "integration"),
	}
	if !equalStrings(verdict.Missing, want) {
		t.Errorf("Missing = %v, want %v", verdict.Missing, want)
	}
	if len(verdict.Wrong) != 0 {
		t.Errorf("Wrong = %v, want nothing", verdict.Wrong)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
