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
	// A derived worktree on another branch: registered, and on the branch its seat works on. It is
	// Registered, not Wrong - a seat's worktree moves onto its lane, and the branch the derivation
	// names is where a MISSING path is created.
	moved := filepath.Join(fixture.room, "wt", "review")
	gitIn(t, moved, "checkout", "-q", "-b", "somebody-elses-branch")
	// A derived worktree whose client link was never placed: that is the shape a seat cannot build in.
	wrong := filepath.Join(fixture.room, "wt", "lead")
	if err := os.RemoveAll(filepath.Join(wrong, clientSubmodule)); err != nil {
		t.Fatalf("remove the client link under %s: %v", wrong, err)
	}
	// An entry that is not derived at all.
	foreign := filepath.Join(t.TempDir(), "somebodys-checkout")
	gitIn(t, fixture.main, "worktree", "add", "--detach", foreign, "main")
	removeTmpWorktree(t, fixture.main, foreign)

	verdict, err := Check(fixture.main, fixture.room, fixture.name, fixture.seats)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	wantRegistered := []string{
		moved,
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

	verdict, err := Check(fixture.main, fixture.room, fixture.name, fixture.seats)
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

// A link whose metadata is gone reads as a link on disk and is not one: git answers `fatal: not a git
// repository` there, so the path is Wrong, exactly as a path with no link at all is.
func TestCheckCallsADanglingClientLinkWrong(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	seat := fixture.derived()[0]
	link := filepath.Join(seat.Path, clientSubmodule)
	gitIn(t, fixture.main, "worktree", "add", "-b", seat.Branch, seat.Path, "main")
	mkdirAll(t, link)
	writeFile(t, filepath.Join(link, ".git"),
		"gitdir: "+filepath.Join(fixture.main, clientSubmodule, ".git", "worktrees", "gone")+"\n")

	verdict, err := Check(fixture.main, fixture.room, fixture.name, fixture.seats)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !equalStrings(verdict.Wrong, []string{seat.Path}) {
		t.Errorf("Wrong = %v, want %v", verdict.Wrong, []string{seat.Path})
	}
	if len(verdict.Registered) != 0 || len(verdict.Missing) != 0 {
		t.Errorf("Registered = %v and Missing = %v, want both empty", verdict.Registered, verdict.Missing)
	}
}

// A room whose worktrees were never started is Missing, not an error: "not started yet" is the state
// `up` exists to change.
func TestCheckReportsARoomThatWasNeverStarted(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "lead", Type: "lead"}})
	verdict, err := Check(fixture.main, fixture.room, fixture.name, fixture.seats)
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
