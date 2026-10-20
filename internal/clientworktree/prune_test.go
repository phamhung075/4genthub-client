package clientworktree

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// The ruling's prune case: a clean `/tmp/wt-*` goes, and everything else is named and kept.
func TestPruneRemovesOnlyTheWorktreesTheRulingNames(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "lead", Type: "lead"}})
	uniq := strconv.FormatInt(time.Now().UnixNano(), 36)

	// The ruled prefixes are the rule's own key, so the case uses real /tmp paths under them, with a
	// suffix two runs cannot share.
	clean := filepath.Join("/tmp", "wt-"+uniq+"-clean")
	locked := filepath.Join("/tmp", "wt-"+uniq+"-locked")
	dirty := filepath.Join("/tmp", "gate-"+uniq+"-dirty")
	detached := filepath.Join("/tmp", "gate-"+uniq+"-detached")
	prunable := filepath.Join("/tmp", "gate-"+uniq+"-prunable")
	// A checkout that is foreign and NOT under a ruled prefix: it must be listed and never removed.
	foreign := filepath.Join(t.TempDir(), "somebodys-checkout")

	for _, path := range []string{clean, locked, dirty, detached, prunable, foreign} {
		gitIn(t, fixture.main, "worktree", "add", "--detach", path, "main")
		removeTmpWorktree(t, fixture.main, path)
	}
	writeFile(t, filepath.Join(dirty, "junk.txt"), "uncommitted work\n")
	writeFile(t, filepath.Join(detached, "only-here.txt"), "a commit main does not contain\n")
	gitIn(t, detached, "add", "only-here.txt")
	gitIn(t, detached, "commit", "-q", "-m", "a commit main does not contain")
	gitIn(t, fixture.main, "worktree", "lock", locked)
	// A worktree whose directory is gone is what git calls prunable.
	if err := os.RemoveAll(prunable); err != nil {
		t.Fatalf("remove %s: %v", prunable, err)
	}

	var out strings.Builder
	err := runPrune([]string{"--main", fixture.main, fixture.name}, &out)
	if err == nil {
		t.Fatalf("prune left three worktrees behind, so it must fail:\n%s", out.String())
	}
	for _, path := range []string{locked, dirty, detached} {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("the error did not name %s: %v", path, err)
		}
	}
	if strings.Contains(err.Error(), clean) {
		t.Errorf("the error named the worktree prune DID remove: %v", err)
	}

	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Errorf("%s must be removed from the disk: %v", clean, err)
	}
	if _, registered := fixture.entry(t, clean); registered {
		t.Errorf("%s is still in `git worktree list`", clean)
	}
	for _, path := range []string{locked, dirty, detached, foreign} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s must be left in place: %v", path, err)
		}
		if _, registered := fixture.entry(t, path); !registered {
			t.Errorf("%s must still be in `git worktree list`", path)
		}
	}
	if _, registered := fixture.entry(t, prunable); registered {
		t.Errorf("the prunable entry %s is still in the listing; `git worktree prune` must clear it", prunable)
	}

	for _, want := range []string{
		"removed " + clean,
		"refused " + detached,
		"kept " + locked,
		"kept " + dirty,
		"foreign " + foreign,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prune output missing %q:\n%s", want, out.String())
		}
	}
}

// `list` prints the derived worktrees first and every other entry of the listing, labelled foreign.
func TestListPrintsTheDerivedWorktreesAndEveryForeignEntry(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{
		{Key: "lead", Type: "lead"},
		{Key: "go-dev", Type: "developer"},
	})
	stubLinkProviderKey(t)
	var ignored strings.Builder
	if err := fixture.ensure(&ignored); err != nil {
		t.Fatalf("Ensure: %v\n%s", err, ignored.String())
	}
	// One derived worktree taken away again, so the listing has a missing one to report rather than a
	// room that reads as if it had no seat there.
	missing := filepath.Join(fixture.room, "wt", "go-dev")
	if err := os.RemoveAll(missing); err != nil {
		t.Fatalf("remove %s: %v", missing, err)
	}
	gitIn(t, fixture.main, "worktree", "prune")

	foreign := filepath.Join(t.TempDir(), "somebodys-checkout")
	gitIn(t, fixture.main, "worktree", "add", "--detach", foreign, "main")
	removeTmpWorktree(t, fixture.main, foreign)

	var out strings.Builder
	if err := runList([]string{"--main", fixture.main, fixture.name}, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{
		"derived " + filepath.Join(fixture.room, "wt", "lead") + " " + fixture.name + "/lead",
		"derived " + missing + " missing",
		"derived " + filepath.Join(fixture.room, "wt", "integration") + " " + fixture.name + "/integration",
		"foreign " + fixture.main + " main",
		"foreign " + foreign + " detached",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list output missing %q:\n%s", want, out.String())
		}
	}
}

// The seam sits BEHIND Read, so the verbs and a case read the same listing: this case replaces it and
// checks the porcelain parse through Read, with no repository at all.
func TestReadGoesThroughTheSeamAndParsesPorcelain(t *testing.T) {
	// The shape git 2.43 prints, including the two states prune must respect.
	sample := "worktree /room/wt/lead\nHEAD abc123\nbranch refs/heads/room/lead\n\n" +
		"worktree /tmp/gate-x\nHEAD def456\ndetached\nlocked\nprunable gitdir file points to non-existent location\n"
	original := gitWorktrees
	gitWorktrees = func(mainRoot string) ([]Entry, error) {
		if mainRoot != "/main" {
			t.Errorf("Read called the seam with %q, want the main root it was given", mainRoot)
		}
		return parseWorktreeList(sample), nil
	}
	t.Cleanup(func() { gitWorktrees = original })

	entries, err := Read("/main")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []Entry{
		{Path: "/room/wt/lead", Branch: "room/lead"},
		{Path: "/tmp/gate-x", Detached: true, Locked: true, Prunable: true},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("Read = %+v, want %+v", entries, want)
	}
}

// The prefix rule itself, spelled out: only a DIRECT child of /tmp whose name begins wt- or gate- is a
// worktree prune may remove.
func TestRuledForRemovalIsThePrefixRule(t *testing.T) {
	for path, want := range map[string]bool{
		"/tmp/wt-x":                    true,
		"/tmp/gate-x":                  true,
		"/tmp/wt-x/nested":             false,
		"/tmp/wtx":                     false,
		"/tmp/other/wt-x":              false,
		"/home/someone/__projects__/x": false,
		"/tmp":                         false,
	} {
		if got := ruledForRemoval(path); got != want {
			t.Errorf("ruledForRemoval(%q) = %v, want %v", path, got, want)
		}
	}
}
