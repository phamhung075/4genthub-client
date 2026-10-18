package clientlifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// stubCloneRowSeams replaces the checkout resolution and the git read, so a case drives the row's three
// outcomes - clean, dirty and an input that could not be read - without a repository on the machine.
func stubCloneRowSeams(t *testing.T, changed []string, err error) {
	t.Helper()
	root, read := cloneMainRoot, cloneChanged
	cloneMainRoot = func(string) (string, error) { return "/main", nil }
	cloneChanged = func(got string) ([]string, error) {
		if got != "/main" {
			t.Errorf("the row read %q, want the checkout it resolved", got)
		}
		return changed, err
	}
	t.Cleanup(func() { cloneMainRoot, cloneChanged = root, read })
}

// The row's ONE failure: a path changed in the shared checkout belongs to no seat's worktree, and the row
// names the path - a row that says the checkout is dirty without naming one cannot be acted on.
func TestTheCloneRowNamesEveryChangedPathAsUnowned(t *testing.T) {
	stubCloneRowSeams(t, []string{"ai_docs/index.json", "scratch/notes.md"}, nil)
	row := cloneRowOf()
	if row.Row.State != clientservices.CloneDirty {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.CloneDirty)
	}
	if row.Row.Rig != "" {
		t.Errorf("rig = %q, want the machine's row to carry no rig", row.Row.Rig)
	}
	lines := cloneLines(row)
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want one per changed path", lines)
	}
	for i, want := range []string{"ai_docs/index.json", "scratch/notes.md"} {
		if !strings.Contains(lines[i], want) || !strings.Contains(lines[i], "no seat's worktree owns it") {
			t.Errorf("line %d = %q, want it to name %q as unowned", i, lines[i], want)
		}
	}
}

// A clean shared checkout is the healthy row: nothing to name, so no line at all.
func TestACleanSharedCheckoutIsTheRunningRow(t *testing.T) {
	stubCloneRowSeams(t, nil, nil)
	row := cloneRowOf()
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.Running)
	}
	if lines := cloneLines(row); len(lines) != 0 {
		t.Errorf("lines = %v, want none for a clean checkout", lines)
	}
}

// An input the client could not read is a NOTE, never a failure: the row answers a question about the
// checkout, and a client that could not look has no answer to give.
func TestAnUnreadableCheckoutIsANoteAndNeverAFailure(t *testing.T) {
	stubCloneRowSeams(t, nil, errors.New("git status in /main: exit status 128"))
	row := cloneRowOf()
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want running: a row that could not look must not refuse", row.Row.State)
	}
	lines := cloneLines(row)
	if len(lines) != 1 || !strings.Contains(lines[0], "not observed") {
		t.Errorf("lines = %v, want one note saying the row was not observed", lines)
	}
}

// A checkout this client cannot even resolve is the same note, with MainRoot's own refusal in it.
func TestAnUnresolvableCheckoutIsANote(t *testing.T) {
	root := cloneMainRoot
	cloneMainRoot = func(string) (string, error) { return "", errors.New("no checkout: pass --main") }
	t.Cleanup(func() { cloneMainRoot = root })

	row := cloneRowOf()
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want running", row.Row.State)
	}
	if !strings.Contains(row.Note, "not observed") || !strings.Contains(row.Note, "pass --main") {
		t.Errorf("note = %q, want the resolution's own refusal", row.Note)
	}
}

// parseChangedPaths reads the porcelain shape: a modified path, an untracked one, a rename (the name that
// exists NOW) and a staged one - and skips the lines that carry no path.
func TestParseChangedPathsReadsThePorcelainStates(t *testing.T) {
	out := " M ai_docs/index.json\n?? scratch/notes.md\nR  old.txt -> new.txt\nA  staged.md\n\n  \n"
	got := parseChangedPaths(out)
	want := []string{"ai_docs/index.json", "scratch/notes.md", "new.txt", "staged.md"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The wiring: doctor prints the row and the paths beside it, and `--json` carries the clone row with its
// state. (The reasons field on the JSON rows is batch 5's change, on its own lane; main's --json is the rows.)
func TestDoctorPrintsTheCloneRowAndItsPaths(t *testing.T) {
	rows, worktrees, clones, orphans, hooks := rowsFor, worktreesRowFor, cloneRowFor, orphanRowsFor, hooksRowFor
	rowsFor = func(string, string) []clientservices.Health { return nil }
	worktreesRowFor = func(string, string) []worktreesRow { return nil }
	orphanRowsFor = func(string) []orphanRow { return nil }
	hooksRowFor = func(string) []hooksRow { return nil }
	cloneRowFor = func(string) []cloneRow {
		return []cloneRow{{
			Row: clientservices.Health{
				Safeguard: clientservices.Clone,
				State:     clientservices.CloneDirty,
				AgeS:      clientservices.UnknownAge,
			},
			Changed: []string{"ai_docs/index.json"},
		}}
	}
	t.Cleanup(func() {
		rowsFor, worktreesRowFor, cloneRowFor, orphanRowsFor, hooksRowFor = rows, worktrees, clones, orphans, hooks
	})

	var stdout, stderr bytes.Buffer
	if code := runDoctor("demo", t.TempDir(), false, &stdout, &stderr); code == 0 {
		t.Error("doctor exited 0 although the shared checkout is dirty")
	}
	for what, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if !strings.Contains(got, "ai_docs/index.json") {
			t.Errorf("%s does not name the changed path:\n%s", what, got)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := runDoctor("demo", t.TempDir(), true, &stdout, &stderr); code == 0 {
		t.Error("doctor --json exited 0 although the shared checkout is dirty")
	}
	var decoded []clientservices.Health
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("--json is not the rows: %v", err)
	}
	found := false
	for _, row := range decoded {
		if row.Safeguard != clientservices.Clone {
			continue
		}
		found = true
		if row.State != clientservices.CloneDirty {
			t.Errorf("the JSON clone row's state = %q, want %q", row.State, clientservices.CloneDirty)
		}
	}
	if !found {
		t.Errorf("--json rows do not carry the clone row:\n%s", stdout.String())
	}
}
