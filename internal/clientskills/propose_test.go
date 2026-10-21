package clientskills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// proposeFixture is one tree, the room it is read for, and the seams a proposal goes through: the caller's
// worktree and the cited rows. Every test starts here so no test can reach a daemon or a real worktree.
type proposeFixture struct {
	tree  Tree
	room  string
	head  string
	rows  map[string]*clientruntime.QueueRow
	fault map[string]string
}

const proposeSkillBody = `---
name: tool
description: a fixture skill
signals:
  - row-tag: defect
---
body
`

func newProposeFixture(t *testing.T, body string) *proposeFixture {
	t.Helper()
	t.Setenv("AGENTHUB_ROOM", "")
	t.Setenv("AGENTHUB_SEAT", "")
	t.Setenv("AGENTHUB_SESSION_NAME", "")
	if body == "" {
		body = proposeSkillBody
	}
	tree, head := gitTree(t, map[string]string{"share/tool/" + SkillFile: body})
	fixture := &proposeFixture{
		tree:  tree,
		room:  "fixtureroom",
		head:  head,
		rows:  map[string]*clientruntime.QueueRow{"qitem-1": {QitemID: "qitem-1", Tags: []string{"defect"}}},
		fault: map[string]string{},
	}
	restore := callerWorktree
	callerWorktree = func(string) (string, error) { return tree.Root, nil }
	restoreRows := queueRow
	queueRow = func(room, id string) (*clientruntime.QueueRow, error) {
		if reason, ok := fixture.fault[id]; ok {
			return nil, &fixtureError{reason}
		}
		row, ok := fixture.rows[id]
		if !ok {
			return nil, &fixtureError{"no such row"}
		}
		return row, nil
	}
	t.Cleanup(func() { callerWorktree = restore; queueRow = restoreRows })
	return fixture
}

// fixtureError is what the seam answers for a row the fixture does not have, in the shape the daemon's own
// errors take - a message, not a nil row, because that is the case the verb must refuse.
type fixtureError struct{ reason string }

func (e *fixtureError) Error() string { return e.reason }

// propose runs the verb over the fixture and answers what it printed.
func (f *proposeFixture) propose(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{f.room, "--skills-root", f.tree.Root}, args...)
	code := runSkillsPropose(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (f *proposeFixture) skillDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(f.tree.Root, ShareDir, "tool")
}

func readText(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// TestProposeWritesAnUncommittedRecordAndDraft: the proposal appears in the caller's worktree, the record
// names the cited evidence and windows from HEAD, the draft is applied verbatim, and the repository is in
// exactly the state a gate reviews - changed, staged nothing, committed nothing.
func TestProposeWritesAnUncommittedRecordAndDraft(t *testing.T) {
	fixture := newProposeFixture(t, "")
	draft := filepath.Join(t.TempDir(), "SKILL.md")
	revision := proposeSkillBody + "one more line\n"
	if err := os.WriteFile(draft, []byte(revision), 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	code, stdout, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix",
		"--from", "qitem-1", "--draft", draft, "--risk", "the cited refusal was a race, not a rule")
	if code != 0 {
		t.Fatalf("propose: exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "nothing was committed") {
		t.Fatalf("the answer must say nothing was committed: %q", stdout)
	}
	record, present, err := ReadRecord(fixture.skillDir(t))
	if err != nil || !present {
		t.Fatalf("the proposal wrote no readable record: present=%v err=%v", present, err)
	}
	if record.State != StateProvisional || record.Change != ChangeFix {
		t.Fatalf("record = %+v, want provisional/fix", record)
	}
	if record.Since != fixture.head {
		t.Fatalf("record since = %q, want the tree's HEAD %q", record.Since, fixture.head)
	}
	if len(record.Evidence) != 1 || record.Evidence[0] != "qitem-1" {
		t.Fatalf("record evidence = %v, want the cited row", record.Evidence)
	}
	if record.Risk != "the cited refusal was a race, not a rule" {
		t.Fatalf("record risk = %q, want the one --risk named", record.Risk)
	}
	if got := readText(t, filepath.Join(fixture.skillDir(t), SkillFile)); got != revision {
		t.Fatalf("SKILL.md is not the draft verbatim:\n%s", got)
	}

	if staged := git(t, fixture.tree.Root, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("the proposal staged something: %q", staged)
	}
	if log := git(t, fixture.tree.Root, "log", "--oneline"); strings.Count(log, "\n") != 0 {
		t.Fatalf("the proposal committed: %q", log)
	}
	changed := git(t, fixture.tree.Root, "status", "--porcelain")
	for _, want := range []string{"share/tool/" + SkillFile, "share/tool/" + RecordFile} {
		if !strings.Contains(changed, want) {
			t.Fatalf("git status does not show %s: %q", want, changed)
		}
	}
}

// TestProposeRefusesAnUnresolvedEvidenceID: one id the rail does not have refuses the whole proposal, by the
// id's name, and writes nothing - a citation the daemon cannot answer is not evidence.
func TestProposeRefusesAnUnresolvedEvidenceID(t *testing.T) {
	fixture := newProposeFixture(t, "")
	fixture.fault["qitem-gone"] = "row qitem-gone: not found"

	code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "qitem-1,qitem-gone")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "qitem-gone") || !strings.Contains(stderr, "does not resolve") {
		t.Fatalf("the refusal must name the id: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(fixture.skillDir(t), RecordFile)); !os.IsNotExist(err) {
		t.Fatalf("a refused proposal wrote a record (stat err = %v)", err)
	}
	if got := readText(t, filepath.Join(fixture.skillDir(t), SkillFile)); got != proposeSkillBody {
		t.Fatalf("a refused proposal rewrote SKILL.md")
	}
}

// TestProposeRefusesASkillThatDeclaresNoSignalTheRowsCarry: the skill declares only a stream kind, and a queue
// row's typed facts are its tags, so the proposal cites evidence the skill is not accountable for.
func TestProposeRefusesASkillThatDeclaresNoSignalTheRowsCarry(t *testing.T) {
	fixture := newProposeFixture(t, `---
name: tool
description: a fixture skill
signals:
  - guard: docs-only
---
body
`)

	code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "qitem-1")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "guard: docs-only") || !strings.Contains(stderr, "row-tag") {
		t.Fatalf("the refusal must name the declared signals and the kind a row can carry: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(fixture.skillDir(t), RecordFile)); !os.IsNotExist(err) {
		t.Fatalf("a refused proposal wrote a record (stat err = %v)", err)
	}
}

// TestProposeRefusesAProposalThatChangesNoLine: the same proposal twice changes nothing the second time, and
// the second run refuses instead of rewriting a file with its own bytes.
func TestProposeRefusesAProposalThatChangesNoLine(t *testing.T) {
	fixture := newProposeFixture(t, "")
	if code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "qitem-1"); code != 0 {
		t.Fatalf("first proposal: exit %d, stderr %q", code, stderr)
	}
	path := filepath.Join(fixture.skillDir(t), RecordFile)
	before := readText(t, path)

	code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "qitem-1")
	if code != 2 {
		t.Fatalf("second proposal: exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "changes no line") {
		t.Fatalf("the refusal must say the proposal changes no line: %q", stderr)
	}
	if after := readText(t, path); after != before {
		t.Fatalf("a refused proposal rewrote the record")
	}
}

// TestProposeRefusesAnotherSeatsWorktree: the write target is the caller's own worktree, so a tree outside it
// is refused by both paths and left alone.
func TestProposeRefusesAnotherSeatsWorktree(t *testing.T) {
	fixture := newProposeFixture(t, "")
	elsewhere := t.TempDir()
	callerWorktree = func(string) (string, error) { return elsewhere, nil }

	code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "qitem-1")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, elsewhere) || !strings.Contains(stderr, "outside your own worktree") {
		t.Fatalf("the refusal must name the worktree it refused: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(fixture.skillDir(t), RecordFile)); !os.IsNotExist(err) {
		t.Fatalf("a refused proposal wrote a record (stat err = %v)", err)
	}
}

// TestProposeNamesTheLevelWhenTwoLevelsDeliverTheName: two levels are two files, so the bare name is refused
// with both candidates listed and LEVEL/NAME picks one.
func TestProposeNamesTheLevelWhenTwoLevelsDeliverTheName(t *testing.T) {
	fixture := newProposeFixture(t, "")
	roomSkill := filepath.Join(fixture.tree.Root, RoomsDir, fixture.room, "tool")
	writeFile(t, filepath.Join(roomSkill, SkillFile), proposeSkillBody)

	code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "qitem-1")
	if code != 2 {
		t.Fatalf("ambiguous name: exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "share/tool") || !strings.Contains(stderr, "room/tool") {
		t.Fatalf("the refusal must list both levels: %q", stderr)
	}

	if code, _, stderr := fixture.propose(t, "--skill", "room/tool", "--change", "fix", "--from", "qitem-1"); code != 0 {
		t.Fatalf("LEVEL/NAME: exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(roomSkill, RecordFile)); err != nil {
		t.Fatalf("the proposal did not land in the room-level skill: %v", err)
	}
}

// TestProposeRefusesAnIDThatIsNotAQueueRow: --from cites queue rows, and a commit name is a different kind of
// evidence - refused by name rather than handed to the rail.
func TestProposeRefusesAnIDThatIsNotAQueueRow(t *testing.T) {
	fixture := newProposeFixture(t, "")

	code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "0123abc")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "is not a qitem- id") {
		t.Fatalf("the refusal must say what kind of id it wanted: %q", stderr)
	}
}

// TestSeatFromSessionMatchesOnlyThisRoomsSeat: the room in the session name is the guard, so a name for
// another room answers nothing instead of naming a seat this room does not have.
func TestSeatFromSessionMatchesOnlyThisRoomsSeat(t *testing.T) {
	cases := []struct{ session, room, want string }{
		{"agenthub--4genthub-min--go-dev", "4genthub-min", "go-dev"},
		{"4genthub-min--fe-dev", "4genthub-min", ""},
		{"agenthub--other-room--go-dev", "4genthub-min", ""},
		{"", "4genthub-min", ""},
	}
	for _, testCase := range cases {
		if got := seatFromSession(testCase.session, testCase.room); got != testCase.want {
			t.Fatalf("seatFromSession(%q, %q) = %q, want %q",
				testCase.session, testCase.room, got, testCase.want)
		}
	}
}
