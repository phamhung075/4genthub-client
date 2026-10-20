// The `clone` row of `doctor`: the changes sitting in the SHARED checkout, which belong to no seat.
//
// A seat commits in its own worktree (ai_docs/core-architecture/seat-worktrees.md), so a changed path in
// the shared checkout is a change no seat owns. That is 4.1's third measured failure: a generated file,
// `ai_docs/index.json`, sat changed in the shared checkout, and eleven landing-branch files appeared there
// with no seat claiming them. The row names every path, because a row that says the checkout is dirty
// without naming one is a row a reader cannot act on - the same reason the seats, worktrees and orphan
// rows carry their lines beside them.
//
// THE CHECKOUT IS THE ONE `up` AND THE WORKTREES ROW ALREADY RESOLVE (clientworktree.MainRoot). A second
// resolution here would be a second answer to "which checkout are these worktrees in", and the two would
// disagree the first time a flag or an environment variable moved.
//
// THE ROW IS THE MACHINE'S, NOT A ROOM'S: two rooms share one checkout, so a row per room would repeat
// the same paths. It therefore carries no room, like the bridge and agent rows, and it is reported whenever
// the machine has a checkout at all - including a machine whose agent has never run, which is exactly a
// machine whose checkout may have been edited by hand.
//
// AN INPUT THAT COULD NOT BE READ IS A NOTE, NEVER A FAILURE, like the worktrees row: the row answers
// "what is changed in the shared checkout", and a client that could not look has no answer to give.
package clientlifecycle

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientworktree"
)

// cloneRow is the row plus what produced it.
type cloneRow struct {
	Row     clientservices.Health
	Changed []string
	// Note names an input this client could not read. It is never a failure: a row that could not look
	// says so, and the exit code that refuses belongs to a row that looked and found something.
	Note string
}

// cloneReasonLines are the lines a refusal must carry: one per changed path, each saying the change is the
// shared checkout's and no seat's.
func cloneReasonLines(row cloneRow) []string {
	lines := make([]string, 0, len(row.Changed))
	for _, path := range row.Changed {
		lines = append(lines, clientservices.Clone+": "+path+
			" is changed in the shared checkout and no seat's worktree owns it")
	}
	return lines
}

// cloneLines is one clone row as the report prints it: the reasons first, then the note about an input
// that could not be read. The human report prints these; `--json` carries the same lines as the row's
// reasons, from this ONE function, so the two surfaces cannot disagree.
func cloneLines(row cloneRow) []string {
	lines := cloneReasonLines(row)
	if row.Note != "" {
		lines = append(lines, row.Note)
	}
	return lines
}

// cloneRowFor is the clone-row computation as a seam, beside rowsFor, seatsRowFor, worktreesRowFor and
// orphanRowsFor: a case drives the row's three outcomes - clean, dirty and an input that could not be
// read - without a repository on the machine.
var cloneRowFor = cloneRows

// cloneRows is the row the report covers. The argument is the room the caller named, and it is deliberately
// unused: the checkout belongs to the machine, not to a room.
func cloneRows(string) []cloneRow { return []cloneRow{cloneRowOf()} }

// cloneRowOf is the row itself. Every step that cannot read its input produces the note rather than a
// state, because a row that says the checkout is dirty when the client could not look is a lie with a
// confident face.
func cloneRowOf() cloneRow {
	row := cloneRow{Row: clientservices.Health{
		Safeguard: clientservices.Clone,
		State:     clientservices.Running,
		AgeS:      clientservices.UnknownAge,
	}}
	root, err := cloneMainRoot("")
	if err != nil {
		row.Note = clientservices.Clone + ": not observed: " + err.Error()
		return row
	}
	changed, err := cloneChanged(root)
	if err != nil {
		row.Note = clientservices.Clone + ": not observed: " + err.Error()
		return row
	}
	row.Changed = changed
	if len(changed) > 0 {
		row.Row.State = clientservices.CloneDirty
	}
	return row
}

// cloneMainRoot and cloneChanged are the checkout resolution and the git read as seams, the way the
// worktrees row keeps its own two.
var (
	cloneMainRoot = clientworktree.MainRoot
	cloneChanged  = gitChangedPaths
)

// gitChangedPaths is the ONE reader of a checkout's changed set: `git status --porcelain`, with quoting
// off so a path holding a space or a non-ASCII byte is printed the way it is on disk rather than escaped
// into a string a reader cannot match against the tree.
func gitChangedPaths(root string) ([]string, error) {
	out, err := exec.Command("git", "-c", "core.quotePath=false", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %v", root, err)
	}
	return parseChangedPaths(string(out)), nil
}

// parseChangedPaths is the porcelain shape: `XY <path>`, where XY is the two status letters, and
// `XY <from> -> <to>` for a rename or a copy - the name that exists NOW is the one that matters, because
// that is the path a reader will find in the tree. A line too short to carry a status and a path is
// skipped rather than guessed at.
func parseChangedPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if arrow := strings.LastIndex(path, " -> "); arrow >= 0 {
			path = strings.TrimSpace(path[arrow+4:])
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}
