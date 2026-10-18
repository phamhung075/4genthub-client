package clientlifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientguard"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// writeHookFile and mkdirHookDir are this file's own helpers: one case needs a real hooks folder on disk,
// and the package has no shared writer to borrow.
func writeHookFile(t *testing.T, path, content string) {
	t.Helper()
	mkdirHookDir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mkdirHookDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// initCheckout makes a real repository with one commit, so git itself names its hooks folder.
func initCheckout(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "room/writer"},
		{"config", "user.email", "writer@room.example"},
	} {
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		command.Env = os.Environ()
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// The guard `guard install` writes is the guard this row sees: one marker, one folder answer, so an installed
// guard can never be reported absent by the report that exists to notice a switched-off guard. The case uses
// the REAL installer and the REAL git answers, with only the checkout name stubbed.
func TestTheHooksRowSeesTheGuardTheInstallerWrites(t *testing.T) {
	checkout := t.TempDir()
	initCheckout(t, checkout)
	if _, err := clientguard.Install(checkout, "/opt/4genteam", io.Discard); err != nil {
		t.Fatalf("guard install: %v", err)
	}
	root := hooksMainRoot
	hooksMainRoot = func(string) (string, error) { return checkout, nil }
	t.Cleanup(func() { hooksMainRoot = root })

	row := hooksRowOf()

	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q: the row did not see the guard the installer wrote (guards %v, note %q)",
			row.Row.State, clientservices.Running, row.Guards, row.Note)
	}
	if strings.Join(row.Guards, ",") != "pre-commit,pre-push" {
		t.Errorf("guards = %v, want the two hooks the installer writes", row.Guards)
	}
}

// stubHooksRowSeams replaces the checkout resolution, git's folder answer, the folder read and the
// configuration read, so a case drives every outcome without a repository on the machine.
func stubHooksRowSeams(t *testing.T, folder string, guards []string, guardsErr error, configured bool) {
	t.Helper()
	root, folderFn, guardsFn, configuredFn := hooksMainRoot, hooksFolder, hooksGuards, hooksConfigured
	hooksMainRoot = func(string) (string, error) { return "/main", nil }
	hooksFolder = func(got string) (string, error) {
		if got != "/main" {
			t.Errorf("the row asked git about %q, want the checkout it resolved", got)
		}
		return folder, nil
	}
	hooksGuards = func(got string) ([]string, error) {
		if got != folder {
			t.Errorf("the row read %q, want the folder git named", got)
		}
		return guards, guardsErr
	}
	hooksConfigured = func(got string) bool {
		if got != "/main" {
			t.Errorf("the row read the configuration of %q, want the checkout it resolved", got)
		}
		return configured
	}
	t.Cleanup(func() {
		hooksMainRoot, hooksFolder, hooksGuards, hooksConfigured = root, folderFn, guardsFn, configuredFn
	})
}

// A guard that is installed is the healthy row, and it still reports the folder git uses: a reader who has
// to check a switched-off guard needs to know where git is looking.
func TestTheHooksRowNamesTheFolderAndTheGuard(t *testing.T) {
	stubHooksRowSeams(t, "/repo/.git/hooks", []string{"pre-commit", "pre-push"}, nil, true)
	row := hooksRowOf()
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.Running)
	}
	if row.Row.Rig != "" {
		t.Errorf("rig = %q, want the machine's row to carry no rig", row.Row.Rig)
	}
	if len(hooksReasonLines(row)) != 0 {
		t.Errorf("reasons = %v, want none while the guard is in force", hooksReasonLines(row))
	}
	lines := strings.Join(hooksLines(row), "\n")
	if !strings.Contains(lines, "/repo/.git/hooks") {
		t.Errorf("lines = %q, want the folder git uses", lines)
	}
	for _, guard := range []string{"pre-commit", "pre-push"} {
		if !strings.Contains(lines, guard) {
			t.Errorf("lines = %q, want it to name the guard file %q", lines, guard)
		}
	}
}

// The row's ONE failure (4.4's fifth risk): the checkout names a hooks folder and no guard is in it, which
// is a guard that was installed and has been switched off.
func TestASwitchedOffGuardIsTheFailingRow(t *testing.T) {
	stubHooksRowSeams(t, "/repo/hooks", nil, nil, true)
	row := hooksRowOf()
	if row.Row.State != clientservices.GuardAbsent {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.GuardAbsent)
	}
	reasons := hooksReasonLines(row)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "/repo/hooks") || !strings.Contains(reasons[0], "switched off") {
		t.Errorf("reasons = %v, want one line naming the folder and the switched-off guard", reasons)
	}
	if row.Note != "" {
		t.Errorf("note = %q, want none: a switch-off is a failure, not an unread input", row.Note)
	}
}

// A guard that was never installed is a NOTE, not a failure: `up` waits for these rows, and a machine where
// item 2 has not run yet must not refuse for a reason no reader can act on.
func TestAGuardThatWasNeverInstalledIsANote(t *testing.T) {
	stubHooksRowSeams(t, "/repo/.git/hooks", nil, nil, false)
	row := hooksRowOf()
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q: an uninstalled guard must not refuse", row.Row.State, clientservices.Running)
	}
	if !strings.Contains(row.Note, "/repo/.git/hooks") || !strings.Contains(row.Note, "not installed") {
		t.Errorf("note = %q, want it to name the folder and say the guard is not installed", row.Note)
	}
	if !strings.Contains(strings.Join(hooksLines(row), "\n"), "not installed") {
		t.Errorf("lines = %v, want the note among them", hooksLines(row))
	}
}

// An input the client could not read is a note too, like the clone and worktrees rows.
func TestAnUnreadableHooksFolderIsANote(t *testing.T) {
	stubHooksRowSeams(t, "/repo/hooks", nil, errors.New("cannot read the hooks folder /repo/hooks: permission denied"), true)
	row := hooksRowOf()
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want running: a row that could not look must not refuse", row.Row.State)
	}
	if !strings.Contains(row.Note, "not observed") || !strings.Contains(row.Note, "permission denied") {
		t.Errorf("note = %q, want the folder read's own refusal", row.Note)
	}
}

// guardsUnder reads the folder: only the files carrying the client's marker count as the guard, and a folder
// git names but that does not exist holds none.
func TestGuardsUnderFindsOnlyTheMarkedFiles(t *testing.T) {
	folder := t.TempDir()
	writeHookFile(t, filepath.Join(folder, "pre-push"), "#!/bin/sh\n# "+clientservices.GuardMarker+"\nexit 0\n")
	writeHookFile(t, filepath.Join(folder, "pre-commit"), "#!/bin/sh\n# somebody else's hook\nexit 0\n")
	writeHookFile(t, filepath.Join(folder, "README"), "notes\n")
	mkdirHookDir(t, filepath.Join(folder, "pre-rebase.d"))

	guards, err := guardsUnder(folder)
	if err != nil {
		t.Fatalf("guardsUnder: %v", err)
	}
	if len(guards) != 1 || guards[0] != "pre-push" {
		t.Errorf("guards = %v, want exactly the marked file pre-push", guards)
	}

	missing, err := guardsUnder(filepath.Join(folder, "absent"))
	if err != nil || len(missing) != 0 {
		t.Errorf("guardsUnder(absent) = %v, %v; want no guard and no error", missing, err)
	}
}

// The wiring: doctor prints the row and the folder beside it on both surfaces, and refuses while the guard
// is switched off.
func TestDoctorPrintsTheHooksRowAndItsFolder(t *testing.T) {
	rows, worktrees, clones, hooks, orphans := rowsFor, worktreesRowFor, cloneRowFor, hooksRowFor, orphanRowsFor
	rowsFor = func(string, string) []clientservices.Health { return nil }
	worktreesRowFor = func(string, string) []worktreesRow { return nil }
	cloneRowFor = func(string) []cloneRow { return nil }
	orphanRowsFor = func(string) []orphanRow { return nil }
	hooksRowFor = func(string) []hooksRow {
		return []hooksRow{{
			Row: clientservices.Health{
				Safeguard: clientservices.Hooks,
				State:     clientservices.GuardAbsent,
				AgeS:      clientservices.UnknownAge,
			},
			Folder: "/repo/hooks",
		}}
	}
	t.Cleanup(func() {
		rowsFor, worktreesRowFor, cloneRowFor, hooksRowFor, orphanRowsFor = rows, worktrees, clones, hooks, orphans
	})

	var stdout, stderr bytes.Buffer
	if code := runDoctor("demo", t.TempDir(), false, &stdout, &stderr); code == 0 {
		t.Error("doctor exited 0 although the guard is switched off")
	}
	for what, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if !strings.Contains(got, "/repo/hooks") {
			t.Errorf("%s does not name the hooks folder:\n%s", what, got)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := runDoctor("demo", t.TempDir(), true, &stdout, &stderr); code == 0 {
		t.Error("doctor --json exited 0 although the guard is switched off")
	}
	var decoded []clientservices.Health
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("--json is not the rows: %v", err)
	}
	found := false
	for _, row := range decoded {
		if row.Safeguard != clientservices.Hooks {
			continue
		}
		found = true
		if row.State != clientservices.GuardAbsent {
			t.Errorf("the JSON row's state = %q, want %q", row.State, clientservices.GuardAbsent)
		}
	}
	if !found {
		t.Errorf("--json rows do not carry the hooks row:\n%s", stdout.String())
	}
}
