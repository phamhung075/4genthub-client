package clientskills

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitTree writes a tree from relative path -> content, commits it into a repository, and returns the tree
// and the commit. The date is set in 2020 so a fixture fact recorded "now" is unambiguously inside the
// window: a commit dated the same second as the fact would make every window assertion a coin toss.
func gitTree(t *testing.T, files map[string]string) (Tree, string) {
	t.Helper()
	tree := skillTree(t, files)
	git(t, tree.Root, "init", "-q")
	git(t, tree.Root, "add", "-A")
	git(t, tree.Root, "-c", "user.email=fixture@example.com", "-c", "user.name=fixture",
		"commit", "-q", "-m", "fixture tree")
	out := git(t, tree.Root, "rev-parse", "HEAD")
	return tree, out
}

// git runs one git command in dir and returns its trimmed stdout.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z")
	out, err := command.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes one fixture file, creating its directory.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// auditLine is one SEND line of a seat's comm-policy audit.
func auditLine(at string, allowed bool, reason string) string {
	return fmt.Sprintf(`{"At":"%s","From":"agenthub--room--a","To":"agenthub--room--b","Intent":"task","Allowed":%t,"Reason":%q}`,
		at, allowed, reason)
}

// toolLine is one seat's tool-calls line.
func toolLine(at, tool, target, outcome string) string {
	return fmt.Sprintf(`{"at":"%s","seat":"a","tool":%q,"target":%q,"outcome":%q,"ms":3}`,
		at, tool, target, outcome)
}

// jevLine is one Jev decision.
func jevLine(at, kind, id string) string {
	return fmt.Sprintf(`{"timestamp":"%s","kind":%q,"tier":"seat","outcome":"decided","id":%q}`, at, kind, id)
}

// fixtureStateRoot writes the three streams the census reads from the state root and returns that root.
func fixtureStateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	seat := filepath.Join(root, "seats", "room", "a")
	writeFile(t, filepath.Join(seat, "audit.jsonl"), auditLine("2026-10-21T10:00:00Z", false, "no link")+"\n"+
		auditLine("2026-10-21T10:01:00Z", true, "")+"\n")
	writeFile(t, filepath.Join(root, "rooms", "room", "seats", "a", "tool-calls.jsonl"),
		toolLine("2026-10-21T10:02:00Z", "bash", "room queue create --tag defect", "error")+"\n"+
			toolLine("2026-10-21T10:03:00Z", "bash", "go test ./...", "ok")+"\n")
	writeFile(t, filepath.Join(root, jevLogFile), jevLine("2026-10-21T10:04:00Z", "escalation", "jev-1")+"\n")
	return root
}

// fixtureRefusalSpool writes the guard's spool into a repository's git dir and returns the repo.
func fixtureRefusalSpool(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	gitDir := git(t, repo, "rev-parse", "--absolute-git-dir")
	writeFile(t, filepath.Join(gitDir, "parked-diffs", "refusals.jsonl"),
		`{"id":"rf-1","ts":"2026-10-21T10:05:00Z","seat":"a","rule":"docs-only","docs":2,"code":0}`+"\n")
	return repo
}

// The loader reads the four streams and keeps the field each signal names: a refused send's Reason, a Jev
// kind, a tool target, and a refusal's rule. An ALLOWED send is not a fact - the census reports failures,
// and a send the room licensed is not one.
func TestLoadEvidenceReadsTheTypedStreams(t *testing.T) {
	stateRoot := fixtureStateRoot(t)
	repo := fixtureRefusalSpool(t)

	evidence := LoadEvidence(stateRoot, "room", repo)
	if len(evidence.Skipped) > 0 {
		t.Fatalf("skipped = %v, want none", evidence.Skipped)
	}
	values := map[string][]string{}
	for _, fact := range evidence.Facts {
		values[fact.Stream] = append(values[fact.Stream], fact.Value)
	}
	cases := []struct {
		stream string
		want   []string
	}{
		{StreamGuard, []string{"docs-only"}},
		{StreamSend, []string{"no link"}},
		{StreamJEV, []string{"escalation"}},
		{StreamTool, []string{"room queue create --tag defect", "go test ./..."}},
	}
	for _, c := range cases {
		if fmt.Sprint(values[c.stream]) != fmt.Sprint(c.want) {
			t.Errorf("%s facts = %v, want %v", c.stream, values[c.stream], c.want)
		}
	}
}

// The S2 acceptance, first half: a fixture rail and a fixture refusal file drive one `demote` and one
// `promotable`. `demote` is a TRUSTED skill whose declared guard rule was refused after its `since`;
// `promotable` is a PROVISIONAL skill whose `row-tag:` row appeared and whose room landed
// PromotionWindow items, with the two landings recorded BEFORE its window left out of the count.
func TestTheWindowsDemoteAndPromote(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"share/guarded/SKILL.md": skillHeader("guarded", "guard: docs-only"),
		"share/fresh/SKILL.md":   skillHeader("fresh", "row-tag: defect"),
	})
	writeRecordFile(t, tree.Root, "share/guarded", `{"state":"trusted","since":"`+sha+`","change":"fix"}`)

	stateRoot := fixtureStateRoot(t)
	repo := fixtureRefusalSpool(t)
	evidence := LoadEvidence(stateRoot, "room", repo)
	recent := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	evidence.Rows = []RailRow{
		{ID: "qitem-old-1", State: "done", Updated: time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "qitem-old-2", State: "done", Updated: time.Date(2019, 6, 2, 0, 0, 0, 0, time.UTC)},
		{ID: "qitem-1", State: "done", Updated: recent},
		{ID: "qitem-2", State: "done", Updated: recent},
		{ID: "qitem-3", State: "done", Updated: recent},
		{ID: "qitem-4", State: "pending", Updated: recent},
	}

	rows, notes, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(notes) > 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
	got := map[string]Row{}
	for _, row := range rows {
		got[row.Level+"/"+row.Name] = row
	}

	guarded := got["share/guarded"]
	if guarded.Verdict != VerdictDemote {
		t.Errorf("guarded verdict = %q, want demote (occurrences %d)", guarded.Verdict, guarded.Occurrences)
	}
	if guarded.Window != sha {
		t.Errorf("guarded window = %q, want the record's since %q", guarded.Window, sha)
	}
	if len(guarded.Refs) != 1 || guarded.Refs[0] != "guard:rf-1" {
		t.Errorf("guarded refs = %v, want [guard:rf-1]", guarded.Refs)
	}

	fresh := got["share/fresh"]
	if fresh.Verdict != VerdictPromotable {
		t.Errorf("fresh verdict = %q, want promotable (landed %d, occurrences %d)",
			fresh.Verdict, fresh.Landed, fresh.Occurrences)
	}
	if fresh.Landed != 3 {
		t.Errorf("fresh landed = %d, want 3: the two landings before its first commit must not count", fresh.Landed)
	}
	if fresh.Window == "" {
		t.Error("fresh window is empty: an absent record windows from the skill's first commit")
	}
}

// The S2 acceptance, second half: a trusted skill declares `command:`, a TOOL line failed on that command,
// and the skill is NOT demoted. TOOL is counts only - a tool call that failed says nothing about the skill
// that recommended the command, and demoting on it is how a loop learns noise.
func TestTheToolStreamIsCountsOnly(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"share/commanded/SKILL.md": skillHeader("commanded", "command: room queue create"),
	})
	writeRecordFile(t, tree.Root, "share/commanded", `{"state":"trusted","since":"`+sha+`"}`)

	evidence := LoadEvidence(fixtureStateRoot(t), "room", fixtureRefusalSpool(t))
	rows, _, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Verdict != VerdictHold {
		t.Errorf("verdict = %q, want hold: an errored TOOL target never demotes", row.Verdict)
	}
	if row.Occurrences != 0 {
		t.Errorf("occurrences = %d, want 0: a tool line is not an attributed occurrence", row.Occurrences)
	}
	if row.Commands != 1 {
		t.Errorf("commands = %d, want 1: the errored call is still counted", row.Commands)
	}
}

// A stream that could not be read is named, and it is not silence: promoting a skill because its streams
// were unavailable would be a verdict about files nobody opened.
func TestAnUnreadStreamIsNamedAndNotCounted(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"share/fresh/SKILL.md": skillHeader("fresh", "guard: docs-only"),
	})
	writeRecordFile(t, tree.Root, "share/fresh", `{"state":"provisional","since":"`+sha+`"}`)

	evidence := LoadEvidence("", "room", "")
	if len(evidence.Skipped) == 0 {
		t.Fatal("skipped is empty: a state root that was never read must be named")
	}
	rows, notes, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(notes) == 0 || !strings.HasPrefix(notes[0], "streams: ") {
		t.Errorf("notes = %v, want one naming the streams", notes)
	}
	if rows[0].Verdict != VerdictHold {
		t.Errorf("verdict = %q, want hold with no landings measured", rows[0].Verdict)
	}
}
