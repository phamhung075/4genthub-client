package clientlifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientworktree"
)

// overlapFixture is one room of seats whose branches answer from a table, so a case says what each seat
// changed without a repository on the machine.
type overlapFixture struct {
	deps    *overlapDeps
	sent    []string
	now     time.Time
	state   string
	roomDir string
	room    string
}

const overlapFixtureRoom = "demo"

// overlapGitAnswers is a fake git keyed by `<worktree>|<arguments>`: an absent key FAILS, so "could not read"
// is a case a test can reach rather than a path nobody exercises.
func overlapGitAnswers(answers map[string]string) func(string, ...string) (string, error) {
	return func(dir string, args ...string) (string, error) {
		key := dir + "|" + strings.Join(args, " ")
		answer, ok := answers[key]
		if !ok {
			return "", fmt.Errorf("fake git has no answer for %s in %s", strings.Join(args, " "), dir)
		}
		return answer, nil
	}
}

// overlapFactsFor fills a seat's answers the way git would: its entry in `changed` since the base, its
// `uncommitted` files, its unmerged count and when it last moved.
func overlapFactsFor(answers map[string]string, worktree, base string, changed, uncommitted []string, unmerged int, last time.Time) {
	answers[worktree+"|rev-parse --verify --quiet "+base+"^{commit}"] = "0000000000000000000000000000000000000000"
	answers[worktree+"|diff --name-only "+base+"...HEAD"] = strings.Join(changed, "\n")
	status := ""
	for _, path := range uncommitted {
		status += " M " + path + "\n"
	}
	answers[worktree+"|status --porcelain"] = status
	answers[worktree+"|rev-list --count "+base+"..HEAD"] = fmt.Sprint(unmerged)
	answers[worktree+"|log -1 --format=%ct HEAD"] = fmt.Sprint(last.Unix())
}

// overlapFixtureFor builds the detector's wiring for one room, with the base `main` and two seats in their own
// worktrees. `verdict` decides what the doctor's worktree check answers, which is what the skip list reads.
func overlapFixtureFor(t *testing.T, seats []clientservices.SeatType, answers map[string]string, verdict clientworktree.CheckVerdict) *overlapFixture {
	t.Helper()
	fixture := &overlapFixture{
		room:    overlapFixtureRoom,
		roomDir: filepath.Join(t.TempDir(), overlapFixtureRoom),
		state:   filepath.Join(t.TempDir(), "overlap.json"),
		now:     time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC),
	}
	fixture.deps = &overlapDeps{
		teamDir:  func(string) (string, error) { return t.TempDir(), nil },
		seats:    func(string) ([]clientservices.SeatType, error) { return seats, nil },
		baseName: func(string) string { return "" },
		roomDir:  func(string) (string, error) { return fixture.roomDir, nil },
		mainRoot: func(string) (string, error) { return t.TempDir(), nil },
		check: func(string, string, string, []clientservices.SeatType) (clientworktree.CheckVerdict, error) {
			return verdict, nil
		},
		git:   overlapGitAnswers(answers),
		say:   func(room, seat, text string) { fixture.sent = append(fixture.sent, seat+": "+text) },
		now:   func() time.Time { return fixture.now },
		state: func(string) (string, error) { return fixture.state, nil },
	}
	return fixture
}

func (f *overlapFixture) options() overlapOptions {
	return overlapOptions{Room: f.room, StaleAfter: overlapStaleAfter, Repeat: overlapRepeat, Now: f.now}
}

// Two seats changing one file is the whole point: both of them and the lead are told, the message names the
// file, each branch, that branch's unmerged commits and when it last moved, and it says nothing is blocked.
func TestOverlapReportsAFileTwoSeatsChanged(t *testing.T) {
	seats := []clientservices.SeatType{{Key: "go-dev", Type: "developer"}, {Key: "fe-dev", Type: "writer"}}
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	share := "src/shared.go"
	// The worktree paths come from the same derivation the scan calls, so the fake git is keyed by it.
	goDevWt, goDevBranch := overlapFixtureWorktree(roomDir, "go-dev", "developer")
	feDevWt, feDevBranch := overlapFixtureWorktree(roomDir, "fe-dev", "writer")
	moved := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	overlapFactsFor(answers, goDevWt, "main", []string{share, "src/go-only.go"}, nil, 3, moved)
	overlapFactsFor(answers, feDevWt, "main", []string{share}, nil, 1, moved)
	fixture := overlapFixtureFor(t, seats, answers, clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Hits) != 1 {
		t.Fatalf("hits = %d, want one (%v)", len(report.Hits), report.Notes)
	}
	hit := report.Hits[0]
	if hit.File != share {
		t.Errorf("hit file = %q, want %q", hit.File, share)
	}
	if len(hit.Seats) != 2 || hit.Seats[0].Seat != "fe-dev" || hit.Seats[1].Seat != "go-dev" {
		t.Errorf("hit seats = %v, want fe-dev and go-dev, sorted", hit.Seats)
	}
	overlapNotify(fixture.deps, fixture.options(), &report)
	text := strings.Join(fixture.sent, "\n")
	for _, want := range []string{
		"Two seats are changing the same file. File: " + share + ".",
		"Seat go-dev on branch " + goDevBranch + " (3 commits not yet merged, last at 2026-10-11T09:00:00Z).",
		"Seat fe-dev on branch " + feDevBranch + " (1 commits not yet merged, last at 2026-10-11T09:00:00Z).",
		"Nothing is blocked. Each author keeps the change on its own branch; the lead decides the merge order. " +
			"Reply to the lead, not to each other.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the message does not carry %q:\n%s", want, text)
		}
	}
	for _, told := range []string{"go-dev: ", "fe-dev: ", "lead: "} {
		if !strings.Contains(text, told) {
			t.Errorf("nobody told %q:\n%s", told, text)
		}
	}
}

// A file one seat changed is not a hit, and neither is a file two seats changed in DIFFERENT worktrees when
// only one of them changed this one: the detector reports agreement, not activity.
func TestOverlapReportsNothingWhenNoFileIsShared(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	goDevWt, _ := overlapFixtureWorktree(roomDir, "go-dev", "developer")
	feDevWt, _ := overlapFixtureWorktree(roomDir, "fe-dev", "writer")
	moved := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	overlapFactsFor(answers, goDevWt, "main", []string{"a.go", "b.go"}, nil, 1, moved)
	overlapFactsFor(answers, feDevWt, "main", []string{"c.go"}, nil, 1, moved)
	fixture := overlapFixtureFor(t, []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"}, {Key: "fe-dev", Type: "writer"},
	}, answers, clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Hits) != 0 || len(report.Stale) != 0 || len(report.Notes) != 0 {
		t.Fatalf("report = %+v, want nothing at all", report)
	}
}

// A file that is changed but NOT committed counts: a seat mid-edit is exactly who the other author must know
// about, and the ruling's "plus the files changed but not yet committed".
func TestOverlapCountsFilesNotYetCommitted(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	goDevWt, _ := overlapFixtureWorktree(roomDir, "go-dev", "developer")
	feDevWt, _ := overlapFixtureWorktree(roomDir, "fe-dev", "writer")
	moved := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	overlapFactsFor(answers, goDevWt, "main", []string{"done.go"}, []string{"half/edit.go"}, 1, moved)
	overlapFactsFor(answers, feDevWt, "main", []string{"half/edit.go"}, nil, 1, moved)
	fixture := overlapFixtureFor(t, []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"}, {Key: "fe-dev", Type: "writer"},
	}, answers, clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Hits) != 1 || report.Hits[0].File != "half/edit.go" {
		t.Fatalf("hits = %+v, want the uncommitted file the two seats share", report.Hits)
	}
}

// The seats the doctor does not show in their own worktree are SKIPPED and NAMED: their changes cannot be told
// apart from anyone else's, so a hit that named them would be a guess.
func TestOverlapSkipsTheSeatsTheDoctorDoesNotShow(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	goDevWt, _ := overlapFixtureWorktree(roomDir, "go-dev", "developer")
	feDevWt, _ := overlapFixtureWorktree(roomDir, "fe-dev", "writer")
	moved := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	// go-dev answers, but the doctor says its worktree is not there, so it is never read and never reported.
	overlapFactsFor(answers, goDevWt, "main", []string{"src/shared.go"}, nil, 1, moved)
	overlapFactsFor(answers, feDevWt, "main", []string{"src/shared.go"}, nil, 1, moved)
	fixture := overlapFixtureFor(t, []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"}, {Key: "fe-dev", Type: "writer"},
	}, answers, clientworktree.CheckVerdict{Missing: []string{goDevWt}})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Hits) != 0 {
		t.Errorf("hits = %+v, want none: one of the two seats is not in its own worktree", report.Hits)
	}
	joined := strings.Join(report.Skipped, ", ")
	if !strings.Contains(joined, "go-dev (its own worktree is not created yet)") {
		t.Errorf("skipped = %q, want go-dev named with the reason", joined)
	}
	if strings.Contains(joined, "fe-dev") {
		t.Errorf("skipped = %q, but fe-dev is in its own worktree", joined)
	}
}

// A reviewer is named too, and for its own reason: its worktree is detached, so it has no branch to compare.
func TestOverlapSkipsTheDetachedReviewerAndSaysWhy(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	fixture := overlapFixtureFor(t, []clientservices.SeatType{{Key: "reviewer", Type: "reviewer"}}, map[string]string{},
		clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0], "detached") {
		t.Fatalf("skipped = %q, want the reviewer named as detached", report.Skipped)
	}
	if len(report.Notes) != 0 {
		t.Errorf("notes = %q, want none: a detached worktree is normal, not an unreadable input", report.Notes)
	}
}

// A read that fails is a NOTE, never silence: a detector that could not look has no answer to give, and an
// empty report must not read as "nothing overlaps".
func TestOverlapNotesASeatItCannotRead(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	fixture := overlapFixtureFor(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}}, map[string]string{},
		clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Notes) != 1 {
		t.Fatalf("notes = %q, want the seat named with what could not be read", report.Notes)
	}
	if !strings.Contains(report.Notes[0], "go-dev") || !strings.Contains(report.Notes[0], "main") {
		t.Errorf("note = %q, want it to name the seat and the base it could not find", report.Notes[0])
	}
}

// The base branch is the ROOM's, never fixed to main: the room setting wins over the fallback, and the flag
// wins over the room.
func TestOverlapBaseIsTheRoomsThenTheFlag(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	goDevWt, _ := overlapFixtureWorktree(roomDir, "go-dev", "developer")
	overlapFactsFor(answers, goDevWt, "batch-7", nil, nil, 0, time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC))
	fixture := overlapFixtureFor(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}}, answers,
		clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir
	fixture.deps.baseName = func(string) string { return "batch-7" }

	if report := overlapScanFor(fixture.deps, fixture.options()); report.Base != "batch-7" {
		t.Errorf("base = %q, want the room's batch-7", report.Base)
	}
	options := fixture.options()
	options.BaseBranch = "hotfix"
	if report := overlapScanFor(fixture.deps, options); report.Base != "hotfix" {
		t.Errorf("base = %q, want the flag's hotfix", report.Base)
	}
	fixture.deps.baseName = func(string) string { return "" }
	if report := overlapScanFor(fixture.deps, fixture.options()); report.Base != overlapDefaultBase {
		t.Errorf("base = %q, want the fallback %q", report.Base, overlapDefaultBase)
	}
}

// The room setting is read from team.json's room.base_branch, by the same reader the rest of the client uses.
func TestOverlapReadsTheRoomSettingFromTheTeamFile(t *testing.T) {
	dir := t.TempDir()
	body := `{"room":{"slug":"demo","base_branch":"batch-7"},"seats":[{"seat_key":"lead","seat_type":"lead"}]}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := overlapTeamBaseBranch(dir); got != "batch-7" {
		t.Errorf("base branch = %q, want batch-7", got)
	}
	if got := overlapTeamBaseBranch(t.TempDir()); got != "" {
		t.Errorf("base branch of a room with no team file = %q, want nothing", got)
	}
}

// One report is sent once: the same file with the same seats stays quiet inside the hour, a third seat joining
// is news, and the hour passing is news again.
func TestOverlapSendsOnceAndOnlyWhenTheSetChanges(t *testing.T) {
	seats := []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"}, {Key: "fe-dev", Type: "writer"}, {Key: "lead", Type: "lead"},
	}
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	moved := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	paths := map[string]string{}
	for _, key := range []string{"go-dev", "fe-dev", "lead"} {
		worktree, _ := overlapFixtureWorktree(roomDir, key, "developer")
		paths[key] = worktree
	}
	fixture := overlapFixtureFor(t, seats, answers, clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir
	shared := "src/shared.go"

	// First scan: two seats on the file, both told, plus the lead (which is not one of them).
	overlapFactsFor(answers, paths["go-dev"], "main", []string{shared}, nil, 1, moved)
	overlapFactsFor(answers, paths["fe-dev"], "main", []string{shared}, nil, 1, moved)
	overlapFactsFor(answers, paths["lead"], "main", []string{"src/other.go"}, nil, 1, moved)
	report := overlapScanFor(fixture.deps, fixture.options())
	overlapNotify(fixture.deps, fixture.options(), &report)
	if len(fixture.sent) != 3 {
		t.Fatalf("sent %d messages, want go-dev, fe-dev and the lead: %v", len(fixture.sent), fixture.sent)
	}

	// Same file, same seats, thirty minutes later: silent.
	fixture.sent = nil
	fixture.now = fixture.now.Add(30 * time.Minute)
	report = overlapScanFor(fixture.deps, fixture.options())
	overlapNotify(fixture.deps, fixture.options(), &report)
	if len(fixture.sent) != 0 {
		t.Fatalf("the same file and seats were reported again inside the hour: %v", fixture.sent)
	}

	// The lead joins the same file: that is news, and now the lead is one of the pair, so it is told once.
	fixture.sent = nil
	overlapFactsFor(answers, paths["lead"], "main", []string{shared}, nil, 1, moved)
	report = overlapScanFor(fixture.deps, fixture.options())
	overlapNotify(fixture.deps, fixture.options(), &report)
	if len(fixture.sent) != 3 {
		t.Fatalf("a third seat joining sent %d messages, want the three seats: %v", len(fixture.sent), fixture.sent)
	}
	if strings.Count(strings.Join(fixture.sent, "\n"), "lead: ") != 1 {
		t.Errorf("the lead was told twice:\n%s", strings.Join(fixture.sent, "\n"))
	}

	// The RECORD of that report and the DRY RUN of it are the same recipient list, and the lead appears in it
	// once. Before this, both appended "and the lead" to a list that already named it, so the dry run promised
	// a line the real send does not make and the record claimed a recipient that was never told.
	if len(report.Sent) != 1 || strings.Count(report.Sent[0], "lead") != 1 {
		t.Errorf("the sent record does not name the lead exactly once: %v", report.Sent)
	}
	var plan strings.Builder
	printOverlapPlan(&plan, report)
	headers := 0
	for _, line := range strings.Split(plan.String(), "\n") {
		if !strings.HasPrefix(line, "  would send to ") {
			continue
		}
		headers++
		head := line[:strings.Index(line, ": ")]
		for _, want := range []string{"fe-dev", "go-dev", "lead"} {
			if !strings.Contains(head, want) {
				t.Errorf("the dry run does not name %s among the recipients: %q", want, head)
			}
		}
		if strings.Count(head, "lead") != 1 {
			t.Errorf("the dry run names the lead more than once: %q", head)
		}
	}
	if headers != 1 {
		t.Errorf("the dry run printed %d recipient lines, want one: %q", headers, plan.String())
	}

	// Past the hour: the same file and the same three seats are reported again.
	fixture.sent = nil
	fixture.now = fixture.now.Add(overlapRepeat + time.Minute)
	report = overlapScanFor(fixture.deps, fixture.options())
	overlapNotify(fixture.deps, fixture.options(), &report)
	if len(fixture.sent) != 3 {
		t.Fatalf("after the hour sent %d messages, want the three seats again: %v", len(fixture.sent), fixture.sent)
	}
}

// A seat branch with unmerged commits that has not moved for the stale window is reported; the same branch
// inside the window is not, and a branch with nothing unmerged is never.
func TestOverlapReportsAStaleSeatBranch(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	staleWt, _ := overlapFixtureWorktree(roomDir, "go-dev", "developer")
	freshWt, _ := overlapFixtureWorktree(roomDir, "fe-dev", "writer")
	mergedWt, _ := overlapFixtureWorktree(roomDir, "lead", "lead")
	old := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	overlapFactsFor(answers, staleWt, "main", []string{"a.go"}, nil, 4, old)
	overlapFactsFor(answers, freshWt, "main", []string{"b.go"}, nil, 2, time.Date(2026, 10, 11, 9, 59, 0, 0, time.UTC))
	overlapFactsFor(answers, mergedWt, "main", nil, nil, 0, old)
	fixture := overlapFixtureFor(t, []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"}, {Key: "fe-dev", Type: "writer"}, {Key: "lead", Type: "lead"},
	}, answers, clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Stale) != 1 || report.Stale[0].Seat != "go-dev" {
		t.Fatalf("stale = %+v, want go-dev alone: fe-dev moved an hour ago and the lead has nothing unmerged", report.Stale)
	}
	overlapNotify(fixture.deps, fixture.options(), &report)
	text := strings.Join(fixture.sent, "\n")
	for _, want := range []string{"A seat branch has unmerged commits and has not moved for 4h0m0s.",
		"Seat go-dev on branch " + overlapFixtureRoom + "/go-dev (4 commits not yet merged, last at 2026-10-11T01:00:00Z).",
		"Nothing is blocked. The lead decides whether it is merged, rebased or dropped."} {
		if !strings.Contains(text, want) {
			t.Errorf("the alert does not carry %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "lead: ") {
		t.Errorf("the lead was not told about the stale branch:\n%s", text)
	}
}

// porcelainPaths strips the two status columns and takes the NEW path of a rename: a missed parse here is a
// missed overlap or a false one.
func TestPorcelainPathsReadsWhatStatusNames(t *testing.T) {
	got := porcelainPaths(" M internal/a.go\n?? internal/new.go\nR  old.go -> internal/renamed.go\n")
	want := []string{"internal/a.go", "internal/new.go", "internal/renamed.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

// agent runs the detector every ten minutes, and the first pass runs immediately: a room `up` adds while agent
// runs is looked at on the next pass, not ten minutes later.
func TestRoomdRunsTheDetectorEveryTenMinutes(t *testing.T) {
	manager := newAgentManager(t.TempDir(), func(string) {})
	start := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	if !manager.overlapDue(start) {
		t.Error("the first pass did not run the detector")
	}
	if manager.overlapDue(start.Add(overlapEvery - time.Second)) {
		t.Error("the detector ran again inside its ten minutes")
	}
	if !manager.overlapDue(start.Add(overlapEvery)) {
		t.Error("the detector did not run when its ten minutes had passed")
	}
}

// The verb reads its own arguments, so a run on request can point at another base without a code change.
func TestOverlapVerbRunsOneScanAndPrintsIt(t *testing.T) {
	dir := t.TempDir()
	restoreDeps, restoreScan := overlapDepsFor, overlapScanFor
	t.Cleanup(func() { overlapDepsFor, overlapScanFor = restoreDeps, restoreScan })

	overlapDepsFor = func(string) *overlapDeps {
		return &overlapDeps{
			now:   func() time.Time { return time.Now() },
			say:   func(string, string, string) {},
			state: func(string) (string, error) { return filepath.Join(dir, "overlap.json"), nil },
		}
	}
	overlapScanFor = func(_ *overlapDeps, opts overlapOptions) overlapReport {
		return overlapReport{Room: opts.Room, Base: opts.BaseBranch, StaleAt: opts.StaleAfter}
	}
	t.Setenv("OPENRIG_STATE_ROOT", dir)

	var stdout, stderr strings.Builder
	code := (overlapCommand{}).Run(context.Background(),
		[]string{"--base-branch", "batch-7", "demo"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "overlap demo: base batch-7") {
		t.Errorf("the report does not name the room and its base:\n%s", stdout.String())
	}

	stdout.Reset()
	code = (overlapCommand{}).Run(context.Background(), []string{"--json", "demo"}, &stdout, &stderr)
	if code != clientcmd.ExitOK || !strings.Contains(stdout.String(), `"room": "demo"`) {
		t.Errorf("--json printed %q (exit %d)", stdout.String(), code)
	}

	stdout.Reset()
	if code := (overlapCommand{}).Run(context.Background(), nil, &stdout, &stderr); code != clientcmd.ExitUsage {
		t.Errorf("a run with no ROOM exited %d, want the usage exit", code)
	}
}

// A dry run reads and shows what it WOULD send, and sends nothing: the first run of a detector that messages a
// whole room must be safe to watch, which is why the flag exists rather than a filter on the logs.
func TestOverlapDryRunSendsNothing(t *testing.T) {
	restoreDeps, restoreScan := overlapDepsFor, overlapScanFor
	t.Cleanup(func() { overlapDepsFor, overlapScanFor = restoreDeps, restoreScan })

	var said []string
	overlapDepsFor = func(string) *overlapDeps {
		return &overlapDeps{
			now:   func() time.Time { return time.Now() },
			say:   func(room, seat, text string) { said = append(said, seat) },
			state: func(string) (string, error) { return filepath.Join(t.TempDir(), "overlap.json"), nil },
		}
	}
	overlapScanFor = func(_ *overlapDeps, opts overlapOptions) overlapReport {
		seats := []overlapFacts{
			{Seat: "go-dev", Branch: "demo/go-dev", Unmerged: 2, Last: time.Unix(1, 0).UTC()},
			{Seat: "fe-dev", Branch: "demo/fe-dev", Unmerged: 1, Last: time.Unix(1, 0).UTC()},
		}
		return overlapReport{Room: opts.Room, Base: "main", StaleAt: opts.StaleAfter,
			Hits: []overlapHit{{File: "src/shared.go", Seats: seats}}}
	}
	t.Setenv("OPENRIG_STATE_ROOT", t.TempDir())

	var stdout, stderr strings.Builder
	code := (overlapCommand{}).Run(context.Background(), []string{"--dry-run", "demo"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if len(said) != 0 {
		t.Errorf("a dry run messaged %v", said)
	}
	for _, want := range []string{"DRY RUN, nothing sent", "would send to fe-dev, go-dev and the lead:",
		"Two seats are changing the same file. File: src/shared.go."} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the dry run does not show %q:\n%s", want, stdout.String())
		}
	}
}

// overlapFixtureWorktree is the path and branch of one seat's worktree as the CLIENT derives them: the one
// rule (clientservices.DerivedWorktrees), so a case cannot pass against a path the doctor and `up` disagree
// with. An empty answer means the room derives no entry for that key.
func overlapFixtureWorktree(roomDir, key, seatType string) (string, string) {
	for _, derived := range clientservices.DerivedWorktrees(roomDir, overlapFixtureRoom,
		[]clientservices.SeatType{{Key: key, Type: seatType}}) {
		if derived.SeatKey == key {
			return derived.Path, derived.Branch
		}
	}
	return "", ""
}

// The scan's production wiring binds the room directory to the root the VERB resolved (hub task
// 54a942c5): `overlap --state-root DIR` must read DIR/rooms/<room>, not the machine's own store.
func TestTheOverlapScanReadsTheRoomUnderTheResolvedRoot(t *testing.T) {
	deps := defaultOverlapDeps("/tmp/x")
	dir, err := deps.roomDir("demo")
	if err != nil {
		t.Fatalf("the scan's roomDir: %v", err)
	}
	if want := filepath.Join("/tmp/x", "rooms", "demo"); dir != want {
		t.Fatalf("the scan reads %q, want %q", dir, want)
	}
}

// A REPOSITORY NESTED IN EACH SEAT'S CHECKOUT IS NOT A SHARED FILE. On aa2-rs each seat's wt/<seat> is a hub
// worktree with the project's own worktree inside it at aa2-rs/, which `git status` names as one untracked
// directory in every seat; the scan alerted two seats "changing file aa2-rs/" on hub branches stamped
// 2026-10-23 (the lead's false-positive report, 2026-10-28 04:53Z). A nested directory holding its own .git is
// dropped; a real untracked file the two seats share is still a hit.
func TestOverlapIgnoresARepositoryNestedInTheSeatCheckouts(t *testing.T) {
	roomDir := filepath.Join(t.TempDir(), "demo")
	answers := map[string]string{}
	goDevWt, _ := overlapFixtureWorktree(roomDir, "go-dev", "developer")
	feDevWt, _ := overlapFixtureWorktree(roomDir, "fe-dev", "writer")
	for _, wt := range []string{goDevWt, feDevWt} {
		if err := os.MkdirAll(filepath.Join(wt, "aa2-rs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, "aa2-rs", ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	moved := time.Date(2026, 10, 23, 1, 39, 0, 0, time.UTC)
	overlapFactsFor(answers, goDevWt, "main", nil, nil, 0, moved)
	overlapFactsFor(answers, feDevWt, "main", nil, nil, 0, moved)
	answers[goDevWt+"|status --porcelain"] = "?? aa2-rs/\n?? notes.md\n"
	answers[feDevWt+"|status --porcelain"] = "?? aa2-rs/\n?? notes.md\n"
	fixture := overlapFixtureFor(t, []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"}, {Key: "fe-dev", Type: "writer"},
	}, answers, clientworktree.CheckVerdict{})
	fixture.roomDir = roomDir

	report := overlapScanFor(fixture.deps, fixture.options())
	if len(report.Hits) != 1 || report.Hits[0].File != "notes.md" {
		t.Fatalf("hits = %+v, want only notes.md: a nested repository is not a file the seats share", report.Hits)
	}
}
