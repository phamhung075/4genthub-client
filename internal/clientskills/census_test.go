package clientskills

import (
	"os"
	"path/filepath"
	"strings"
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

// The tree-wide listing is what the verb prints: share, the room's own skills, every seat folder's, and the
// client tier's - two levels delivering one name are two rows, because the collision refusal belongs to one
// seat's install. The client tier is last because it is not part of a room's set: it is the tree's fourth
// tier, read for evidence and handed to no seat.
func TestRoomSkillsListsEveryTier(t *testing.T) {
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
	want := []string{"share/a", "room/b", "seat:seat1/c", "seat:seat2/a", "client/c"}
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
	if dir := skills[len(skills)-1].Dir; dir != filepath.Join(tree.Root, ClientDir, "c") {
		t.Fatalf("client skill dir is %s", dir)
	}
}

// TestTheClientTierHasEvidenceAndNoSeatReceivesIt: the tier the client LLM's own instructions live in is read
// by the census - so a commit touching it can cite a record - while a seat's set still excludes it. The two
// questions are asked by two functions, and this is the test that keeps them apart.
func TestTheClientTierHasEvidenceAndNoSeatReceivesIt(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/shared/SKILL.md":     skillHeader("shared"),
		"client/principal/SKILL.md": skillHeader("principal", "guard: docs-only"),
	})
	rows, err := tree.Census("room", nil)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if got, want := strings.Join(rowKeys(rows), ","), "share/shared,client/principal"; got != want {
		t.Fatalf("census rows [%s], want [%s]", got, want)
	}
	for _, row := range rows {
		if row.Name != "principal" {
			continue
		}
		if row.Level != ClientDir {
			t.Fatalf("the client skill's level is %q, want %q", row.Level, ClientDir)
		}
		if row.Path != filepath.Join(tree.Root, ClientDir, "principal") {
			t.Fatalf("the client row's path is %s", row.Path)
		}
		if row.Verdict != VerdictHold {
			t.Fatalf("a client skill with a signal and no record is %q, want %q", row.Verdict, VerdictHold)
		}
	}

	resolved, err := tree.Resolve("room", "seat1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	requireNames(t, resolved, "shared")
	requireAbsent(t, resolved, "principal")
}

// TestAnAbsentOrEmptyClientTierIsNoFault: client/ is optional - a tree without the tier and a tier with no
// skills both read as "no rows", not as an error and not as a silently dropped level - while an entry under it
// that holds no SKILL.md is refused by name, exactly as share/ refuses one.
func TestAnAbsentOrEmptyClientTierIsNoFault(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		tree := skillTree(t, map[string]string{"share/a/SKILL.md": skillHeader("a")})
		skills, err := tree.RoomSkills("room")
		if err != nil {
			t.Fatalf("a tree without client/: %v", err)
		}
		if got, want := strings.Join(roomSkillKeys(skills), ","), "share/a"; got != want {
			t.Fatalf("listed [%s], want [%s]", got, want)
		}
	})
	t.Run("empty", func(t *testing.T) {
		tree := skillTree(t, map[string]string{"share/a/SKILL.md": skillHeader("a")})
		if err := os.MkdirAll(filepath.Join(tree.Root, ClientDir), 0o755); err != nil {
			t.Fatalf("mkdir client/: %v", err)
		}
		skills, err := tree.RoomSkills("room")
		if err != nil {
			t.Fatalf("an empty client/: %v", err)
		}
		if got, want := strings.Join(roomSkillKeys(skills), ","), "share/a"; got != want {
			t.Fatalf("listed [%s], want [%s]", got, want)
		}
	})
	t.Run("not a skill", func(t *testing.T) {
		tree := skillTree(t, map[string]string{
			"share/a/SKILL.md":       skillHeader("a"),
			"client/stray/notes.txt": "no marker",
		})
		_, err := tree.RoomSkills("room")
		if err == nil || !strings.Contains(err.Error(), filepath.Join(ClientDir, "stray")) {
			t.Fatalf("a client entry with no %s must be refused by name: %v", SkillFile, err)
		}
	})
}

// TestAClientSkillWithNoSignalsIsReportedNotDropped: the tier exists so a client skill's own account can be
// asked for, and a skill that declares nothing is `no-signal` - the same verdict the seat tiers get - rather
// than missing from the report.
func TestAClientSkillWithNoSignalsIsReportedNotDropped(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/keep/SKILL.md":       skillHeader("keep"),
		"client/principal/SKILL.md": skillHeader("principal"),
	})
	rows, err := tree.Census("room", nil)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if got, want := strings.Join(rowKeys(rows), ","), "share/keep,client/principal"; got != want {
		t.Fatalf("census rows [%s], want [%s]", got, want)
	}
	client := rows[1]
	if client.Level != ClientDir || client.Verdict != VerdictNoSignal {
		t.Fatalf("client row is %s/%s, want %s/%s", client.Level, client.Verdict, ClientDir, VerdictNoSignal)
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
