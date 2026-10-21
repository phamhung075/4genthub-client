package clientlifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientskills"
)

// skillsRowFixture drives one room's skills row over a real tree and a census the test wrote down: the row
// is what this slice adds, so its rules are what these cases pin - not the census's own readers.
type skillsRowFixture struct {
	root string
	rows []clientskills.Row
	// fault makes the census answer an error, the case where the row has no census at all.
	fault error
}

func newSkillsRowFixture(t *testing.T, rows ...clientskills.Row) *skillsRowFixture {
	t.Helper()
	root := t.TempDir()
	// The tree must hold share/ - the same test clientskills.Tree.Require makes - so the row reaches the
	// census instead of stopping at "no tree".
	if err := os.MkdirAll(filepath.Join(root, "share"), 0o755); err != nil {
		t.Fatalf("mkdir share: %v", err)
	}
	fixture := &skillsRowFixture{root: root, rows: rows}
	restoreTree := skillsTreeRoot
	skillsTreeRoot = func(string) (string, error) { return root, nil }
	restoreCensus := censusFor
	censusFor = func(string, string, string) ([]clientskills.Row, []string, error) {
		if fixture.fault != nil {
			return nil, nil, fixture.fault
		}
		return fixture.rows, nil, nil
	}
	t.Cleanup(func() { skillsTreeRoot = restoreTree; censusFor = restoreCensus })
	return fixture
}

// TestASkillsRowFailsOnADemoteAndNamesTheRowsThatDidIt: a demote is the loop acting, so the row leaves
// `running`, and the reason names the skill, the signal and the cited row a reader opens.
func TestASkillsRowFailsOnADemoteAndNamesTheRowsThatDidIt(t *testing.T) {
	newSkillsRowFixture(t, clientskills.Row{
		Name: "queue-row-schema", Level: clientskills.ShareDir, State: clientskills.StateTrusted,
		Verdict: clientskills.VerdictDemote, Signals: []string{"row-tag: defect"}, Occurrences: 2,
		Refs: []string{"qitem-1", "qitem-2"}, Window: "abc1234",
	})

	row := skillsRowOf("fixtureroom", "")
	if row.Row.State != clientservices.SkillsDemote {
		t.Fatalf("state = %q, want %q", row.Row.State, clientservices.SkillsDemote)
	}
	reasons := strings.Join(skillsReasonLines(row), "\n")
	for _, want := range []string{"share/queue-row-schema", "row-tag: defect", "qitem-1,qitem-2", "2 time(s)"} {
		if !strings.Contains(reasons, want) {
			t.Fatalf("the demote reason does not name %q:\n%s", want, reasons)
		}
	}
	if down := clientservices.NotRunning([]clientservices.Health{row.Row}); len(down) != 1 {
		t.Fatalf("a demote must fail the row doctor counts as down")
	}
}

// TestASkillsRowFailsOnABrokenRecordAndNamesTheMissingID: a record citing evidence that does not resolve
// fails the row, and the reason is the id - "the record is broken" alone is not actionable.
func TestASkillsRowFailsOnABrokenRecordAndNamesTheMissingID(t *testing.T) {
	newSkillsRowFixture(t, clientskills.Row{
		Name: "go-dev-role", Level: "seat:go-dev", Verdict: clientskills.VerdictBrokenRecord,
		Broken: []string{"qitem-gone"},
	})

	row := skillsRowOf("fixtureroom", "")
	if row.Row.State != clientservices.SkillsBroken {
		t.Fatalf("state = %q, want %q", row.Row.State, clientservices.SkillsBroken)
	}
	reasons := strings.Join(skillsReasonLines(row), "\n")
	if !strings.Contains(reasons, "seat:go-dev/go-dev-role") || !strings.Contains(reasons, "qitem-gone does not resolve") {
		t.Fatalf("the broken-record reason names neither the skill nor the id:\n%s", reasons)
	}
}

// TestAPromotableSkillIsADetailAndNotAFailure: a skill ready for its gate is not a defect in the room, so the
// row stays `running` and the line is a DETAIL - it must not appear among the reasons either.
func TestAPromotableSkillIsADetailAndNotAFailure(t *testing.T) {
	newSkillsRowFixture(t, clientskills.Row{
		Name: "verification-before-completion", Level: clientskills.ShareDir,
		Verdict: clientskills.VerdictPromotable, Landed: 3, Window: "def5678",
	})

	row := skillsRowOf("fixtureroom", "")
	if row.Row.State != clientservices.Running {
		t.Fatalf("state = %q, want %q", row.Row.State, clientservices.Running)
	}
	if reasons := skillsReasonLines(row); len(reasons) != 0 {
		t.Fatalf("a promotable skill must not be a reason the row is down: %v", reasons)
	}
	details := strings.Join(skillsDetailLines(row), "\n")
	if !strings.Contains(details, "promotable") || !strings.Contains(details, "landed 3") {
		t.Fatalf("the promotable detail is missing:\n%s", details)
	}
}

// TestASkillsRowWithNoTreeSaysWhatItCouldNotLookAt: a census over a tree the runtime never installed would
// judge skills no seat receives, so the row reports the input it could not read instead of a verdict.
func TestASkillsRowWithNoTreeSaysWhatItCouldNotLookAt(t *testing.T) {
	newSkillsRowFixture(t)
	skillsTreeRoot = func(string) (string, error) { return "", errors.New("AGENTHUB_CLIENT_SKILLS is unset") }

	row := skillsRowOf("fixtureroom", "")
	if !strings.Contains(row.Note, "AGENTHUB_CLIENT_SKILLS is unset") {
		t.Fatalf("the row must name the input it could not read, got note %q", row.Note)
	}
	if len(row.Demoted)+len(row.Broken) != 0 {
		t.Fatalf("a row with no census reported a failure: %+v", row)
	}
}

// TestASkillsRowSurfacesACensusNote: a stream the census could not read rides beside the row, because a
// census that read nothing must not print as a census that found nothing.
func TestASkillsRowSurfacesACensusNote(t *testing.T) {
	newSkillsRowFixture(t)
	censusFor = func(string, string, string) ([]clientskills.Row, []string, error) {
		return nil, []string{"the room's queue is not reachable, so landings were not counted"}, nil
	}

	row := skillsRowOf("fixtureroom", "")
	if row.Row.State != clientservices.Running {
		t.Fatalf("state = %q, want running", row.Row.State)
	}
	lines := strings.Join(skillsDetailLines(row), "\n")
	if !strings.Contains(lines, "queue is not reachable") {
		t.Fatalf("the census note is not on the row:\n%s", lines)
	}
}
