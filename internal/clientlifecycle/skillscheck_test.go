package clientlifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientskills"
)

// seatCheckFixture is one seat's inputs on disk: a tree with share/room/seat levels, the directory the
// runtime installed into, the worktree the room's derivation names, and the fake evidence for one case.
type seatCheckFixture struct {
	t        *testing.T
	root     string
	treeRoot string
	room     string
	seat     string
	worktree string
	agentDir string
	statuses []clientruntime.SeatStatus
	listing  []ompSkillEntry
	cwd      map[string]string
}

func newSeatCheckFixture(t *testing.T) *seatCheckFixture {
	t.Helper()
	f := &seatCheckFixture{
		t:    t,
		root: t.TempDir(),
		room: "demo",
		seat: "dev",
		cwd:  map[string]string{},
	}
	f.treeRoot = filepath.Join(f.root, "checkout", "agenthub_client", clientskills.SkillsDir)
	f.worktree = filepath.Join(f.root, "rooms", f.room, "wt", f.seat)
	f.agentDir = filepath.Join(f.root, "rooms", f.room, "seats", f.seat)
	f.cwd[f.seat] = f.worktree
	f.statuses = []clientruntime.SeatStatus{{Room: f.room, Seat: f.seat, Runtime: runtimeOMP,
		SessionName: "agenthub--" + f.room + "--" + f.seat, State: "running"}}

	// The seams: the tree root, the room record, the runtime statuses, the pane paths and the omp listing.
	oldTreeRoot, oldSpec, oldSeats, oldCwd, oldList := skillsTreeRoot, roomSpec, roomSeats, seatCwd, ompSkillList
	t.Cleanup(func() {
		skillsTreeRoot, roomSpec, roomSeats, seatCwd, ompSkillList = oldTreeRoot, oldSpec, oldSeats, oldCwd, oldList
	})
	skillsTreeRoot = func(string) (string, error) { return f.treeRoot, nil }
	roomSpec = func(string, string) (clientruntime.RoomSpec, error) {
		return clientruntime.RoomSpec{Room: f.room, Seats: []clientruntime.SeatSpec{
			{SeatKey: f.seat, SeatType: "developer", Runtime: runtimeOMP},
		}}, nil
	}
	roomSeats = func(string) ([]clientruntime.SeatStatus, error) { return f.statuses, nil }
	seatCwd = func(session string) string {
		seat := strings.TrimPrefix(session, "agenthub--"+f.room+"--")
		return f.cwd[seat]
	}
	ompSkillList = func(string, []string) ([]byte, error) {
		return json.Marshal(ompSkillsDocument{Skills: f.listing})
	}
	return f
}

// skill writes one skill at a tree level and returns its directory.
func (f *seatCheckFixture) skill(level, name string) string {
	f.t.Helper()
	dir := filepath.Join(f.treeRoot, filepath.FromSlash(level), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("create %s: %v", dir, err)
	}
	f.writeSkill(dir, name)
	return dir
}

func (f *seatCheckFixture) writeSkill(dir, name string) {
	f.t.Helper()
	body := "---\nname: " + name + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, clientskills.SkillFile), []byte(body), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", dir, err)
	}
}

// install copies the tree's skill into the seat's installed directory, the way the runtime does.
func (f *seatCheckFixture) install(name string, from string) string {
	f.t.Helper()
	dir := filepath.Join(f.agentDir, clientskills.SkillsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("create %s: %v", dir, err)
	}
	body, err := os.ReadFile(filepath.Join(from, clientskills.SkillFile))
	if err != nil {
		f.t.Fatalf("read %s: %v", from, err)
	}
	if err := os.WriteFile(filepath.Join(dir, clientskills.SkillFile), body, 0o644); err != nil {
		f.t.Fatalf("write %s: %v", dir, err)
	}
	return dir
}

// installFor copies one skill into another seat's installed directory.
func (f *seatCheckFixture) installFor(seat, name, from string) string {
	f.t.Helper()
	dir := filepath.Join(f.root, "rooms", f.room, "seats", seat, clientskills.SkillsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("create %s: %v", dir, err)
	}
	body, err := os.ReadFile(filepath.Join(from, clientskills.SkillFile))
	if err != nil {
		f.t.Fatalf("read %s: %v", from, err)
	}
	if err := os.WriteFile(filepath.Join(dir, clientskills.SkillFile), body, 0o644); err != nil {
		f.t.Fatalf("write %s: %v", dir, err)
	}
	return dir
}

// listingFor is the omp evidence for the installed set: every skill with the SKILL.md it resolved to.
func (f *seatCheckFixture) listingFor(names ...string) []ompSkillEntry {
	entries := make([]ompSkillEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, ompSkillEntry{Name: name, Source: "native:user",
			FilePath: filepath.Join(f.agentDir, clientskills.SkillsDir, name, clientskills.SkillFile)})
	}
	return entries
}

func (f *seatCheckFixture) verdict() seatSkillsVerdict {
	f.t.Helper()
	return roomSkillsVerdict(f.root, f.room)
}

func codes(findings []seatSkillFailure) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code+" "+finding.Detail)
	}
	return codes
}

func TestACorrectlyInstalledSeatHasNoFinding(t *testing.T) {
	f := newSeatCheckFixture(t)
	from := f.skill("share", "plain")
	f.install("plain", from)
	f.listing = f.listingFor("plain")

	verdict := f.verdict()
	if len(verdict.Failures) != 0 || len(verdict.Notes) != 0 {
		t.Fatalf("a seat in its worktree with its set installed and loaded reported %v / %v",
			codes(verdict.Failures), codes(verdict.Notes))
	}
}

func TestACwdOutsideTheDerivedWorktreeIsF1(t *testing.T) {
	f := newSeatCheckFixture(t)
	f.skill("share", "plain")
	f.install("plain", filepath.Join(f.treeRoot, "share", "plain"))
	f.listing = f.listingFor("plain")
	f.cwd[f.seat] = filepath.Join(f.root, "somewhere-else")

	verdict := f.verdict()
	if len(verdict.Failures) != 1 || verdict.Failures[0].Code != codeCwd {
		t.Fatalf("want one F1, got %v", codes(verdict.Failures))
	}
	if !strings.Contains(verdict.Failures[0].Detail, f.worktree) {
		t.Fatalf("F1 does not name the derived worktree: %q", verdict.Failures[0].Detail)
	}
	// The refusal names the way out, with the room and the seat the remedy takes.
	if !strings.Contains(verdict.Failures[0].Detail, "4genteam seat reseat "+f.room+" --seat "+f.seat) {
		t.Fatalf("F1 does not name the remedy: %q", verdict.Failures[0].Detail)
	}
}

func TestASetSkillTheRuntimeDidNotLoadIsF3(t *testing.T) {
	f := newSeatCheckFixture(t)
	f.skill("share", "plain")
	f.skill("share", "second")
	f.install("plain", filepath.Join(f.treeRoot, "share", "plain"))
	f.install("second", filepath.Join(f.treeRoot, "share", "second"))
	f.listing = f.listingFor("plain")

	verdict := f.verdict()
	if len(verdict.Failures) != 1 || verdict.Failures[0].Code != codeMissing {
		t.Fatalf("want one F3, got %v", codes(verdict.Failures))
	}
	if !strings.Contains(verdict.Failures[0].Detail, `"second"`) {
		t.Fatalf("F3 does not name the skill: %q", verdict.Failures[0].Detail)
	}
}

func TestASkillInstalledButNotInTheTreeIsF2(t *testing.T) {
	f := newSeatCheckFixture(t)
	from := f.skill("share", "plain")
	f.install("plain", from)
	// A skill sitting in the installed tree that the tree does not declare: one of the two is wrong.
	leftover := filepath.Join(f.agentDir, clientskills.SkillsDir, "leftover")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatalf("create %s: %v", leftover, err)
	}
	f.writeSkill(leftover, "leftover")
	f.listing = f.listingFor("plain", "leftover")

	verdict := f.verdict()
	if len(verdict.Failures) != 1 || verdict.Failures[0].Code != codeForeign {
		t.Fatalf("want one F2, got %v", codes(verdict.Failures))
	}
	if !strings.Contains(verdict.Failures[0].Detail, "leftover") ||
		!strings.Contains(verdict.Failures[0].Detail, filepath.Join("leftover", clientskills.SkillFile)) {
		t.Fatalf("F2 does not name the skill and its file: %q", verdict.Failures[0].Detail)
	}
}

func TestASkillLoadedFromThePlatformRootIsNotAFinding(t *testing.T) {
	f := newSeatCheckFixture(t)
	from := f.skill("share", "plain")
	f.install("plain", from)
	platform := filepath.Join(f.root, "home", ".agents", clientskills.SkillsDir, "client-skills", clientskills.SkillFile)
	f.listing = append(f.listingFor("plain"),
		ompSkillEntry{Name: "client-skills", Source: "agents:user", FilePath: platform})

	verdict := f.verdict()
	if len(verdict.Failures) != 0 {
		t.Fatalf("the platform's own delivery was reported as foreign: %v", codes(verdict.Failures))
	}
}

func TestASetSkillLoadedOutsideTheInstalledTreeIsF4(t *testing.T) {
	f := newSeatCheckFixture(t)
	from := f.skill("share", "plain")
	f.install("plain", from)
	f.listing = []ompSkillEntry{{Name: "plain", Source: "agents:user",
		FilePath: filepath.Join(f.root, "home", ".agents", clientskills.SkillsDir, "plain", clientskills.SkillFile)}}

	verdict := f.verdict()
	if len(verdict.Failures) != 1 || verdict.Failures[0].Code != codePath {
		t.Fatalf("want one F4, got %v", codes(verdict.Failures))
	}
}

func TestAnEditedInstalledCopyIsF5(t *testing.T) {
	f := newSeatCheckFixture(t)
	from := f.skill("share", "plain")
	installed := f.install("plain", from)
	f.listing = f.listingFor("plain")
	if err := os.WriteFile(filepath.Join(installed, clientskills.SkillFile), []byte("edited\n"), 0o644); err != nil {
		t.Fatalf("edit the installed copy: %v", err)
	}

	verdict := f.verdict()
	if len(verdict.Failures) != 1 || verdict.Failures[0].Code != codeStale {
		t.Fatalf("want one F5, got %v", codes(verdict.Failures))
	}
}

func TestAnAbsentInstalledTreeIsOneF5(t *testing.T) {
	f := newSeatCheckFixture(t)
	f.skill("share", "plain")
	f.skill("share", "second")
	f.listing = nil

	verdict := f.verdict()
	stale := 0
	for _, failure := range verdict.Failures {
		if failure.Code == codeStale {
			stale++
			if !strings.Contains(failure.Detail, "absent") {
				t.Fatalf("the line does not say the tree is absent: %q", failure.Detail)
			}
		}
	}
	if stale != 1 {
		t.Fatalf("an absent install must be ONE F5, got %v", codes(verdict.Failures))
	}
}

func TestARuntimeWithNoReaderIsNotedAndF5IsStillDecided(t *testing.T) {
	f := newSeatCheckFixture(t)
	f.statuses = []clientruntime.SeatStatus{{Room: f.room, Seat: f.seat, Runtime: "agy",
		SessionName: "agenthub--" + f.room + "--" + f.seat, State: "running"}}
	from := f.skill("share", "plain")
	f.install("plain", from)

	verdict := f.verdict()
	if len(verdict.Failures) != 0 {
		t.Fatalf("a runtime with no reader produced failures: %v", codes(verdict.Failures))
	}
	if len(verdict.Notes) != 1 || !strings.Contains(verdict.Notes[0].Detail, `"agy"`) {
		t.Fatalf("want one note naming the runtime, got %v", codes(verdict.Notes))
	}
}

func TestAMissingInputIsNotedOnceForTheWholeRoom(t *testing.T) {
	f := newSeatCheckFixture(t)
	f.statuses = []clientruntime.SeatStatus{
		{Room: f.room, Seat: "a", Runtime: "agy", SessionName: "agenthub--demo--a"},
		{Room: f.room, Seat: "b", Runtime: "agy", SessionName: "agenthub--demo--b"},
	}
	f.skill("share", "plain")

	verdict := f.verdict()
	if len(verdict.Notes) != 2 {
		t.Fatalf("want the runtime note once plus one F1 note per seat, got %v", codes(verdict.Notes))
	}
}

func TestAnUnreadableRoomIsOneNoteNamingTheReason(t *testing.T) {
	f := newSeatCheckFixture(t)
	roomSpec = func(string, string) (clientruntime.RoomSpec, error) {
		return clientruntime.RoomSpec{}, os.ErrNotExist
	}

	verdict := f.verdict()
	if len(verdict.Failures) != 0 || len(verdict.Notes) != 1 {
		t.Fatalf("want one note and no failures, got %v / %v", codes(verdict.Failures), codes(verdict.Notes))
	}
	if verdict.Notes[0].Seat != f.room {
		t.Fatalf("a room-level note must be attributed to the room, got %q", verdict.Notes[0].Seat)
	}
}

func TestSeatCheckLinesAnswersForTheNamedSeatOnly(t *testing.T) {
	f := newSeatCheckFixture(t)
	oldSpec := roomSpec
	t.Cleanup(func() { roomSpec = oldSpec })
	roomSpec = func(string, string) (clientruntime.RoomSpec, error) {
		return clientruntime.RoomSpec{Room: f.room, Seats: []clientruntime.SeatSpec{
			{SeatKey: "a", SeatType: "developer", Runtime: runtimeOMP},
			{SeatKey: "b", SeatType: "developer", Runtime: runtimeOMP},
		}}, nil
	}
	f.statuses = []clientruntime.SeatStatus{
		{Room: f.room, Seat: "a", Runtime: runtimeOMP, SessionName: "agenthub--demo--a"},
		{Room: f.room, Seat: "b", Runtime: runtimeOMP, SessionName: "agenthub--demo--b"},
	}
	from := f.skill("share", "plain")
	f.installFor("a", "plain", from)
	f.installFor("b", "plain", from)
	f.listing = []ompSkillEntry{
		{Name: "plain", Source: "native:user",
			FilePath: filepath.Join(f.root, "rooms", f.room, "seats", "a", clientskills.SkillsDir, "plain", clientskills.SkillFile)},
	}
	f.cwd["a"] = filepath.Join(f.root, "rooms", f.room, "wt", "a")
	f.cwd["b"] = filepath.Join(f.root, "elsewhere")

	lines := SeatCheckLines(f.root, f.room, "a")
	if len(lines) != 0 {
		t.Fatalf("seat a's checks reported %v, want none (its node is not checked for b)", lines)
	}
	lines = SeatCheckLines(f.root, f.room, "b")
	if len(lines) == 0 || !strings.Contains(lines[0], codeCwd) {
		t.Fatalf("seat b's checks = %v, want its own F1 first", lines)
	}
	for _, line := range lines {
		if strings.Contains(line, "seat a:") {
			t.Fatalf("seat b's answer carries seat a's finding: %q", line)
		}
	}
	if lines := SeatCheckLines(f.root, f.room, "ghost"); len(lines) != 1 ||
		!strings.Contains(lines[0], "no record") {
		t.Fatalf("a seat the runtime does not report = %v, want the fact", lines)
	}
}

func TestTheSeatsRowIsStaleWhenTheCheckFindsADifference(t *testing.T) {
	f := newSeatCheckFixture(t)
	oldRoomSkills := roomSkills
	t.Cleanup(func() { roomSkills = oldRoomSkills })
	roomSkills = func(string, string) seatSkillsVerdict {
		return seatSkillsVerdict{Failures: []seatSkillFailure{{Seat: "dev", Code: codeCwd, Detail: "x"}},
			Notes: []seatSkillFailure{{Seat: "demo", Code: codeUnobserved, Detail: "y"}}}
	}

	row := seatsRows(f.room, f.root)
	if len(row) != 1 {
		t.Fatalf("want one row, got %d", len(row))
	}
	if row[0].Row.Safeguard != clientservices.Seats || row[0].Row.State != clientservices.SkillsStale {
		t.Fatalf("row = %s/%s, want seats/skills_stale", row[0].Row.Safeguard, row[0].Row.State)
	}
	if len(clientservices.NotRunning([]clientservices.Health{row[0].Row})) != 1 {
		t.Fatalf("skills_stale is not counted as down")
	}
	if lines := seatLines(row[0]); len(lines) != 2 || lines[0] != "seat dev: F1 cwd x" {
		t.Fatalf("reasons = %v", lines)
	}
	if notes := seatNoteLines(row); len(notes) != 1 || notes[0] != "seat demo: note y" {
		t.Fatalf("notes = %v", notes)
	}
}
