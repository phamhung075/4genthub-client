package clientskills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillHeader is the minimal SKILL.md a fixture writes, with an optional signals block.
func skillHeader(name string, signals ...string) string {
	body := "---\nname: " + name + "\ndescription: a fixture skill\n"
	if len(signals) > 0 {
		body += "signals:\n"
		for _, signal := range signals {
			body += "  - " + signal + "\n"
		}
	}
	return body + "---\n\n# " + name + "\n"
}

func writeRecordFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel), RecordFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The record is what travels with a skill: absent means provisional-and-unset, and a written record reads
// back exactly what was written.
func TestAnAbsentRecordIsProvisionalAndAWrittenOneRoundTrips(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/plain/SKILL.md": skillHeader("plain"),
	})
	dir := filepath.Join(tree.Root, "share", "plain")

	record, present, err := ReadRecord(dir)
	if err != nil {
		t.Fatalf("read absent record: %v", err)
	}
	if present {
		t.Fatal("a skill with no record.json reported one present")
	}
	if record.State != StateProvisional || record.Since != "" {
		t.Fatalf("absent record read as %+v, want provisional with no since", record)
	}

	want := Record{
		State:    StateTrusted,
		Since:    "c8a73ce3",
		Change:   ChangeFix,
		Evidence: []string{"qitem-20261021154233-69a3129a24cf"},
		Risk:     "the census may promote on evidence that only looks typed",
	}
	if err := WriteRecord(dir, want); err != nil {
		t.Fatalf("write record: %v", err)
	}
	got, present, err := ReadRecord(dir)
	if err != nil || !present {
		t.Fatalf("read back: present=%v err=%v", present, err)
	}
	if got.State != want.State || got.Since != want.Since || got.Change != want.Change ||
		got.Risk != want.Risk || strings.Join(got.Evidence, ",") != strings.Join(want.Evidence, ",") {
		t.Fatalf("read back %+v, want %+v", got, want)
	}
}

// An unreadable record is a refusal, never the default: a decision the census cannot parse must not read
// as "no decision was taken".
func TestAMalformedOrInvalidRecordIsRefused(t *testing.T) {
	cases := map[string]string{
		"unknown state":  `{"state":"blessed"}`,
		"unknown change": `{"state":"trusted","change":"rewritten"}`,
		"bad since":      `{"state":"trusted","since":"main"}`,
		"unknown field":  `{"state":"trusted","counter":7}`,
		"blank evidence": `{"state":"trusted","evidence":[" "]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			tree := skillTree(t, map[string]string{"share/s/SKILL.md": skillHeader("s")})
			writeRecordFile(t, tree.Root, "share/s", body)
			_, _, err := ReadRecord(filepath.Join(tree.Root, "share", "s"))
			if err == nil {
				t.Fatalf("record %s was accepted", body)
			}
		})
	}
}

// A record is written only beside a SKILL.md: a mistyped path must not grow a record of its own.
func TestWriteRecordRefusesADirectoryThatIsNotASkill(t *testing.T) {
	tree := skillTree(t, map[string]string{"share/s/SKILL.md": skillHeader("s")})
	err := WriteRecord(filepath.Join(tree.Root, "share", "typo"), Record{State: StateTrusted})
	if err == nil {
		t.Fatal("a record was written into a directory holding no SKILL.md")
	}
}

// The declared signals are the join keys: five closed kinds, parsed from the front matter, sorted so two
// runs of a report are comparable.
func TestSignalsAreParsedFromTheFrontMatter(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/s/SKILL.md": skillHeader("s",
			`row-tag: defect`,
			`guard: docs-only`,
			`command: "room queue create"`,
			`send: no link`,
			`jev: refusal`,
		),
	})
	signals, err := Skill{Name: "s", Dir: filepath.Join(tree.Root, "share", "s")}.Signals()
	if err != nil {
		t.Fatalf("parse signals: %v", err)
	}
	want := []string{
		"command: room queue create",
		"guard: docs-only",
		"jev: refusal",
		"row-tag: defect",
		"send: no link",
	}
	if len(signals) != len(want) {
		t.Fatalf("parsed %d signals, want %d: %v", len(signals), len(want), signals)
	}
	for i, signal := range signals {
		if signal.String() != want[i] {
			t.Fatalf("signal %d is %q, want %q", i, signal.String(), want[i])
		}
	}
}

// A skill with no front matter, or no signals key, declares nothing - which is a finding, not a parse
// error. An unknown kind IS an error: the vocabulary is closed, so a typo cannot invent a stream.
func TestSignalParsingAcceptsSilenceAndRefusesUnknownKinds(t *testing.T) {
	if signals, err := parseSignals([]byte("# no front matter\n")); err != nil || signals != nil {
		t.Fatalf("no front matter gave %v, %v; want nil, nil", signals, err)
	}
	if signals, err := parseSignals([]byte("---\nname: s\n---\n")); err != nil || signals != nil {
		t.Fatalf("no signals key gave %v, %v; want nil, nil", signals, err)
	}
	if _, err := parseSignals([]byte("---\nsignals:\n  - defect: x\n---\n")); err == nil {
		t.Fatal("an unknown signal kind was accepted")
	}
	if _, err := parseSignals([]byte("---\nsignals:\n  - guard:\n---\n")); err == nil {
		t.Fatal("a signal with no value was accepted")
	}
}

// TestATrailingSecondRecordIsRefused: one value per file. A second value is a record written twice or two
// pasted together, and acting on the first while dropping the rest is how a decision goes silently missing
// (the S1 verdict's trailing-JSON finding).
func TestATrailingSecondRecordIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, SkillFile), "---\nname: x\n---\nbody\n")
	writeFile(t, filepath.Join(dir, RecordFile),
		"{\"state\":\"provisional\"}\n{\"state\":\"trusted\"}\n")

	_, present, err := ReadRecord(dir)
	if err == nil {
		t.Fatalf("a record with a second JSON value was accepted (present=%v)", present)
	}
	if !strings.Contains(err.Error(), "more than one record") {
		t.Fatalf("the refusal must say what it found: %v", err)
	}
}

// TestAQueueLookupThatCouldNotBeMadeIsNotBroken: the daemon's own "unknown qitem_id" is CHECKED and absent;
// anything else is a lookup nobody could make, and reading it as broken would fail a record for a broken pipe
// (the S1 verdict's minor finding).
func TestAQueueLookupThatCouldNotBeMadeIsNotBroken(t *testing.T) {
	absent := queueLookup("qitem-1", errors.New("unknown qitem_id 'qitem-1'"))
	if !absent.Checked || absent.Found {
		t.Fatalf("the daemon's absent row must read checked-and-absent: %+v", absent)
	}
	broken := queueLookup("qitem-1", errors.New("dial unix: connection refused"))
	if broken.Checked || broken.Found {
		t.Fatalf("a failed lookup must read unchecked: %+v", broken)
	}
	if !strings.Contains(broken.Reason, "could not be looked up") {
		t.Fatalf("the unchecked answer must say why: %q", broken.Reason)
	}
}
