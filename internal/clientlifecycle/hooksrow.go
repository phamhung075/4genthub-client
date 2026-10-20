// The `hooks` row of `doctor`: the hooks folder GIT actually runs for the checkout, and whether the
// client's git guard is installed in it.
//
// 4.4's fifth risk is the reason the row exists: a hook can itself be switched off - `git push --no-verify`,
// or a `core.hooksPath` pointing at another folder - so the guard's presence must be READ rather than
// assumed. Item 2 installs the guard; this row is what turns "the guard is not in force here" into a line a
// reader can see.
//
// THE FOLDER IS GIT'S OWN ANSWER: `git rev-parse --git-path hooks` honors `core.hooksPath` and otherwise
// names the repository's own hooks folder, so the row reads what git will run instead of guessing a path.
// The checkout is the one `up` and the worktrees row already resolve (clientworktree.MainRoot), and like
// the clone row this one is the MACHINE's: one checkout holds every room's worktrees, so it carries no room.
//
// A GUARD THAT WAS NEVER INSTALLED IS A NOTE, NOT A FAILURE. 7.5 makes `doctor` refuse while any row is not
// running, and a machine where the guard has not been installed yet would refuse for a reason no reader can
// act on - item 2's own task. A guard the checkout NAMES by setting `core.hooksPath` and then does not have
// is different: that is a guard which was installed and is now switched off, and that IS the failure.
//
// AN INPUT THAT COULD NOT BE READ IS A NOTE, never a failure, like the clone and worktrees rows.
package clientlifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientworktree"
)

// hooksRow is the row plus what produced it.
type hooksRow struct {
	Row clientservices.Health
	// Folder is the hooks folder git resolves, reported whether or not a guard is in it: it is the fact a
	// reader needs to see WHY a guard is not in force, and the whole point of 4.4's fifth risk.
	Folder string
	// Guards are the hook files under Folder that carry the client's marker, by git's own hook names.
	Guards []string
	// Note names an input this client could not read, or a guard that was never installed. Never a
	// failure.
	Note string
}

// hooksReasonLines are the lines a refusal must carry: why the guard is not in force.
func hooksReasonLines(row hooksRow) []string {
	if row.Row.State != clientservices.GuardAbsent {
		return nil
	}
	return []string{clientservices.Hooks + ": core.hooksPath names " + row.Folder +
		", and no hook there carries the git guard's marker - the guard is switched off"}
}

// hooksDetailLines are what the row reports when it has nothing to refuse: the folder git uses, and the
// guard files it found in it. They are details rather than reasons because both are readings, not failures.
func hooksDetailLines(row hooksRow) []string {
	line := clientservices.Hooks + ": git's hooks folder is " + row.Folder
	if len(row.Guards) == 0 {
		return []string{line}
	}
	return []string{line, clientservices.Hooks + ": the git guard is present: " + strings.Join(row.Guards, ", ")}
}

// hooksLines is one hooks row as the report prints it: the reasons first, then the details, then the note
// about an input that could not be read. The human report prints these; `--json` carries the same lines as
// the row's reasons, from this ONE function.
func hooksLines(row hooksRow) []string {
	lines := hooksReasonLines(row)
	lines = append(lines, hooksDetailLines(row)...)
	if row.Note != "" {
		lines = append(lines, row.Note)
	}
	return lines
}

// hooksRowFor is the hooks-row computation as a seam, beside cloneRowFor and the rows above it: a case
// drives all three outcomes - a guard present, a guard switched off, and a checkout with no guard at all -
// without a repository on the machine.
var hooksRowFor = hooksRows

// hooksRows is the row the report covers. The argument is the room the caller named and is deliberately
// unused: the hooks folder belongs to the checkout, not to a room.
func hooksRows(string) []hooksRow { return []hooksRow{hooksRowOf()} }

// hooksRowOf is the row itself.
func hooksRowOf() hooksRow {
	row := hooksRow{Row: clientservices.Health{
		Safeguard: clientservices.Hooks,
		State:     clientservices.Running,
		AgeS:      clientservices.UnknownAge,
	}}
	root, err := hooksMainRoot("")
	if err != nil {
		row.Note = clientservices.Hooks + ": not observed: " + err.Error()
		return row
	}
	folder, err := hooksFolder(root)
	if err != nil {
		row.Note = clientservices.Hooks + ": not observed: " + err.Error()
		return row
	}
	row.Folder = folder
	guards, err := hooksGuards(folder)
	if err != nil {
		row.Note = clientservices.Hooks + ": not observed: " + err.Error()
		return row
	}
	row.Guards = guards
	if len(guards) > 0 {
		return row
	}
	if hooksConfigured(root) {
		row.Row.State = clientservices.GuardAbsent
		return row
	}
	row.Note = clientservices.Hooks + ": no hook in " + folder +
		" carries the git guard's marker and core.hooksPath is not set, so the git guard is not installed here"
	return row
}

// hooksMainRoot, hooksFolder, hooksGuards and hooksConfigured are the checkout resolution, git's own
// folder answer, the folder read and the configuration read as seams, so a case drives every outcome
// without a repository.
var (
	hooksMainRoot   = clientworktree.MainRoot
	hooksFolder     = gitHooksFolder
	hooksGuards     = guardsUnder
	hooksConfigured = gitHooksConfigured
)

// gitHooksFolder is git's own answer for the checkout. `rev-parse --git-path hooks` resolves
// core.hooksPath when the checkout sets one and otherwise names the repository's own hooks folder, which
// is exactly the question this row asks: where will git look for a hook.
func gitHooksFolder(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-path hooks in %s: %v", root, err)
	}
	folder := strings.TrimSpace(string(out))
	if folder == "" {
		return "", fmt.Errorf("git named no hooks folder for %s", root)
	}
	if !filepath.IsAbs(folder) {
		folder = filepath.Join(root, folder)
	}
	return folder, nil
}

// gitHooksConfigured reports whether the checkout SETS core.hooksPath. It is what separates a guard that
// was installed and is now switched off (a failure) from one that was never installed (a note).
func gitHooksConfigured(root string) bool {
	out, err := exec.Command("git", "-C", root, "config", "--get", "core.hooksPath").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// guardsUnder lists the hook files under a folder that carry the client's marker, in a stable order. A
// folder git names but that does not exist holds no guard, which is a reading rather than an error; a file
// this client cannot read is skipped, because a guard it cannot read is not a guard it can vouch for and
// failing the row over one unreadable file would be a refusal the reader cannot act on.
func guardsUnder(folder string) ([]string, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read the hooks folder %s: %v", folder, err)
	}
	var guards []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(folder, entry.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(body), clientservices.GuardMarker) {
			guards = append(guards, entry.Name())
		}
	}
	sort.Strings(guards)
	return guards, nil
}
