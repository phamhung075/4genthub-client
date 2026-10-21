// `doctor`'s worktrees row: the ruling's Check as a row, its ONE failure, and the two things that are
// details rather than failures. The row's seams are replaced here - the room directory, the main root
// and the Check itself - so no case needs a git repository or the machine's store.
package clientlifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientworktree"
)

// writeDemoRoom writes the live record the row reads for room "demo" - the room's own spec.json, the
// file the runtime wrote from the definition - and returns the state root it lives under: the test
// drives the real resolution rather than replacing it.
func writeDemoRoom(t *testing.T, seats string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "rooms", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"room":"demo","seats":[` + seats + `]}`
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func stubWorktreeRowSeams(t *testing.T, verdict clientworktree.CheckVerdict, err error) {
	t.Helper()
	mainRoot, check, room, seatRoot := worktreeMainRoot, worktreeCheck, worktreeRoomDir, worktreeSeatRoot
	worktreeMainRoot = func(string) (string, error) { return "/main", nil }
	worktreeCheck = func(string, string, string, []clientservices.SeatType) (clientworktree.CheckVerdict, error) {
		return verdict, err
	}
	// The seam mirrors production: the room directory is the given root plus rooms/<room>, so a case
	// that writes a room under its own root reads THAT room.
	worktreeRoomDir = func(root, room string) (string, error) { return filepath.Join(root, "rooms", room), nil }
	// No derived path answers for a repository unless a case says so, which is the state of a room whose
	// seats are not started yet: the working directory's root is what the row falls back to.
	worktreeSeatRoot = func([]string) (string, bool, error) { return "", false, nil }
	t.Cleanup(func() {
		worktreeMainRoot, worktreeCheck, worktreeRoomDir, worktreeSeatRoot = mainRoot, check, room, seatRoot
	})
}

// A derived path that exists and is not the derived worktree is the row's ONE failure: it is the state
// that would leave a seat in the wrong tree. A path not created yet and a foreign entry are details -
// `up` creates the first before a seat launches, and the ruling says the second is not a failure.
func TestWorktreesRowFailsOnlyOnAWrongDerivedPath(t *testing.T) {
	root := writeDemoRoom(t, `{"seat_key":"lead","seat_type":"lead"}`)
	stubWorktreeRowSeams(t, clientworktree.CheckVerdict{
		Missing: []string{"/room/wt/lead"},
		Wrong:   []string{"/room/wt/integration"},
		Foreign: []string{"/tmp/wt-somebody-else"},
	}, nil)

	row := worktreesRowOf("demo", root)
	if row.Row.State != clientservices.WorktreesWrong {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.WorktreesWrong)
	}
	if row.Row.Safeguard != clientservices.Worktrees || row.Row.Room != "demo" {
		t.Errorf("row = %+v, want the worktrees row for demo", row.Row)
	}
	if got := clientservices.NotRunning([]clientservices.Health{row.Row}); len(got) != 1 {
		t.Errorf("NotRunning counted %d rows, want 1: a wrong derived path must fail doctor", len(got))
	}
	reasons := strings.Join(worktreeReasonLines(row), "\n")
	if !strings.Contains(reasons, "/room/wt/integration") {
		t.Errorf("the reasons do not name the wrong path:\n%s", reasons)
	}
	details := strings.Join(worktreeDetailLines(row), "\n")
	if !strings.Contains(details, "/room/wt/lead") || !strings.Contains(details, "/tmp/wt-somebody-else") {
		t.Errorf("the details do not name the missing and foreign entries:\n%s", details)
	}
}

// Missing and foreign alone leave the row running: doctor does not fail a room for a worktree that `up`
// has not created yet, nor for one that belongs to no room of this client's.
func TestWorktreesRowIsRunningWithOnlyDetails(t *testing.T) {
	root := writeDemoRoom(t, `{"seat_key":"lead","seat_type":"lead"}`)
	stubWorktreeRowSeams(t, clientworktree.CheckVerdict{
		Missing: []string{"/room/wt/lead"},
		Foreign: []string{"/tmp/wt-x"},
	}, nil)

	row := worktreesRowOf("demo", root)
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.Running)
	}
	if got := clientservices.NotRunning([]clientservices.Health{row.Row}); len(got) != 0 {
		t.Errorf("NotRunning counted %d rows, want 0", len(got))
	}
	if len(worktreeDetailLines(row)) != 2 {
		t.Errorf("details = %v, want one line for the missing and one for the foreign entry", worktreeDetailLines(row))
	}
}

// An input this client could not read is a NOTE, not a failure and not a pass: a reader must be able to
// tell "the worktrees are where they belong" from "this client could not look".
func TestWorktreesRowNotesAnUnreadableRoomRecord(t *testing.T) {
	root := t.TempDir() // no rooms/demo/spec.json under it
	stubWorktreeRowSeams(t, clientworktree.CheckVerdict{}, nil)

	row := worktreesRowOf("demo", root)
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q, want %q", row.Row.State, clientservices.Running)
	}
	if row.Note == "" {
		t.Error("an unreadable room record produced no note")
	}
	if !strings.Contains(row.Note, clientservices.Worktrees+": not observed") {
		t.Errorf("note = %q, want it to say the row was not observed", row.Note)
	}
}

// The wiring: doctor prints the row, its reasons beside it, and the same reasons on stderr when the row
// is down. The three computations are seams, so no case needs a daemon, a room or a repository.
func TestDoctorPrintsTheWorktreesRowAndItsReasons(t *testing.T) {
	rows, worktrees, orphans := rowsFor, worktreesRowFor, orphanRowsFor
	rowsFor = func(string, string) ([]clientservices.Health, error) { return nil, nil }
	orphanRowsFor = func(string) []orphanRow { return nil }
	worktreesRowFor = func(string, string) []worktreesRow {
		return []worktreesRow{{
			Row: clientservices.Health{
				Room: "demo", Safeguard: clientservices.Worktrees,
				State: clientservices.WorktreesWrong, AgeS: clientservices.UnknownAge,
			},
			Wrong: []string{"/room/wt/go-dev"},
		}}
	}
	t.Cleanup(func() { rowsFor, worktreesRowFor, orphanRowsFor = rows, worktrees, orphans })

	var stdout, stderr bytes.Buffer
	if code := runDoctor("demo", "", false, &stdout, &stderr); code == 0 {
		t.Error("doctor exited 0 although the worktrees row is wrong")
	}
	if !strings.Contains(stdout.String(), clientservices.Worktrees) {
		t.Errorf("stdout does not carry the row:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "/room/wt/go-dev") || !strings.Contains(stderr.String(), "/room/wt/go-dev") {
		t.Errorf("the wrong path is not named on both surfaces:\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runDoctor("demo", "", true, &stdout, &stderr); code == 0 {
		t.Error("doctor --json exited 0 although the worktrees row is wrong")
	}
	var decoded []clientservices.Health
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("--json is not the rows: %v", err)
	}
	found := false
	for _, row := range decoded {
		if row.Safeguard == clientservices.Worktrees && row.State == clientservices.WorktreesWrong {
			found = true
		}
	}
	if !found {
		t.Errorf("--json rows do not carry the worktrees row:\n%s", stdout.String())
	}
}

// THE MEASURED DEFECT IN THE DIAGNOSTIC SURFACE: the row judged the room's paths against the repository
// the command was run in, so `doctor` from a client worktree called the room's own six worktrees Wrong.
// A derived path that exists names the repository it belongs to, so the row judges against that one.
func TestWorktreesRowJudgesAgainstTheRepositoryItsPathsBelongTo(t *testing.T) {
	root := writeDemoRoom(t, `{"seat_key":"lead","seat_type":"lead"}`)
	stubWorktreeRowSeams(t, clientworktree.CheckVerdict{}, nil)
	var gotRoot, gotPaths string
	worktreeSeatRoot = func(paths []string) (string, bool, error) {
		gotPaths = strings.Join(paths, ",")
		return "/room-repo", true, nil
	}
	worktreeCheck = func(mainRoot, _, _ string, _ []clientservices.SeatType) (clientworktree.CheckVerdict, error) {
		gotRoot = mainRoot
		return clientworktree.CheckVerdict{}, nil
	}

	worktreesRowOf("demo", root)
	if gotRoot != "/room-repo" {
		t.Errorf("the row checked against %q, want the paths' own repository %q", gotRoot, "/room-repo")
	}
	if want := filepath.Join(root, "rooms", "demo", "wt", "lead"); !strings.Contains(gotPaths, want) {
		t.Errorf("the resolver was handed %q, want the derived path of the room's lead (%q)", gotPaths, want)
	}
}

// Paths that answer nothing - a room never started - keep the working directory's root, as before: the
// fix must not invent a root for a room with nothing derived yet.
func TestWorktreesRowKeepsTheWorkingDirectorysRootWhenNoPathAnswers(t *testing.T) {
	root := writeDemoRoom(t, `{"seat_key":"lead","seat_type":"lead"}`)
	stubWorktreeRowSeams(t, clientworktree.CheckVerdict{}, nil)
	var gotRoot string
	worktreeCheck = func(mainRoot, _, _ string, _ []clientservices.SeatType) (clientworktree.CheckVerdict, error) {
		gotRoot = mainRoot
		return clientworktree.CheckVerdict{}, nil
	}

	worktreesRowOf("demo", root)
	if gotRoot != "/main" {
		t.Errorf("the row checked against %q, want the working directory's %q", gotRoot, "/main")
	}
}

// The room's own worktrees spanning two repositories is a state the row cannot judge from one root, and
// it is the state the row exists to make visible: the note carries both roots rather than a verdict.
func TestWorktreesRowSaysItCouldNotResolveWhenThePathsSpanTwoRepositories(t *testing.T) {
	root := writeDemoRoom(t, `{"seat_key":"lead","seat_type":"lead"}`)
	stubWorktreeRowSeams(t, clientworktree.CheckVerdict{}, nil)
	worktreeSeatRoot = func([]string) (string, bool, error) {
		return "", false, errors.New("the seat directories belong to /one and /two")
	}
	checked := false
	worktreeCheck = func(string, string, string, []clientservices.SeatType) (clientworktree.CheckVerdict, error) {
		checked = true
		return clientworktree.CheckVerdict{}, nil
	}

	row := worktreesRowOf("demo", root)
	if checked {
		t.Error("the row judged the paths from a root it could not resolve")
	}
	if !strings.Contains(row.Note, "/one and /two") {
		t.Errorf("the note does not name the two repositories: %q", row.Note)
	}
	if row.Row.State != clientservices.Running {
		t.Errorf("state = %q; a row that could not look reports no verdict", row.Row.State)
	}
}
