// The `worktrees` row of `doctor`: where each seat's own worktree is, and whether it is the one this
// client derives (ai_docs/core-architecture/seat-worktrees.md).
//
// IT CARRIES ITS REASONS BESIDE THE ROW, like the seats row and the orphan row, and for the same reason:
// a Health row's field set is 7.2's exactly and has no place for them, and a row that says the worktrees
// are wrong without naming one is a row a reader cannot act on.
//
// MISSING AND FOREIGN ARE DETAILS, NEVER FAILURES. `up` creates what is missing before it launches a
// seat, so a room between its first `up` and the worktrees step is not unhealthy; and a foreign entry is
// `worktree prune`'s business, not a room's health - the ruling says so in one line. WRONG is the
// failure: a derived path that EXISTS and is not the worktree this ruling derives, which is the state
// that would put a seat in the wrong tree.
package clientlifecycle

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientworktree"
)

// worktreesRow is the row plus what produced it.
type worktreesRow struct {
	Row     clientservices.Health
	Missing []string
	Wrong   []string
	Foreign []string
	// Note is the input this client could not read, in the seats row's shape: a row that could not be
	// looked at must not read as a row that is right.
	Note string
}

// worktreeReasonLines are the lines a refusal must carry: what is wrong, and what could not be read.
func worktreeReasonLines(row worktreesRow) []string {
	lines := make([]string, 0, len(row.Wrong)+1)
	for _, path := range row.Wrong {
		lines = append(lines, row.Row.Safeguard+": "+path+" is not the seat worktree this client derives")
	}
	if row.Note != "" {
		lines = append(lines, row.Note)
	}
	return lines
}

// worktreeDetailLines are the counts the report shows: the worktrees not created yet, and the entries
// that belong to no room of this client's.
func worktreeDetailLines(row worktreesRow) []string {
	lines := make([]string, 0, 2)
	if len(row.Missing) > 0 {
		lines = append(lines, fmt.Sprintf("%d seat worktree(s) not created yet: %s",
			len(row.Missing), strings.Join(row.Missing, ", ")))
	}
	if len(row.Foreign) > 0 {
		lines = append(lines, fmt.Sprintf("%d foreign worktree(s): %s",
			len(row.Foreign), strings.Join(row.Foreign, ", ")))
	}
	return lines
}

// worktreeLines is one worktrees row as the report prints it: the reasons first, then the details.
func worktreeLines(row worktreesRow) []string {
	return append(worktreeReasonLines(row), worktreeDetailLines(row)...)
}

// worktreesRowFor is the worktrees-row computation as a seam, beside rowsFor, seatsRowFor and
// orphanRowsFor: a case drives the row without a room directory or a git repository on the machine.
var worktreesRowFor = worktreesRows

// worktreesRows computes the `worktrees` row of every room the report covers.
func worktreesRows(rig, root string) []worktreesRow {
	// The rigs a report covers are agent.json's list, the same list the safeguard rows read (7.9), so
	// the two row families cannot disagree about which rooms exist. A failed read leaves it empty, as a
	// machine whose agent never ran does; the row then says so in its note rather than inventing a room.
	state, _ := clientservices.ReadAgentState()
	rigs := reportedRigs(state.Rigs, rig)
	rows := make([]worktreesRow, 0, len(rigs))
	for _, name := range rigs {
		rows = append(rows, worktreesRowOf(name, root))
	}
	return rows
}

// worktreesRowOf is one room's row. Every step that cannot read its input produces the note rather than
// a state: the row answers "are the seats in their own worktrees", and a client that could not look has
// no answer to give.
func worktreesRowOf(name, root string) worktreesRow {
	row := worktreesRow{Row: clientservices.Health{
		Rig:       name,
		Safeguard: clientservices.Worktrees,
		State:     clientservices.Running,
		AgeS:      clientservices.UnknownAge,
	}}
	// The seats are the room's OWN record - spec.json, the file the runtime wrote from the definition
	// and every start reads - so the row answers about the room that exists, not about a definition that
	// may since have changed. A room with no record has nothing to report, which is its note.
	seats, err := clientservices.LiveSeats(root, name)
	if err != nil {
		row.Note = clientservices.Worktrees + ": not observed: " + err.Error()
		return row
	}
	roomDir := ""
	if root != "" {
		// `doctor --state-root DIR` names the tree the rows are read from, so the room's own directory
		// is read there too: the same call must not report one tree and check another.
		roomDir = filepath.Join(root, "rooms", name)
	} else {
		roomDir, err = worktreeRoomDir(name)
	}
	if err != nil {
		row.Note = clientservices.Worktrees + ": not observed: " + err.Error()
		return row
	}
	mainRoot, err := worktreeMainRoot("")
	if err != nil {
		row.Note = clientservices.Worktrees + ": not observed: " + err.Error()
		return row
	}
	verdict, err := worktreeCheck(mainRoot, roomDir, name, seats)
	if err != nil {
		row.Note = clientservices.Worktrees + ": not observed: " + err.Error()
		return row
	}
	row.Missing, row.Wrong, row.Foreign = verdict.Missing, verdict.Wrong, verdict.Foreign
	if len(verdict.Wrong) > 0 {
		row.Row.State = clientservices.WorktreesWrong
	}
	return row
}

// worktreeRoomDir is a room's own directory, the one `rigLaunchSpec` calls roomDir, and it is
// clientworktree.RoomDir: the ONE resolution, the same function the two worktree verbs call, so `up`,
// `doctor` and the verbs cannot disagree about where a room is. It stays a variable so a case drives the
// row without the machine's store; it takes its value at package init, so a case that redirects the row
// does not redirect the verbs with it.
var worktreeRoomDir = clientworktree.RoomDir

// worktreeMainRoot and worktreeCheck are the ruling's reader and Check as seams: a case drives the row
// without a git repository on the machine, and the row's two failure modes - a wrong path and an input
// that could not be read - are both reachable from a test.
var (
	worktreeMainRoot = clientworktree.MainRoot
	worktreeCheck    = clientworktree.Check
	// worktreeSeatRoot names the repository the room's own seat directories belong to. It is the seam
	// `up` reads the root through, so a case can make the working directory name one repository while
	// the definition names another - the measured defect - without building two repositories.
	worktreeSeatRoot = clientworktree.SeatProjectRoot
	// worktreeEnsure is the step `up` runs, a seam for the same reason: a case asserts what `up` derives
	// and what it does with a refusal without creating worktrees on the machine.
	worktreeEnsure = clientworktree.Ensure
)
