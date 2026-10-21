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
func jevLine(at, kind, tier string) string {
	return fmt.Sprintf(`{"timestamp":"%s","kind":%q,"tier":%q,"outcome":"decided"}`, at, kind, tier)
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
	writeFile(t, filepath.Join(root, jevLogFile), jevLine("2026-10-21T10:04:00Z", "escalation", "seat")+"\n")
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
	if len(evidence.Skips) > 0 {
		t.Fatalf("skips = %v, want none", evidence.Skips)
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
	old := time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)
	evidence.Rows = []RailRow{
		{ID: "qitem-old-1", State: "done", Tags: []string{"defect"}, Created: old, Updated: old},
		{ID: "qitem-old-2", State: "done", Tags: []string{"defect"}, Created: old, Updated: old},
		{ID: "qitem-1", State: "done", Tags: []string{"defect"}, Created: recent, Updated: recent},
		{ID: "qitem-2", State: "done", Tags: []string{"defect"}, Created: recent, Updated: recent},
		{ID: "qitem-3", State: "done", Tags: []string{"defect"}, Created: recent, Updated: recent},
		{ID: "qitem-4", State: "pending", Tags: []string{"defect"}, Created: recent, Updated: recent},
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

// TestALandingCountsOnlyForTheSkillWhoseTagsItCarries is the reviewer's change to 7c2c7d4e: the census counted
// every row the room closed, so a provisional skill was promoted on other skills' work. A landing now counts when
// the row carries one of the skill's OWN declared row-tag values - the fact its stream is made of.
//
// Four skills, three closed rows: `defect` claims the row tagged defect, `guard` the row tagged guard, `streamed`
// declares only a guard: rule (no row is its own), and `missing` declares a tag no closed row carries. Landed:
// 1, 1, 0, 0 - the old count answered 3 for each of them.
func TestALandingCountsOnlyForTheSkillWhoseTagsItCarries(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"share/defect/SKILL.md":   skillHeader("defect", "row-tag: defect"),
		"share/guard/SKILL.md":    skillHeader("guard", "row-tag: guard"),
		"share/streamed/SKILL.md": skillHeader("streamed", "guard: docs-only"),
		"share/missing/SKILL.md":  skillHeader("missing", "row-tag: nobody"),
	})
	for _, name := range []string{"defect", "guard", "streamed", "missing"} {
		writeRecordFile(t, tree.Root, "share/"+name, `{"state":"provisional","since":"`+sha+`"}`)
	}
	recent := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

	evidence := LoadEvidence(fixtureStateRoot(t), "room", fixtureRefusalSpool(t))
	evidence.Rows = []RailRow{
		{ID: "qitem-1", State: "done", Tags: []string{"defect", "gate"}, Created: recent, Updated: recent},
		{ID: "qitem-2", State: "done", Tags: []string{"guard"}, Created: recent, Updated: recent},
		{ID: "qitem-3", State: "done", Tags: []string{"other"}, Created: recent, Updated: recent},
	}
	want := map[string]int{"share/defect": 1, "share/guard": 1, "share/streamed": 0, "share/missing": 0}

	rows, _, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	seen := 0
	for _, row := range rows {
		key := row.Level + "/" + row.Name
		expected, ok := want[key]
		if !ok {
			continue
		}
		seen++
		if row.Landed != expected {
			t.Errorf("%s landed = %d, want %d", key, row.Landed, expected)
		}
	}
	if seen != len(want) {
		t.Fatalf("census reported %d of the %d fixture skills", seen, len(want))
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
	// R2 of the signal ruling: a declaration made only of `command:` facts has no failure stream at all, so the
	// verdict is `no-signal` - the one that already means "nobody can hold it to account" - and not `hold`. The
	// errored call is still counted and still never demotes: TOOL is use, never outcome.
	if row.Verdict != VerdictNoSignal {
		t.Errorf("verdict = %q, want no-signal: a command counts use, and a failed use demotes nobody", row.Verdict)
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
	if len(evidence.Skips) == 0 {
		t.Fatal("skips is empty: a state root that was never read must be named")
	}
	rows, notes, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(notes) == 0 || !strings.HasPrefix(notes[0], "unread: ") {
		t.Errorf("notes = %v, want one naming an unread stream", notes)
	}
	if rows[0].Verdict != VerdictHold {
		t.Errorf("verdict = %q, want hold with its stream unread", rows[0].Verdict)
	}
	if len(rows[0].Unread) != 1 || rows[0].Unread[0] != StreamGuard {
		t.Errorf("unread = %v, want [guard]", rows[0].Unread)
	}
}

// A signal that fires on half of its own stream inside the window is a tautology and decides nothing: a tag
// every gate row carries would demote every skill declaring it. The skill keeps `hold` and the signal is
// named in `broad`, so a reader sees why it was not acted on. The floor is BroadMinFacts - the demote case
// above is one refusal out of one, and it must still demote.
func TestASignalThatFiresOnHalfItsStreamIsTooBroad(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"share/tagged/SKILL.md": skillHeader("tagged", "row-tag: gate"),
	})
	writeRecordFile(t, tree.Root, "share/tagged", `{"state":"trusted","since":"`+sha+`"}`)

	recent := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	evidence := Evidence{Rows: []RailRow{
		{ID: "qitem-1", State: "done", Tags: []string{"gate"}, Created: recent, Updated: recent},
		{ID: "qitem-2", State: "done", Tags: []string{"gate"}, Created: recent, Updated: recent},
		{ID: "qitem-3", State: "done", Tags: []string{"defect"}, Created: recent, Updated: recent},
		{ID: "qitem-4", State: "pending", Tags: []string{"defect"}, Created: recent, Updated: recent},
	}}
	rows, _, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	row := rows[0]
	if row.Verdict != VerdictHold {
		t.Errorf("verdict = %q, want hold: a signal matching half its stream is too broad", row.Verdict)
	}
	if len(row.Broad) != 1 || row.Broad[0] != "row-tag: gate" {
		t.Errorf("broad = %v, want [row-tag: gate]", row.Broad)
	}
	if row.Occurrences != 0 {
		t.Errorf("occurrences = %d, want 0: a broad signal decides nothing", row.Occurrences)
	}
}

// A missing stream is not observed, so it decides nothing: the row keeps `hold`, names the stream, and is
// NOT promoted even though three items landed. This is the acceptance's "no guessed verdict" - promotion
// asserts zero occurrences, and zero from a file nobody opened is not evidence.
func TestASkippedStreamIsReportedAndNotJudged(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"share/fresh/SKILL.md": skillHeader("fresh", "send: no link"),
	})
	writeRecordFile(t, tree.Root, "share/fresh", `{"state":"provisional","since":"`+sha+`"}`)

	recent := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	evidence := Evidence{
		Skips: []Skip{{Stream: StreamSend, Reason: "no state root, so the send audit was not read"}},
		Rows: []RailRow{
			{ID: "qitem-1", State: "done", Created: recent, Updated: recent},
			{ID: "qitem-2", State: "done", Created: recent, Updated: recent},
			{ID: "qitem-3", State: "done", Created: recent, Updated: recent},
		},
	}
	rows, notes, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "send audit") {
		t.Errorf("notes = %v, want one naming the send audit", notes)
	}
	if rows[0].Verdict != VerdictHold {
		t.Errorf("verdict = %q, want hold: an unread stream is not silence", rows[0].Verdict)
	}
	if len(rows[0].Unread) != 1 || rows[0].Unread[0] != StreamSend {
		t.Errorf("unread = %v, want [send]", rows[0].Unread)
	}
}

// TestASeatSkillIsJudgedOnItsOwnSeat is R1 of the signal ruling: the same rule refused by two seats is one
// occurrence for the seat that states the rule and two for a skill the room owns. Without the scope a seat skill
// demotes on its neighbours' refusals - measured on 2026-10-21 as go-dev-role 152 facts of which 44 were its own.
func TestASeatSkillIsJudgedOnItsOwnSeat(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"rooms/room/go-dev/role/SKILL.md": skillHeader("role", "guard: env-secret"),
		"share/roomskill/SKILL.md":        skillHeader("roomskill", "guard: env-secret"),
	})
	writeRecordFile(t, tree.Root, "rooms/room/go-dev/role", `{"state":"trusted","since":"`+sha+`"}`)
	writeRecordFile(t, tree.Root, "share/roomskill", `{"state":"trusted","since":"`+sha+`"}`)

	stateRoot := fixtureStateRoot(t)
	repo := fixtureRefusalSpool(t)
	evidence := LoadEvidence(stateRoot, "room", repo)
	at := time.Date(2026, 10, 21, 10, 30, 0, 0, time.UTC)
	evidence.Facts = append(evidence.Facts,
		Fact{Stream: StreamGuard, Value: "env-secret", At: at, Ref: "own", Seat: "go-dev"},
		Fact{Stream: StreamGuard, Value: "env-secret", At: at, Ref: "neighbour", Seat: "reviewer"})

	rows, _, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	got := map[string]Row{}
	for _, row := range rows {
		got[row.Level+"/"+row.Name] = row
	}
	seatRow, ok := got["seat:go-dev/role"]
	if !ok {
		t.Fatalf("no seat row: %v", got)
	}
	// Its own fact only: the reviewer's refusals of the same rule are not failures of this seat's rule, and the
	// fixture's spool line carries another rule.
	if seatRow.Occurrences != 1 || seatRow.Refs[0] != "guard:own" {
		t.Errorf("seat skill = %d occurrences %v, want 1 [guard:own]", seatRow.Occurrences, seatRow.Refs)
	}
	roomRow, ok := got["share/roomskill"]
	if !ok {
		t.Fatalf("no room row: %v", got)
	}
	if roomRow.Occurrences != 2 {
		t.Errorf("room skill occurrences = %d, want 2: the room owns every seat's facts", roomRow.Occurrences)
	}
}

// TestRefusalSpoolsOfTheRoomsWorktreesAreAllRead is R3: a refused commit lands in the git dir of the worktree it
// was attempted in, so a census that read only the caller's checkout saw one seat's refusals out of six.
func TestRefusalSpoolsOfTheRoomsWorktreesAreAllRead(t *testing.T) {
	stateRoot := fixtureStateRoot(t)
	caller := fixtureRefusalSpool(t)

	// One seat's worktree under the room, with its own spool and its own rule.
	wt := filepath.Join(stateRoot, "rooms", "room", "wt", "go-dev")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "init", "-q")
	gitDir := git(t, wt, "rev-parse", "--absolute-git-dir")
	writeFile(t, filepath.Join(gitDir, "parked-diffs", "refusals.jsonl"),
		`{"id":"rf-wt","ts":"2026-10-21T10:06:00Z","seat":"go-dev","rule":"worktree_scope"}`+"\n")

	evidence := LoadEvidence(stateRoot, "room", caller)
	since := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	found := evidence.Occurrences(Signal{Kind: SignalGuard, Value: "worktree-scope"}, since, "go-dev")
	if len(found) != 1 || found[0].Ref != "rf-wt" {
		t.Fatalf("the worktree spool was not read: %+v", found)
	}
	if found[0].Seat != "go-dev" {
		t.Fatalf("the spool line's own seat was not kept: %q", found[0].Seat)
	}
	// And the caller's own spool is still read: the union adds, it does not replace.
	if got := evidence.Occurrences(Signal{Kind: SignalGuard, Value: "docs-only"}, since, ""); len(got) != 1 {
		t.Fatalf("the caller's spool was lost: %+v", got)
	}
}

// TestAHoldRowCitesItsOccurrences: a row that holds on real refusals names them. Before this, only `demote`
// filled Refs, so a skill holding on 152 occurrences showed a number with no id to follow.
func TestAHoldRowCitesItsOccurrences(t *testing.T) {
	tree, sha := gitTree(t, map[string]string{
		"share/holder/SKILL.md": skillHeader("holder", "guard: env-secret"),
	})
	// Provisional: it cannot promote (no landings) and cannot demote (not trusted), so it holds - with refusals.
	writeRecordFile(t, tree.Root, "share/holder", `{"state":"provisional","since":"`+sha+`"}`)

	evidence := LoadEvidence(fixtureStateRoot(t), "room", fixtureRefusalSpool(t))
	at := time.Date(2026, 10, 21, 10, 30, 0, 0, time.UTC)
	evidence.Facts = append(evidence.Facts, Fact{Stream: StreamGuard, Value: "env-secret", At: at, Ref: "rf-hold"})

	rows, _, err := tree.CensusWithEvidence("room", nil, evidence, tree.Root)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Verdict != VerdictHold {
		t.Fatalf("verdict = %q, want hold", row.Verdict)
	}
	if row.Occurrences != 1 || len(row.Refs) != 1 || row.Refs[0] != "guard:rf-hold" {
		t.Errorf("hold = %d occurrences %v, want 1 citing guard:rf-hold", row.Occurrences, row.Refs)
	}
}
