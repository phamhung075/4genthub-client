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
	tree     Tree
	room     string
	head     string
	rows     map[string]*clientruntime.QueueRow
	fault    map[string]string
	evidence Evidence
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
	restoreEvidence := proposeEvidence
	proposeEvidence = func(room, repo string) Evidence { return fixture.evidence }
	t.Cleanup(func() { proposeEvidence = restoreEvidence })
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

// TestProposeCitesTheCensusRefsItPrints is slice 0 of `DECISION-2026-10-21-skill-drafting.md`: `--from` takes a
// queue row OR one of the census's own refs, and the kind a cite matches is the stream its declared signal names.
// The cases are the decision's five, plus the one that keeps the widening from being a loophole: a ref of the
// wrong stream still carries nothing.
func TestProposeCitesTheCensusRefsItPrints(t *testing.T) {
	const at = "2026-10-21T15:33:22Z"
	refusalRef := "20261021153322-22691"
	jevRef := "jev:2026-10-21T17:06:42.668960571Z seat"
	sendRef := "agenthub--4genthub-min--lead->agenthub--4genthub-min--reviewer"
	cases := []struct {
		name    string
		signals string
		facts   []Fact
		from    string
		want    string // the signal line the proposal printed; "" means it must be refused
		reason  string // a substring the refusal must carry
	}{
		{
			name:    "a row-tag skill on a row",
			signals: "  - row-tag: defect\n",
			from:    "qitem-1",
			want:    "signal: row-tag: defect",
		},
		{
			name:    "a guard skill on the refusal id the spool wrote",
			signals: "  - guard: docs-only\n",
			facts:   []Fact{{Stream: StreamGuard, Value: "docs-only", Ref: refusalRef}},
			from:    refusalRef,
			want:    "signal: guard: docs-only",
		},
		{
			name:    "a guard skill on a seat's blocked tool line",
			signals: "  - guard: env-secret\n",
			facts:   []Fact{{Stream: StreamGuard, Value: "env-secret", Ref: "seat go-dev " + at}},
			from:    "seat go-dev " + at,
			want:    "signal: guard: env-secret",
		},
		{
			name:    "a guard skill on a queue row still carries nothing",
			signals: "  - guard: docs-only\n",
			from:    "qitem-1",
			reason:  "row-tag",
		},
		{
			name:    "a jev skill on the decision log's own line",
			signals: "  - jev: owner-only\n",
			facts:   []Fact{{Stream: StreamJEV, Value: "owner-only", Ref: jevRef}},
			from:    jevRef,
			want:    "signal: jev: owner-only",
		},
		{
			name:    "a send skill on a refused send's audit line",
			signals: "  - send: guest-denied\n",
			facts:   []Fact{{Stream: StreamSend, Value: "guest-denied", Ref: sendRef}},
			from:    sendRef,
			want:    "signal: send: guest-denied",
		},
		{
			name:    "a guard skill on a ref of another stream",
			signals: "  - guard: owner-only\n",
			facts:   []Fact{{Stream: StreamJEV, Value: "owner-only", Ref: jevRef}},
			from:    jevRef,
			reason:  "guard: owner-only",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newProposeFixture(t, "---\nname: tool\ndescription: a fixture skill\nsignals:\n"+
				testCase.signals+"---\nbody\n")
			fixture.evidence = Evidence{Facts: testCase.facts}
			code, stdout, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix",
				"--from", testCase.from)
			if testCase.want == "" {
				if code != 2 || !strings.Contains(stderr, testCase.reason) {
					t.Fatalf("want a refusal naming %q; exit %d, stderr %q", testCase.reason, code, stderr)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if !strings.Contains(stdout, testCase.want) {
				t.Fatalf("the proposal must name %q: %q", testCase.want, stdout)
			}
			record, present, err := ReadRecord(fixture.skillDir(t))
			if err != nil || !present {
				t.Fatalf("no record: present=%v err=%v", present, err)
			}
			if len(record.Evidence) != 1 || record.Evidence[0] != testCase.from {
				t.Fatalf("the record cites %v, want [%s]", record.Evidence, testCase.from)
			}
		})
	}
}
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

// TestProposeRefusesAValueThatIsNeitherARowNorACensusRef: --from takes a queue row or a ref `skills evidence`
// prints, and a value that is neither is refused by name rather than handed to a stream reader that would
// read it as "no such fact".
func TestProposeRefusesAValueThatIsNeitherARowNorACensusRef(t *testing.T) {
	fixture := newProposeFixture(t, "")

	code, _, stderr := fixture.propose(t, "--skill", "tool", "--change", "fix", "--from", "0123abc")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "is neither a qitem- id nor a ref") {
		t.Fatalf("the refusal must say which two kinds it takes: %q", stderr)
	}
}

// TestSeatFromSessionMatchesOnlyThisRoomsSeat: the room in the session name is the guard, so a name for
// another room answers nothing instead of naming a seat this room does not have.
//
// The store holds THREE spellings of a destination and all three are read here: `agenthub--<room>--<seat>`,
// `<seat>@<room>` (the branch propose.go gained when it learned the second spelling, untested until now) and
// the bare `<seat>` - a seat only when the room's own seat keys say so, so a destination naming no seat of the
// room answers nothing rather than demoting every skill the room has.
func TestSeatFromSessionMatchesOnlyThisRoomsSeat(t *testing.T) {
	seats := map[string]bool{"go-dev": true, "lead": true, "reviewer": true}
	cases := []struct{ session, room, want string }{
		{"agenthub--4genthub-min--go-dev", "4genthub-min", "go-dev"},
		{"4genthub-min--fe-dev", "4genthub-min", ""},
		{"agenthub--other-room--go-dev", "4genthub-min", ""},
		{"", "4genthub-min", ""},
		// `<seat>@<room>`: 28 + 4 + 10 rows in this room name their seat this way.
		{"lead@4genthub-min", "4genthub-min", "lead"},
		{"reviewer@4genthub-min", "4genthub-min", "reviewer"},
		{"lead@other-room", "4genthub-min", ""},
		// The bare spelling, which only the room's own seat keys can resolve: 50 rows in this room.
		{"reviewer", "4genthub-min", "reviewer"},
		{"go-dev", "4genthub-min", "go-dev"},
		// A destination that names no seat of the room stays unattributed (`owner-session`: 16 rows here).
		{"owner-session", "4genthub-min", ""},
		{"agenthub--4genthub-min", "4genthub-min", ""},
	}
	for _, testCase := range cases {
		if got := seatFromSession(testCase.session, testCase.room, seats); got != testCase.want {
			t.Fatalf("seatFromSession(%q, %q) = %q, want %q",
				testCase.session, testCase.room, got, testCase.want)
		}
	}
	// A room with no record to read answers an EMPTY seat set, which narrows the reader to the two spelled
	// forms - it never turns a real seat name into a failure.
	if got := seatFromSession("reviewer", "4genthub-min", nil); got != "" {
		t.Fatalf("a bare name with no seat set = %q, want \"\"", got)
	}
	if got := seatFromSession("lead@4genthub-min", "4genthub-min", nil); got != "lead" {
		t.Fatalf("a spelled name with no seat set = %q, want lead", got)
	}
}
