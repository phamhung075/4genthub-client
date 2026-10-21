package clientskills

import (
	"path/filepath"
	"testing"
)

// fakeResolver answers evidence lookups from a set: found ids resolve, every other id is a definite miss,
// and "skip" marks an id the checker could not look at.
func fakeResolver(found map[string]bool, skip map[string]bool) Resolver {
	return func(id string) Resolution {
		if skip[id] {
			return Resolution{Reason: "not checked"}
		}
		if found[id] {
			return Resolution{Checked: true, Found: true}
		}
		return Resolution{Checked: true}
	}
}

// The acceptance of S1: one case per verdict. hold is a skill with declared signals and a clean record;
// no-signal is a skill that declares none; broken-record is a record citing an id that does not resolve.
// An id the checker could NOT look at is deliberately not broken - "not found" and "not looked for" are
// different facts, and conflating them would accuse a skill of a fault the checker never established.
func TestCensusDecidesTheThreeVerdicts(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/held/SKILL.md":          skillHeader("held", "guard: docs-only"),
		"share/silent/SKILL.md":        skillHeader("silent"),
		"share/dead/SKILL.md":          skillHeader("dead", "row-tag: defect"),
		"rooms/room/seat/own/SKILL.md": skillHeader("own", "command: room queue create"),
	})
	writeRecordFile(t, tree.Root, "share/held", `{
  "state": "trusted",
  "since": "c8a73ce3",
  "change": "fix",
  "evidence": ["qitem-20261021154233-69a3129a24cf"],
  "risk": "one line"
}`)
	writeRecordFile(t, tree.Root, "share/dead", `{
  "state": "trusted",
  "evidence": ["qitem-20260101000000-doesnotexist"]
}`)

	resolver := fakeResolver(
		map[string]bool{"qitem-20261021154233-69a3129a24cf": true},
		map[string]bool{},
	)
	rows, err := tree.Census("room", resolver)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	got := map[string]Row{}
	for _, row := range rows {
		got[row.Level+"/"+row.Name] = row
	}
	cases := []struct {
		key     string
		verdict string
		state   string
	}{
		{"share/held", VerdictHold, StateTrusted},
		{"share/silent", VerdictNoSignal, StateProvisional},
		{"share/dead", VerdictBrokenRecord, StateTrusted},
		{"seat:seat/own", VerdictHold, StateProvisional},
	}
	for _, c := range cases {
		row, ok := got[c.key]
		if !ok {
			t.Fatalf("no row for %s; rows: %v", c.key, rowKeys(rows))
		}
		if row.Verdict != c.verdict {
			t.Fatalf("%s: verdict %q, want %q", c.key, row.Verdict, c.verdict)
		}
		if row.State != c.state {
			t.Fatalf("%s: state %q, want %q", c.key, row.State, c.state)
		}
	}
	if dead := got["share/dead"]; len(dead.Broken) != 1 ||
		dead.Broken[0] != "qitem-20260101000000-doesnotexist" {
		t.Fatalf("dead record reported broken=%v, want the one dead id", dead.Broken)
	}
}

// An id the checker could not look at leaves the verdict alone and is NOT listed as broken.
func TestAnUncheckedEvidenceIDIsNotBroken(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/s/SKILL.md": skillHeader("s", "jev: refusal"),
	})
	writeRecordFile(t, tree.Root, "share/s", `{"state":"trusted","evidence":["qitem-20260101000000-x"]}`)
	rows, err := tree.Census("room", fakeResolver(map[string]bool{},
		map[string]bool{"qitem-20260101000000-x": true}))
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("census returned %d rows, want 1", len(rows))
	}
	if rows[0].Verdict != VerdictHold || len(rows[0].Broken) != 0 {
		t.Fatalf("unchecked id gave verdict %q broken %v, want hold and none", rows[0].Verdict, rows[0].Broken)
	}
}

// A record whose JSON does not parse is broken-record with the fault named: an unreadable decision is not
// the same as no decision, and the census says which.
func TestAnUnreadableRecordIsBrokenWithItsFault(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/s/SKILL.md": skillHeader("s", "guard: docs-only"),
	})
	writeRecordFile(t, tree.Root, "share/s", `{"state":"trusted",`)
	rows, err := tree.Census("room", nil)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if rows[0].Verdict != VerdictBrokenRecord || rows[0].Fault == "" {
		t.Fatalf("got verdict %q fault %q, want broken-record with a fault", rows[0].Verdict, rows[0].Fault)
	}
}

// The room-wide listing is what the verb prints: share, the room's own skills, and every seat folder's -
// two levels delivering one name are two rows, because the collision refusal belongs to one seat's install.
func TestRoomSkillsListsShareRoomAndEverySeat(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/a/SKILL.md":            skillHeader("a"),
		"rooms/room/b/SKILL.md":       skillHeader("b"),
		"rooms/room/seat1/c/SKILL.md": skillHeader("c"),
		"rooms/room/seat2/a/SKILL.md": skillHeader("a"),
		"rooms/other/z/SKILL.md":      skillHeader("z"),
		"client/c/SKILL.md":           skillHeader("c"),
	})
	skills, err := tree.RoomSkills("room")
	if err != nil {
		t.Fatalf("room skills: %v", err)
	}
	want := []string{"share/a", "room/b", "seat:seat1/c", "seat:seat2/a"}
	if len(skills) != len(want) {
		t.Fatalf("listed %d skills %v, want %v", len(skills), roomSkillKeys(skills), want)
	}
	for i, skill := range skills {
		if got := skill.Level + "/" + skill.Name; got != want[i] {
			t.Fatalf("skill %d is %s, want %s", i, got, want[i])
		}
	}
	if dir := skills[0].Dir; dir != filepath.Join(tree.Root, "share", "a") {
		t.Fatalf("share skill dir is %s", dir)
	}
}

func rowKeys(rows []Row) []string {
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Level+"/"+row.Name)
	}
	return keys
}

func roomSkillKeys(skills []RoomSkill) []string {
	keys := make([]string, 0, len(skills))
	for _, skill := range skills {
		keys = append(keys, skill.Level+"/"+skill.Name)
	}
	return keys
}
