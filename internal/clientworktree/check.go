package clientworktree

import (
	"os"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// Check is the ruling's read-only half: it says which of the room's derived worktrees are registered
// where they belong, and which are not, and it never writes anything. It is what `doctor`'s worktrees
// row reads, so it answers in buckets rather than in a single yes or no - a room with one worktree
// missing and one foreign entry is a different finding from a room that was never started.

// CheckVerdict is the ruling's Check as data: the four buckets one reading of `git worktree list`
// sorts a room's worktrees into.
//
// A foreign entry is NOT an error and never fails the check: the ruling says a foreign count above
// zero is a detail on doctor's worktrees row, and this client's own machine holds foreign worktrees by
// design (the gates in `/tmp/gate-*`, and checkouts an operator keeps beside the repository).
//
// A registered worktree is Registered WHATEVER BRANCH IT HOLDS. A seat's worktree moves onto the lane
// it is working, and a reviewer's is detached; the branch the derivation names is the branch a MISSING
// path is created on, not a rule an existing one must keep. Measured on the live room (2026-10-18):
// fe-dev and architect sat on other branches and the reviewer on its own, and a check that called all
// three wrong refused every start of a room that was fine.
type CheckVerdict struct {
	Registered []string // derived and registered, with the client link present
	Missing    []string // derived, not in `git worktree list` at all
	Wrong      []string // derived, present but not right (not a worktree, or the client link missing)
	Foreign    []string // every entry that is not derived
}

// Check reads one listing and sorts the room's derived worktrees into the verdict's buckets. Its only
// failure is a listing it could not read: a room whose derived paths are all missing is a Verdict with
// four names in Missing, not an error, because "not started yet" is the state `up` exists to change.
func Check(mainRoot, roomDir, rig string, seats []clientservices.SeatType) (CheckVerdict, error) {
	entries, err := Read(mainRoot)
	if err != nil {
		return CheckVerdict{}, err
	}
	present := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		present[canonicalPath(entry.Path)] = entry
	}
	derived := clientservices.DerivedWorktrees(roomDir, rig, seats)
	derivedPaths := make(map[string]bool, len(derived))
	var verdict CheckVerdict
	for _, worktree := range derived {
		derivedPaths[canonicalPath(worktree.Path)] = true
		_, registered := present[canonicalPath(worktree.Path)]
		switch {
		case !registered:
			// A path that is on disk but not in the listing is a path that is not a worktree: it is
			// the ruling's other refusal case, and it reads as Wrong rather than as Missing, because a
			// start step will refuse it rather than create it.
			if _, err := os.Lstat(worktree.Path); err == nil {
				verdict.Wrong = append(verdict.Wrong, worktree.Path)
			} else {
				verdict.Missing = append(verdict.Missing, worktree.Path)
			}
		case !clientLinkPresent(worktree.Path):
			verdict.Wrong = append(verdict.Wrong, worktree.Path)
		default:
			verdict.Registered = append(verdict.Registered, worktree.Path)
		}
	}
	for _, entry := range entries {
		if !derivedPaths[canonicalPath(entry.Path)] {
			verdict.Foreign = append(verdict.Foreign, entry.Path)
		}
	}
	return verdict, nil
}

// clientLinkPresent is the second half of the ruling's registered test: the seat's client submodule
// must be a linked worktree under the derived path. A `.git` that is missing is the shape a worktree
// has when the link was never placed, which is exactly the seat that cannot build.
func clientLinkPresent(path string) bool {
	_, err := os.Lstat(filepath.Join(path, clientSubmodule, ".git"))
	return err == nil
}
