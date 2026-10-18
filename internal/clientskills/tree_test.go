package clientskills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// skillTree writes a tree from relative path -> content and returns it. Every skill in a fixture is
// written WITH its SKILL.md marker: the marker is the rule the resolver applies, so a fixture that
// forgot one would be testing a different shape.
func skillTree(t *testing.T, files map[string]string) Tree {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return Tree{Root: root}
}

func skillNames(skills []Skill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}

func requireNames(t *testing.T, got []Skill, want ...string) {
	t.Helper()
	have := strings.Join(skillNames(got), ",")
	if have != strings.Join(want, ",") {
		t.Fatalf("resolved [%s], want [%s]", have, strings.Join(want, ","))
	}
}

func requireAbsent(t *testing.T, got []Skill, unwanted ...string) {
	t.Helper()
	have := strings.Join(skillNames(got), ",")
	for _, name := range unwanted {
		if strings.Contains(have, name) {
			t.Fatalf("resolved [%s], which carries %q", have, name)
		}
	}
}

// The acceptance this package exists for: a seat of one room receives share/, its own room's room-level
// skills and its own seat-level skills, and NOTHING from another room, from another seat of its own
// room, or from client/ (which is the LLM running the client, not a seat's skill set).
func TestASeatReceivesItsOwnRoomAndItsOwnSeatAndNothingElse(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/shared-skill/SKILL.md":                    "# every room",
		"rooms/alpha/room-skill/SKILL.md":                "# every alpha seat",
		"rooms/alpha/dev/seat-skill/SKILL.md":            "# dev only",
		"rooms/alpha/reviewer/other-seat-skill/SKILL.md": "# reviewer only",
		"rooms/beta/beta-skill/SKILL.md":                 "# beta only",
		"client/client-skill/SKILL.md":                   "# the LLM running the client",
	})

	got, err := tree.Resolve("alpha", "dev")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	requireNames(t, got, "room-skill", "seat-skill", "shared-skill")
	requireAbsent(t, got, "other-seat-skill", "beta-skill", "client-skill")
}

// The loader's one rule, and what it decides: an entry under rooms/<room>/ holding SKILL.md is a
// ROOM-LEVEL skill, and one without it is a SEAT FOLDER. The marker decides, never the name - so a
// room-level skill that happens to be called "dev" is still a skill every seat of that room receives,
// and its directory is not descended into as a seat folder.
func TestTheSkillMarkerDecidesAndNotTheName(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/plain/SKILL.md":            "# plain",
		"rooms/alpha/dev/SKILL.md":        "# a room-level skill named dev",
		"rooms/alpha/qa/qa-only/SKILL.md": "# qa's own",
	})

	dev, err := tree.Resolve("alpha", "dev")
	if err != nil {
		t.Fatalf("resolve dev: %v", err)
	}
	requireNames(t, dev, "dev", "plain")
	requireAbsent(t, dev, "qa-only")

	qa, err := tree.Resolve("alpha", "qa")
	if err != nil {
		t.Fatalf("resolve qa: %v", err)
	}
	requireNames(t, qa, "dev", "plain", "qa-only")
}

// A seat folder gives its skills to that seat alone.
func TestASeatFolderGivesItsSkillsToThatSeatAlone(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/plain/SKILL.md":              "# plain",
		"rooms/alpha/room-skill/SKILL.md":   "# all alpha seats",
		"rooms/alpha/dev/dev-only/SKILL.md": "# dev",
		"rooms/alpha/qa/qa-only/SKILL.md":   "# qa",
	})

	dev, err := tree.Resolve("alpha", "dev")
	if err != nil {
		t.Fatalf("resolve dev: %v", err)
	}
	requireNames(t, dev, "dev-only", "plain", "room-skill")
	requireAbsent(t, dev, "qa-only")

	qa, err := tree.Resolve("alpha", "qa")
	if err != nil {
		t.Fatalf("resolve qa: %v", err)
	}
	requireNames(t, qa, "plain", "qa-only", "room-skill")
	requireAbsent(t, qa, "dev-only")
}

// A copy at two levels is not allowed. The ruling's rule is that each skill lives in exactly one place,
// which leaves the loader's "more specific replaces shared" override with nothing to do - so a name
// delivered by two levels is REFUSED, by name, with both copies given. Silently preferring the more
// specific one is what this refuses: the seat would receive a set that differs from the tree an operator
// is looking at, and no one would be told.
func TestACopyAtTwoLevelsIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  map[string]string
		levels []string
	}{
		{
			name: "share and the room's room-level skills",
			files: map[string]string{
				"share/duplicated/SKILL.md":       "# the shared copy",
				"rooms/alpha/duplicated/SKILL.md": "# the room's copy",
			},
			levels: []string{"share", "room"},
		},
		{
			name: "share and the seat's own skills",
			files: map[string]string{
				"share/duplicated/SKILL.md":           "# the shared copy",
				"rooms/alpha/dev/duplicated/SKILL.md": "# the seat's copy",
			},
			levels: []string{"share", "seat"},
		},
		{
			name: "the room's room-level skills and the seat's own",
			files: map[string]string{
				"share/plain/SKILL.md":                "# an unrelated shared skill, so the tree is a tree",
				"rooms/alpha/duplicated/SKILL.md":     "# the room's copy",
				"rooms/alpha/dev/duplicated/SKILL.md": "# the seat's copy",
			},
			levels: []string{"room", "seat"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := skillTree(t, tc.files)

			_, err := tree.Resolve("alpha", "dev")
			if err == nil {
				t.Fatal("a name delivered by two levels was resolved instead of refused")
			}
			for _, want := range append([]string{"duplicated", SkillFile}, tc.levels...) {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %q, want it to name %q", err, want)
				}
			}
			var coded *clientcmd.CodedError
			if !errors.As(err, &coded) || coded.Code != clientcmd.ExitUsage {
				t.Fatalf("err = %v, want a usage refusal", err)
			}
		})
	}
}

// A room with no room-specific skills is normal: it still receives share/.
func TestARoomWithNoRoomTreeStillGetsShare(t *testing.T) {
	tree := skillTree(t, map[string]string{"share/plain/SKILL.md": "# plain"})

	got, err := tree.Resolve("alpha", "dev")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	requireNames(t, got, "plain")
}

// The shapes the tree that actually ships has, measured rather than assumed: `git ls-tree HEAD` carries
// ZERO paths under skills/rooms, while the checkout on disk HAS an empty skills/rooms/ directory - git
// cannot carry an empty directory, so the live tree is not the shape a fresh clone has. Empty must read
// as ABSENT: Require accepts the root (share/ is the check, not rooms/), Resolve returns the shared
// skills alone with no error, and nothing is written into the tree. rooms/ is empty BY DECISION - the
// classification found zero room-level and zero seat-level skills among the shipped skills - so a seat
// that failed on it would fail on the tree the client itself ships.
func TestAnEmptyRoomsDirectoryReadsAsAbsent(t *testing.T) {
	build := func(t *testing.T) Tree {
		t.Helper()
		return skillTree(t, map[string]string{"share/plain/SKILL.md": "# plain"})
	}
	t.Run("no rooms/ at all", func(t *testing.T) {
		tree := build(t)
		if err := tree.Require(); err != nil {
			t.Fatalf("Require refused a share-only tree: %v", err)
		}
		got, err := tree.Resolve("alpha", "dev")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		requireNames(t, got, "plain")
		if _, err := os.Stat(filepath.Join(tree.Root, RoomsDir)); !os.IsNotExist(err) {
			t.Errorf("stat rooms/ = %v, want it still absent: the resolver must not create it", err)
		}
	})
	t.Run("rooms/ exists and holds nothing", func(t *testing.T) {
		tree := build(t)
		if err := os.Mkdir(filepath.Join(tree.Root, RoomsDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := tree.Require(); err != nil {
			t.Fatalf("Require refused a tree whose rooms/ is empty: %v", err)
		}
		got, err := tree.Resolve("alpha", "dev")
		if err != nil {
			t.Fatalf("resolve with an empty rooms/: %v", err)
		}
		requireNames(t, got, "plain")
	})
	t.Run("rooms/<room>/ exists and holds nothing", func(t *testing.T) {
		tree := build(t)
		if err := os.MkdirAll(filepath.Join(tree.Root, RoomsDir, "alpha"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := tree.Resolve("alpha", "dev")
		if err != nil {
			t.Fatalf("resolve with an empty rooms/<room>/: %v", err)
		}
		requireNames(t, got, "plain")
	})
}

// The two malformed shapes are refused by name rather than skipped: a directory in share/ that is not a
// skill, and a directory inside the seat's own folder that is not a skill.
func TestADirectoryInShareWithoutTheMarkerIsRefused(t *testing.T) {
	tree := skillTree(t, map[string]string{"share/not-a-skill/notes.md": "no marker here"})

	_, err := tree.Resolve("alpha", "dev")
	if err == nil || !strings.Contains(err.Error(), SkillFile) {
		t.Fatalf("err = %v, want a refusal naming %s", err, SkillFile)
	}
}

func TestAMalformedSeatFolderIsRefused(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/plain/SKILL.md":            "# plain",
		"rooms/alpha/dev/broken/notes.md": "no marker here",
	})

	_, err := tree.Resolve("alpha", "dev")
	if err == nil || !strings.Contains(err.Error(), SkillFile) {
		t.Fatalf("err = %v, want a refusal naming %s", err, SkillFile)
	}
}

// Another seat's folder is not even read - so this seat's resolution cannot fail on a malformed tree
// that belongs to somebody else, which is the same isolation the leak test asserts from the other side.
func TestAnotherSeatsFolderIsNotEvenRead(t *testing.T) {
	tree := skillTree(t, map[string]string{
		"share/plain/SKILL.md":           "# plain",
		"rooms/alpha/qa/broken/notes.md": "another seat's malformed folder",
	})

	got, err := tree.Resolve("alpha", "dev")
	if err != nil {
		t.Fatalf("resolve dev: %v", err)
	}
	requireNames(t, got, "plain")
}

func TestRequireRefusesARootThatIsNotTheTree(t *testing.T) {
	if err := (Tree{Root: t.TempDir()}).Require(); err == nil {
		t.Fatal("a directory with no share/ was accepted as the tree")
	}
}

// The tree is found by flag, then by environment, then beside the executable - the client directory a
// user who has only the binary is told to put it in.
