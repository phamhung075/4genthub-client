// Reading the facts, against a real repository: the two answers the rules turn on (is this a linked
// worktree, is the staged change carrying a secret) are git's own, and a rule that reads them wrongly is a
// rule that blocks or frees the wrong command.
package clientguard

import (
	"os"
	"path/filepath"
	"testing"
)

func noEnv(string) string { return "" }

func TestReadFactsTellsTheCheckoutFromALinkedWorktree(t *testing.T) {
	r := newGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "writer")
	r.git("worktree", "add", "-b", "seat-work", worktree)

	checkoutFacts, err := ReadFacts(HookPreCommit, r.dir, noEnv)
	if err != nil {
		t.Fatalf("ReadFacts in the checkout: %v", err)
	}
	if checkoutFacts.Linked {
		t.Errorf("the checkout reads as a linked worktree: %+v", checkoutFacts)
	}
	worktreeFacts, err := ReadFacts(HookPreCommit, worktree, noEnv)
	if err != nil {
		t.Fatalf("ReadFacts in the worktree: %v", err)
	}
	if !worktreeFacts.Linked {
		t.Errorf("the linked worktree does not read as one: %+v", worktreeFacts)
	}
	if worktreeFacts.Worktree != worktree {
		t.Errorf("Worktree = %q, want %q", worktreeFacts.Worktree, worktree)
	}
	// The identity this repository resolves for the seat, and the author git will record, are both readable
	// and agree when no override is set.
	if worktreeFacts.SeatName != "room/writer" || worktreeFacts.SeatEmail != "writer@room.example" {
		t.Errorf("seat identity = %q <%q>", worktreeFacts.SeatName, worktreeFacts.SeatEmail)
	}
	if worktreeFacts.AuthorName != worktreeFacts.SeatName || worktreeFacts.AuthorEmail != worktreeFacts.SeatEmail {
		t.Errorf("author = %q <%q>, want the seat's own", worktreeFacts.AuthorName, worktreeFacts.AuthorEmail)
	}
}

func TestReadFactsReadsTheSessionAndTheStagedChange(t *testing.T) {
	r := newGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "writer")
	r.git("worktree", "add", "-b", "seat-work", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "creds.txt"),
		[]byte("token = \"ghp_0123456789012345678901234567890123\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.gitIn(worktree, "add", "creds.txt")

	facts, err := ReadFacts(HookPreCommit, worktree, func(string) string { return "room-writer@room" })
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if !facts.HasSession || facts.Session.Seat != "writer" {
		t.Errorf("session = %+v (has %v), want the daemon's name", facts.Session, facts.HasSession)
	}
	// The staged change is what the scan reads, and this one carries a credential: the verdict warns about
	// it and refuses nothing (a secret is not a blocking finding).
	verdict := Decide(facts)
	if verdict.Refused() {
		t.Errorf("refusals = %v, want none for this commit", verdict.Refusals)
	}
	if len(verdict.Warnings) != 1 {
		t.Errorf("warnings = %v, want the secret warning read from the staged change", verdict.Warnings)
	}
}
