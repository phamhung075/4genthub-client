package clientworktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// The cases of the 2026-11-01 cleanup, each built as real repositories and real worktrees. Nothing here
// reads this machine's rooms: the store is a temp dir, and the two liveness seams are stubbed so no real
// seat or process can decide a result.

type cleanFixture struct {
	t     *testing.T
	store string
	room  string
	wt    string
	repo  string
}

func newCleanFixture(t *testing.T) *cleanFixture {
	t.Helper()
	requireGit(t)
	stubLiveness(t)
	f := &cleanFixture{t: t, store: t.TempDir(), room: "fixture-room"}
	f.wt = filepath.Join(f.store, "rooms", f.room, "wt")
	mkdirAll(t, f.wt)
	f.repo = f.newRepo("project")
	return f
}

// stubLiveness keeps a case off the machine's own seats, processes and git configuration.
func stubLiveness(t *testing.T) {
	t.Helper()
	originalStatus, originalCwds := runtimeRoomStatus, processCwds
	runtimeRoomStatus = func(string) ([]clientruntime.SeatStatus, error) { return nil, errors.New("no runtime here") }
	processCwds = func() []processCwd { return nil }
	t.Cleanup(func() { runtimeRoomStatus, processCwds = originalStatus, originalCwds })
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("AGENTHUB_SEAT", "")
	t.Setenv("AGENTHUB_ROOM", "")
}

func (f *cleanFixture) newRepo(name string) string {
	f.t.Helper()
	repo := filepath.Join(f.t.TempDir(), name)
	mkdirAll(f.t, repo)
	gitIn(f.t, repo, "init", "-q", "-b", "main")
	writeFile(f.t, filepath.Join(repo, ".gitignore"), "target/\nnode_modules/\nscratch/\n.env\nkeepme/\ngraphify-out/\n")
	gitIn(f.t, repo, "add", ".gitignore")
	gitIn(f.t, repo, "commit", "-q", "-m", "first commit")
	return repo
}

// worktree adds a linked worktree on a new branch at main.
func (f *cleanFixture) worktree(repo, rel, branch string) string {
	f.t.Helper()
	path := filepath.Join(f.wt, rel)
	mkdirAll(f.t, filepath.Dir(path))
	gitIn(f.t, repo, "worktree", "add", "-q", "-b", branch, path, "main")
	return path
}

func (f *cleanFixture) commit(dir, file string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(dir, file), "content of "+file+"\n")
	gitIn(f.t, dir, "add", file)
	gitIn(f.t, dir, "commit", "-q", "-m", "add "+file)
}

// clean runs the verb and answers its output and its parsed report.
func (f *cleanFixture) clean(args ...string) (string, CleanReport, error) {
	f.t.Helper()
	var text strings.Builder
	base := []string{"--state-root", f.store}
	err := runClean(append(append(base, args...), f.room), &text)
	var parsed strings.Builder
	var report CleanReport
	// The dry-run reading of the same scope is not repeated after an apply: the text is what a case wants then.
	if !contains(args, "--apply") {
		if jsonErr := runClean(append(append(append([]string{}, base...), args...), "--json", f.room), &parsed); jsonErr == nil || parsed.Len() > 0 {
			if decodeErr := json.Unmarshal([]byte(parsed.String()), &report); decodeErr != nil {
				f.t.Fatalf("clean --json is not JSON: %v\n%s", decodeErr, parsed.String())
			}
		}
	}
	return text.String(), report, err
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (f *cleanFixture) mustExist(paths ...string) {
	f.t.Helper()
	for _, path := range paths {
		if !exists(path) {
			f.t.Errorf("%s must still be on disk", path)
		}
	}
}

func (f *cleanFixture) mustBeGone(paths ...string) {
	f.t.Helper()
	for _, path := range paths {
		if exists(path) {
			f.t.Errorf("%s must be gone", path)
		}
	}
}

func hasBranch(t *testing.T, repo, name string) bool {
	t.Helper()
	_, err := gitTry(t, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

func decisionFor(report CleanReport, path string) (Tree, bool) {
	for _, tree := range report.Worktrees {
		if tree.Path == path && tree.Action == actionDecide {
			return tree, true
		}
	}
	return Tree{}, false
}

// A clean merged worktree and its merged branch go; the branch's base stays.
func TestCleanRemovesCleanMergedWorktreeAndBranch(t *testing.T) {
	f := newCleanFixture(t)
	done := f.worktree(f.repo, "lead-done", f.room+"/lead-done")

	text, report, err := f.clean()
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if report.Applied || !strings.Contains(text, "would remove "+done) {
		t.Errorf("the dry run did not name the removal:\n%s", text)
	}
	f.mustExist(done)
	if !hasBranch(t, f.repo, f.room+"/lead-done") {
		t.Fatal("a dry run deleted a branch")
	}

	text, _, err = f.clean("--apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, text)
	}
	f.mustBeGone(done)
	if hasBranch(t, f.repo, f.room+"/lead-done") {
		t.Errorf("the merged branch survived:\n%s", text)
	}
	if !hasBranch(t, f.repo, "main") {
		t.Error("the base branch was deleted")
	}
	if out := gitIn(t, f.repo, "worktree", "list", "--porcelain"); strings.Contains(out, "lead-done") {
		t.Errorf("the worktree is still registered:\n%s", out)
	}
}

// The real shape: a hub worktree holding a client worktree on the hub's own gitlink, with a third
// worktree in the hub's ignored scratch. Inner ones go first, and the outer one is then clean.
func TestCleanRemovesNestedWorktreesOfSeveralReposInnerFirst(t *testing.T) {
	stubLiveness(t)
	stubLinkProviderKey(t)
	seats := []clientservices.SeatType{{Key: "dev", Type: "worker"}}
	fixture := newSeatFixture(t, seats)
	roomDir := fixture.room
	originalRoomDir := RoomDir
	t.Cleanup(func() { RoomDir = originalRoomDir })
	var ensured strings.Builder
	if err := Ensure(fixture.main, roomDir, fixture.name, seats, &ensured); err != nil {
		t.Fatalf("Ensure: %v\n%s", err, ensured.String())
	}
	outer := filepath.Join(roomDir, "wt", "dev")
	inner := filepath.Join(outer, clientSubmodule)
	if !exists(filepath.Join(inner, ".git")) {
		t.Fatalf("the fixture has no client link:\n%s", ensured.String())
	}
	// A commit on the client link moves it off the parent's gitlink, which reads as modified in the parent
	// until the inner one is gone. It is merged, so it must not block.
	clientRepo := filepath.Join(fixture.main, clientSubmodule)
	branch := fixture.name + "/dev"
	writeFile(t, filepath.Join(inner, "LINK.md"), "work on the link\n")
	gitIn(t, inner, "add", "LINK.md")
	gitIn(t, inner, "commit", "-q", "-m", "work on the link")
	gitIn(t, clientRepo, "merge", "-q", "--ff-only", branch)
	writeFile(t, filepath.Join(fixture.main, ".gitignore"), "scratch/\n")
	gitIn(t, fixture.main, "add", ".gitignore")
	gitIn(t, fixture.main, "commit", "-q", "-m", "ignore scratch")
	gitIn(t, outer, "merge", "-q", "--ff-only", "main")
	scratch := filepath.Join(outer, "scratch", "other")
	gitIn(t, fixture.main, "worktree", "add", "-q", "--detach", scratch, "main")

	var text strings.Builder
	if err := runClean([]string{"--state-root", fixture.store, "--apply", fixture.name}, &text); err != nil {
		t.Fatalf("apply: %v\n%s", err, text.String())
	}
	output := text.String()
	if !strings.Contains(output, "removed "+outer+" (") {
		t.Fatalf("the outer worktree stayed:\n%s", output)
	}
	if strings.Index(output, "removed "+inner) > strings.Index(output, "removed "+outer+" (") ||
		strings.Index(output, "removed "+scratch) > strings.Index(output, "removed "+outer+" (") {
		t.Errorf("an inner worktree went after the one holding it:\n%s", output)
	}
	if exists(outer) {
		t.Errorf("%s must be gone", outer)
	}
	if hasBranch(t, fixture.main, branch) || hasBranch(t, clientRepo, branch) {
		t.Errorf("the seat's branch survived in one of the two repositories:\n%s", output)
	}
}

// A nested worktree that stays holds its parent, and it holds it even inside an ignored folder, the one
// place `git worktree remove` would delete it without a word.
func TestCleanNeverRemovesAWorktreeHoldingOneThatStays(t *testing.T) {
	f := newCleanFixture(t)
	outer := f.worktree(f.repo, "lead", f.room+"/lead")
	inner := f.worktree(f.repo, "lead/scratch/inner", f.room+"/inner")
	writeFile(t, filepath.Join(inner, "work.txt"), "uncommitted\n")

	text, _, err := f.clean("--apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, text)
	}
	f.mustExist(outer, filepath.Join(inner, "work.txt"))
	if !strings.Contains(text, "holds "+inner) {
		t.Errorf("the outer decision does not name the inner worktree:\n%s", text)
	}
}

// *.obsolete with uncommitted work: never deleted, and it says why.
func TestCleanNeverTouchesAnObsoleteWorktree(t *testing.T) {
	f := newCleanFixture(t)
	retired := f.worktree(f.repo, "dev.obsolete", f.room+"/old")
	writeFile(t, filepath.Join(retired, "wip.txt"), "uncommitted\n")
	clean := f.worktree(f.repo, "clean.obsolete", f.room+"/old2")
	cache := filepath.Join(clean, "node_modules")
	writeFile(t, filepath.Join(cache, "pkg.js"), "x\n")

	text, report, err := f.clean()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{retired, clean} {
		tree, ok := decisionFor(report, path)
		if !ok || !strings.Contains(strings.Join(tree.Reasons, ";"), ".obsolete") {
			t.Errorf("%s is not a decision naming .obsolete:\n%s", path, text)
		}
	}
	if _, _, err := f.clean("--apply"); err != nil {
		t.Fatal(err)
	}
	f.mustExist(filepath.Join(retired, "wip.txt"), clean, cache)
}

// A plain directory has nothing to say what it holds; an empty one holds nothing.
func TestCleanKeepsAPlainDirectoryWithFiles(t *testing.T) {
	f := newCleanFixture(t)
	plain := filepath.Join(f.wt, "lead-BoneSolverStability")
	writeFile(t, filepath.Join(plain, "notes.txt"), "what is this\n")
	empty := filepath.Join(f.wt, "lead-empty", "sub")
	mkdirAll(t, empty)

	text, report, err := f.clean()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decisionFor(report, plain); !ok {
		t.Errorf("the plain directory is not a decision:\n%s", text)
	}
	if text, _, err = f.clean("--apply"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustExist(filepath.Join(plain, "notes.txt"))
	f.mustBeGone(filepath.Join(f.wt, "lead-empty"))
}

// A detached commit no ref reaches: a branch first, and only then the removal.
func TestCleanPreservesAnUnreachableDetachedCommit(t *testing.T) {
	f := newCleanFixture(t)
	detached := filepath.Join(f.wt, "reviewer")
	gitIn(t, f.repo, "worktree", "add", "-q", "--detach", detached, "main")
	f.commit(detached, "only-here.txt")
	sha := gitIn(t, detached, "rev-parse", "HEAD")
	preserved := "preserved/" + f.room + "/reviewer-" + sha[:7]

	text, _, err := f.clean()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "would preserve commit "+sha+" as branch "+preserved) {
		t.Errorf("the dry run did not name the preservation:\n%s", text)
	}
	if hasBranch(t, f.repo, preserved) {
		t.Fatal("a dry run made the branch")
	}

	if text, _, err = f.clean("--apply"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustBeGone(detached)
	if !hasBranch(t, f.repo, preserved) {
		t.Fatalf("the commit was not preserved:\n%s", text)
	}
	if got := gitIn(t, f.repo, "rev-parse", "refs/heads/"+preserved); got != sha {
		t.Errorf("the preserved branch is at %s, want %s", got, sha)
	}
	// It stays somebody's decision: the next clean reads it as unmerged and leaves it.
	if text, _, err = f.clean("--apply"); err != nil {
		t.Fatal(err)
	}
	if !hasBranch(t, f.repo, preserved) {
		t.Errorf("a later clean deleted the preserved branch:\n%s", text)
	}
}

// A detached HEAD a branch already reaches has nothing to preserve.
func TestCleanMakesNoRefForAReachableDetachedHead(t *testing.T) {
	f := newCleanFixture(t)
	detached := filepath.Join(f.wt, "reviewer")
	gitIn(t, f.repo, "worktree", "add", "-q", "--detach", detached, "main")
	text, _, err := f.clean("--apply")
	if err != nil {
		t.Fatal(err)
	}
	f.mustBeGone(detached)
	if out := gitIn(t, f.repo, "for-each-ref", "refs/heads/preserved"); out != "" {
		t.Errorf("a preservation branch was made for a reachable commit: %s\n%s", out, text)
	}
}

// Each thing the verb must refuse, one worktree each, all left as they were.
func TestCleanRefusesWorkItCannotRebuild(t *testing.T) {
	f := newCleanFixture(t)

	dirty := f.worktree(f.repo, "dirty", f.room+"/dirty")
	writeFile(t, filepath.Join(dirty, ".gitignore"), "changed\n")
	untracked := f.worktree(f.repo, "untracked", f.room+"/untracked")
	writeFile(t, filepath.Join(untracked, "new.txt"), "new\n")
	ahead := f.worktree(f.repo, "ahead", f.room+"/ahead")
	f.commit(ahead, "unmerged.txt")
	locked := f.worktree(f.repo, "locked", f.room+"/locked")
	gitIn(t, f.repo, "worktree", "lock", locked)
	t.Cleanup(func() { _, _ = gitTry(t, f.repo, "worktree", "unlock", locked) })
	ignored := f.worktree(f.repo, "ignored", f.room+"/ignored")
	writeFile(t, filepath.Join(ignored, "keepme", "results.bin"), "only copy\n")
	busy := f.worktree(f.repo, "busy", f.room+"/busy")
	processCwds = func() []processCwd { return []processCwd{{pid: 4242, cwd: filepath.Join(busy, "sub")}} }

	text, report, err := f.clean()
	if err != nil {
		t.Fatal(err)
	}
	for path, reason := range map[string]string{
		dirty:     "1 uncommitted",
		untracked: "1 untracked",
		ahead:     "1 commit(s) ahead of main",
		locked:    "git locks it",
		ignored:   "keepme",
		busy:      "4242",
	} {
		tree, ok := decisionFor(report, path)
		if !ok {
			t.Errorf("%s is not a decision:\n%s", path, text)
			continue
		}
		if !strings.Contains(strings.Join(tree.Reasons, ";"), reason) {
			t.Errorf("%s: reasons %v do not name %q", path, tree.Reasons, reason)
		}
	}
	if text, _, err = f.clean("--apply"); err != nil {
		t.Fatalf("a decision is not a failure: %v\n%s", err, text)
	}
	f.mustExist(filepath.Join(dirty, ".gitignore"), filepath.Join(untracked, "new.txt"), ahead, locked,
		filepath.Join(ignored, "keepme", "results.bin"), busy)
	if !hasBranch(t, f.repo, f.room+"/ahead") {
		t.Error("the unmerged branch was deleted")
	}
}

// --drop-ignored is the owner saying the ignored files can go, and only that.
func TestCleanDropIgnoredAcceptsIgnoredFilesOnly(t *testing.T) {
	f := newCleanFixture(t)
	ignored := f.worktree(f.repo, "ignored", f.room+"/ignored")
	writeFile(t, filepath.Join(ignored, "keepme", "results.bin"), "disposable\n")
	dirty := f.worktree(f.repo, "dirty", f.room+"/dirty")
	writeFile(t, filepath.Join(dirty, "keepme", "results.bin"), "x\n")
	writeFile(t, filepath.Join(dirty, "new.txt"), "new\n")

	if text, _, err := f.clean("--apply", "--drop-ignored"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustBeGone(ignored)
	f.mustExist(filepath.Join(dirty, "new.txt"))
}

// The live-seat cases: a state file with a running pid, and a seat's lane by name.
func TestCleanRefusesALiveSeatsWorktreeAndCaches(t *testing.T) {
	f := newCleanFixture(t)
	writeFile(t, filepath.Join(f.store, "rooms", f.room, "run", "dev.state.json"),
		fmt.Sprintf(`{"pid":%d,"state":"working"}`, os.Getpid()))
	// A stopped seat: its pid is one that cannot exist.
	writeFile(t, filepath.Join(f.store, "rooms", f.room, "run", "old.state.json"), `{"pid":2147483646}`)
	seat := f.worktree(f.repo, "dev", f.room+"/dev")
	lane := f.worktree(f.repo, "dev-lane", f.room+"/dev-lane")
	cache := filepath.Join(lane, "node_modules")
	writeFile(t, filepath.Join(cache, "pkg.js"), "x\n")
	stopped := f.worktree(f.repo, "old", f.room+"/old")

	text, report, err := f.clean()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{seat, lane} {
		tree, ok := decisionFor(report, path)
		if !ok || !strings.Contains(strings.Join(tree.Reasons, ";"), "live seat dev") {
			t.Errorf("%s is not refused for its live seat:\n%s", path, text)
		}
	}
	if text, _, err = f.clean("--apply"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustExist(seat, lane, cache)
	f.mustBeGone(stopped)
}

// The runtime's own answer counts as well as the files.
func TestCleanReadsTheRuntimesRunningSeats(t *testing.T) {
	f := newCleanFixture(t)
	runtimeRoomStatus = func(string) ([]clientruntime.SeatStatus, error) {
		return []clientruntime.SeatStatus{{Seat: "dev", State: "Running"}}, nil
	}
	seat := f.worktree(f.repo, "dev", f.room+"/dev")
	if text, _, err := f.clean("--apply"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustExist(seat)
}

// A CACHEDIR.TAG target goes, from a worktree that stays too; a target with no tag is not a cache, and it
// blocks the worktree it sits in. A symlink named node_modules is not followed.
func TestCleanDropsOnlyTaggedCaches(t *testing.T) {
	f := newCleanFixture(t)
	kept := f.worktree(f.repo, "kept", f.room+"/kept")
	f.commit(kept, "unmerged.txt")
	tagged := filepath.Join(kept, "target")
	writeFile(t, filepath.Join(tagged, "CACHEDIR.TAG"), cacheDirSignature+"\n# made by a build\n")
	writeFile(t, filepath.Join(tagged, "debug", "bin"), "binary\n")
	nodes := filepath.Join(kept, "app", "node_modules")
	writeFile(t, filepath.Join(nodes, "pkg.js"), "x\n")

	untagged := f.worktree(f.repo, "untagged", f.room+"/untagged")
	writeFile(t, filepath.Join(untagged, "target", "results.bin"), "not a cache\n")

	outside := filepath.Join(f.t.TempDir(), "outside")
	writeFile(t, filepath.Join(outside, "file.txt"), "must survive\n")
	linked := f.worktree(f.repo, "linked", f.room+"/linked")
	f.commit(linked, "unmerged2.txt")
	if err := os.Symlink(outside, filepath.Join(linked, "node_modules")); err != nil {
		t.Fatal(err)
	}

	text, report, err := f.clean()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Caches) != 2 {
		t.Errorf("planned caches = %+v, want the tagged target and the node_modules", report.Caches)
	}
	if tree, ok := decisionFor(report, untagged); !ok || !strings.Contains(strings.Join(tree.Reasons, ";"), "target") {
		t.Errorf("an untagged target must block its worktree:\n%s", text)
	}
	f.mustExist(tagged, nodes)

	if text, _, err = f.clean("--apply"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustBeGone(tagged, nodes)
	f.mustExist(kept, filepath.Join(untagged, "target", "results.bin"), filepath.Join(outside, "file.txt"))
}

// A node_modules somebody committed is source.
func TestCleanKeepsATrackedNodeModules(t *testing.T) {
	f := newCleanFixture(t)
	dir := f.worktree(f.repo, "vendored", f.room+"/vendored")
	writeFile(t, filepath.Join(dir, "vendor", "node_modules", "pkg.js"), "x\n")
	gitIn(t, dir, "add", "-f", "vendor/node_modules/pkg.js")
	gitIn(t, dir, "commit", "-q", "-m", "vendored")

	if text, _, err := f.clean("--apply"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustExist(filepath.Join(dir, "vendor", "node_modules", "pkg.js"))
}

// A live client `.env` link and the code graph are known to come back, so neither holds a worktree.
func TestCleanAcceptsTheClientsOwnLinkAndTheGraph(t *testing.T) {
	f := newCleanFixture(t)
	dir := f.worktree(f.repo, "lead-x", f.room+"/lead-x")
	if err := os.Symlink("/nonexistent/.env", filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "graphify-out", "graph.json"), "{}\n")
	if text, _, err := f.clean("--apply"); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustBeGone(dir)
}

// Branches: merged goes with -d, unmerged is a decision, a --keep and the base stay, and a named room stays
// out of what is not its own. No remote ref moves.
func TestCleanBranchRules(t *testing.T) {
	f := newCleanFixture(t)
	gitIn(t, f.repo, "branch", f.room+"/merged")
	gitIn(t, f.repo, "branch", f.room+"/long-lived")
	gitIn(t, f.repo, "branch", "other-room/merged")
	gitIn(t, f.repo, "branch", "unprefixed-merged")
	gitIn(t, f.repo, "checkout", "-q", "-b", f.room+"/unmerged")
	f.commit(f.repo, "unmerged.txt")
	gitIn(t, f.repo, "checkout", "-q", "main")
	head := gitIn(t, f.repo, "rev-parse", "HEAD")
	gitIn(t, f.repo, "update-ref", "refs/remotes/upstream/"+f.room+"/merged", head)
	gitIn(t, f.repo, "tag", "v1")
	gitIn(t, f.repo, "stash", "list")
	writeFile(t, filepath.Join(f.repo, ".gitignore"), "changed\n")
	gitIn(t, f.repo, "stash")
	f.worktree(f.repo, "lead-x", f.room+"/lead-x")

	text, _, err := f.clean("--apply", "--keep", "*/long-lived")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if hasBranch(t, f.repo, f.room+"/merged") || hasBranch(t, f.repo, f.room+"/lead-x") {
		t.Errorf("a merged branch of this room survived:\n%s", text)
	}
	for _, name := range []string{"main", f.room + "/long-lived", f.room + "/unmerged", "other-room/merged", "unprefixed-merged"} {
		if !hasBranch(t, f.repo, name) {
			t.Errorf("branch %s must stay:\n%s", name, text)
		}
	}
	if !strings.Contains(text, "decide branch project "+f.room+"/unmerged: 1 commit(s) ahead of main") {
		t.Errorf("the unmerged branch is not named as a decision:\n%s", text)
	}
	if out := gitIn(t, f.repo, "for-each-ref", "refs/remotes/upstream"); !strings.Contains(out, f.room+"/merged") {
		t.Errorf("a remote ref moved: %q", out)
	}
	if out := gitIn(t, f.repo, "tag", "--list"); !strings.Contains(out, "v1") {
		t.Errorf("a tag moved: %q", out)
	}
	if out := gitIn(t, f.repo, "stash", "list"); !strings.Contains(out, "stash@{0}") {
		t.Errorf("the stash was dropped: %q", out)
	}
}

// --main reaches the whole repository and leaves its foreign worktrees alone, naming them.
func TestCleanMainReachesEveryMergedBranchAndNeverAForeignWorktree(t *testing.T) {
	f := newCleanFixture(t)
	gitIn(t, f.repo, "branch", "unprefixed-merged")
	foreign := filepath.Join(t.TempDir(), "job")
	gitIn(t, f.repo, "worktree", "add", "-q", "-b", "job-branch", foreign, "main")
	room := f.worktree(f.repo, "lead-x", f.room+"/lead-x")

	var text strings.Builder
	if err := runClean([]string{"--state-root", f.store, "--main", f.repo, "--apply"}, &text); err != nil {
		t.Fatalf("%v\n%s", err, text.String())
	}
	f.mustBeGone(room)
	f.mustExist(foreign)
	if hasBranch(t, f.repo, "unprefixed-merged") {
		t.Errorf("--main did not reach the whole repository:\n%s", text.String())
	}
	if !hasBranch(t, f.repo, "job-branch") {
		t.Errorf("the branch a foreign worktree holds was deleted:\n%s", text.String())
	}
}

// --branch is the land step: this branch and what holds it, nothing else.
func TestCleanBranchIsTheLandStep(t *testing.T) {
	f := newCleanFixture(t)
	landed := f.worktree(f.repo, "dev-land", f.room+"/dev-land")
	f.commit(landed, "land.txt")
	gitIn(t, f.repo, "merge", "-q", "--no-ff", "-m", "land", f.room+"/dev-land")
	other := f.worktree(f.repo, "dev-other", f.room+"/dev-other")
	gitIn(t, f.repo, "branch", f.room+"/also-merged")

	text, _, err := f.clean("--apply", "--branch", f.room+"/dev-land")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	f.mustBeGone(landed)
	f.mustExist(other)
	if hasBranch(t, f.repo, f.room+"/dev-land") {
		t.Errorf("the landed branch survived:\n%s", text)
	}
	if !hasBranch(t, f.repo, f.room+"/also-merged") {
		t.Errorf("--branch reached past its branch:\n%s", text)
	}
}

// The form the merge routine documents: the repository names the scope, and the room is found from it.
func TestCleanMainAndBranchFindTheRoomsWorktree(t *testing.T) {
	f := newCleanFixture(t)
	landed := f.worktree(f.repo, "dev-land", f.room+"/dev-land")
	var text strings.Builder
	err := runClean([]string{"--state-root", f.store, "--main", f.repo, "--branch", f.room + "/dev-land", "--apply"}, &text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text.String())
	}
	f.mustBeGone(landed)
	if hasBranch(t, f.repo, f.room+"/dev-land") {
		t.Errorf("the landed branch survived:\n%s", text.String())
	}
}

// A land whose branch cannot go says so in the exit, so a merge script does not have to read output.
func TestCleanBranchExitsNonZeroWhenTheBranchStays(t *testing.T) {
	f := newCleanFixture(t)
	dirty := f.worktree(f.repo, "dev-dirty", f.room+"/dev-dirty")
	writeFile(t, filepath.Join(dirty, "new.txt"), "wip\n")

	_, _, err := f.clean("--apply", "--branch", f.room+"/dev-dirty")
	if clientcmd.CodeOf(err, 0) != clientcmd.ExitUnavailable {
		t.Errorf("exit %v, want %d", err, clientcmd.ExitUnavailable)
	}
	f.mustExist(filepath.Join(dirty, "new.txt"))

	_, _, err = f.clean("--apply", "--branch", "no-such-branch")
	if clientcmd.CodeOf(err, 0) != clientcmd.ExitUnavailable {
		t.Errorf("a branch nobody has: exit %v, want %d", err, clientcmd.ExitUnavailable)
	}
}

// Only a room's own lead, or the owner, may apply.
func TestCleanApplyRefusesASeat(t *testing.T) {
	f := newCleanFixture(t)
	done := f.worktree(f.repo, "lead-done", f.room+"/lead-done")
	t.Setenv("AGENTHUB_ROOM", f.room)
	t.Setenv("AGENTHUB_SEAT", "dev")
	_, _, err := f.clean("--apply")
	if clientcmd.CodeOf(err, 0) != clientcmd.ExitRefused {
		t.Fatalf("a worker seat's apply: %v, want exit %d", err, clientcmd.ExitRefused)
	}
	f.mustExist(done)
	if _, _, err := f.clean(); err != nil {
		t.Errorf("a seat may still read a dry run: %v", err)
	}

	t.Setenv("AGENTHUB_SEAT", "lead")
	if _, _, err := f.clean("--apply"); err != nil {
		t.Errorf("the room's own lead may apply: %v", err)
	}
	f.mustBeGone(done)
}

// The status view: every fact, in both shapes, and it changes nothing.
func TestStatusReportsEveryFact(t *testing.T) {
	f := newCleanFixture(t)
	second := f.newRepo("client")
	gitIn(t, second, "branch", "second-merged")
	inRepo := f.worktree(f.repo, "lead", f.room+"/lead")
	f.commit(inRepo, "one.txt")
	writeFile(t, filepath.Join(inRepo, "new.txt"), "new\n")
	writeFile(t, filepath.Join(inRepo, ".gitignore"), "changed\n")
	writeFile(t, filepath.Join(f.repo, ".gitignore"), "changed\n")
	gitIn(t, f.repo, "stash")
	f.worktree(second, "lead/client", f.room+"/lead")
	writeFile(t, filepath.Join(f.store, "rooms", f.room, "run", "lead.state.json"),
		fmt.Sprintf(`{"pid":%d}`, os.Getpid()))
	plain := filepath.Join(f.wt, "loose")
	writeFile(t, filepath.Join(plain, "a.txt"), "a\n")

	before := gitIn(t, f.repo, "worktree", "list", "--porcelain")
	var raw strings.Builder
	if err := runStatus([]string{"--state-root", f.store, f.room, "--json"}, &raw); err != nil {
		t.Fatal(err)
	}
	var report StatusReport
	if err := json.Unmarshal([]byte(raw.String()), &report); err != nil {
		t.Fatalf("%v\n%s", err, raw.String())
	}
	if len(report.Repos) != 2 {
		t.Errorf("repos = %+v, want the two the room's worktrees use", report.Repos)
	}
	var found Tree
	kinds := map[string]bool{}
	for _, tree := range report.Worktrees {
		kinds[tree.Kind] = true
		if tree.Path == inRepo {
			found = tree
		}
	}
	if !kinds[KindPlain] || !kinds[KindLinked] {
		t.Errorf("kinds = %v", kinds)
	}
	if found.Repo != "project" || found.Branch != f.room+"/lead" || found.Merged || found.Ahead != 1 ||
		found.Uncommitted != 1 || found.Untracked != 1 || found.LastCommit == "" || found.Seat != "lead" ||
		!found.SeatLive || found.SizeBytes == 0 {
		t.Errorf("the worktree's facts are wrong: %+v", found)
	}
	for _, repo := range report.Repos {
		if repo.Name == "project" && repo.Stashes != 1 {
			t.Errorf("stashes = %d, want 1", repo.Stashes)
		}
		if repo.Name != "client" {
			continue
		}
		merged := false
		for _, branch := range repo.Branches {
			if branch.Name == "second-merged" && branch.Merged && branch.Subject == "first commit" && branch.Date != "" {
				merged = true
			}
		}
		if !merged {
			t.Errorf("the second repository's merged branch is not reported: %+v", repo.Branches)
		}
	}

	var text strings.Builder
	if err := runStatus([]string{"--state-root", f.store, "--all", "--no-size"}, &text); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"room " + f.room, "ahead=1 " + inRepo, "repo project", "stashes=1", "plain " + plain} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("the text view lacks %q:\n%s", want, text.String())
		}
	}
	if after := gitIn(t, f.repo, "worktree", "list", "--porcelain"); after != before {
		t.Errorf("status changed the worktrees:\n%s\n%s", before, after)
	}
}

// Usage: no default room, --all stands alone.
func TestScopeIsRequired(t *testing.T) {
	stubLiveness(t)
	store := t.TempDir()
	var out strings.Builder
	for _, args := range [][]string{
		{"--state-root", store},
		{"--state-root", store, "--all", "room"},
		{"--state-root", store, "one", "two"},
	} {
		if err := runStatus(args, &out); clientcmd.CodeOf(err, 0) != clientcmd.ExitUsage {
			t.Errorf("status %v: %v, want a usage error", args, err)
		}
		if err := runClean(args, &out); clientcmd.CodeOf(err, 0) != clientcmd.ExitUsage {
			t.Errorf("clean %v: %v, want a usage error", args, err)
		}
	}
	if err := runClean([]string{"--help"}, &out); !errors.Is(err, errHelpShown) {
		t.Errorf("--help: %v", err)
	}
}

// Both verbs answer through the dispatcher, usage included.
func TestStatusAndCleanAreVerbsOfTheCommand(t *testing.T) {
	for _, verb := range []string{"status", "clean"} {
		var out, errOut strings.Builder
		if code := (worktreeCommand{}).Run(context.Background(), []string{verb, "--help"}, &out, &errOut); code != clientcmd.ExitOK {
			t.Errorf("worktree %s --help exited %d: %s", verb, code, errOut.String())
		}
		if !strings.Contains(out.String(), "4genteam worktree "+verb) {
			t.Errorf("worktree %s --help: %s", verb, out.String())
		}
	}
}

// The promise that a clean never forces is checked against the source of every git call it can make.
func TestNoGitCallHereCarriesAForceFlag(t *testing.T) {
	forced := regexp.MustCompile(`"(--force|-f|-D)"`)
	for _, file := range []string{"clean.go", "inventory.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if match := forced.FindString(string(raw)); match != "" {
			t.Errorf("%s passes %s", file, match)
		}
		if strings.Contains(string(raw), `"push"`) || strings.Contains(string(raw), `"update-ref"`) {
			t.Errorf("%s moves a ref by a path other than git branch -d", file)
		}
	}
}
