package clientworktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// The ruling's own table: a Start run twice changes nothing.
func TestEnsureRunTwiceChangesNothing(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"},
		{Key: "review", Type: "reviewer"},
		{Key: "lead", Type: "lead"},
	})
	linked := stubLinkProviderKey(t)

	var first strings.Builder
	if err := fixture.ensure(&first); err != nil {
		t.Fatalf("the first Ensure: %v\n%s", err, first.String())
	}
	before := map[string]time.Time{}
	for _, worktree := range fixture.derived() {
		if _, err := os.Stat(worktree.Path); err != nil {
			t.Fatalf("%s was not created: %v\n%s", worktree.Path, err, first.String())
		}
		before[worktree.Path] = fixedTime(t, worktree.Path)
	}
	listingBefore := fixture.listRaw()

	var second strings.Builder
	if err := fixture.ensure(&second); err != nil {
		t.Fatalf("the second Ensure: %v\n%s", err, second.String())
	}
	if listingAfter := fixture.listRaw(); listingAfter != listingBefore {
		t.Errorf("the second run changed `git worktree list`:\nbefore:\n%s\nafter:\n%s",
			listingBefore, listingAfter)
	}
	for path, when := range before {
		if now := fixedTime(t, path); !now.Equal(when) {
			t.Errorf("%s was written again on the second run: its modification time moved from %v to %v",
				path, when, now)
		}
	}
	for _, worktree := range fixture.derived() {
		if !strings.Contains(second.String(), worktree.Path+": kept") {
			t.Errorf("the second run did not keep %s:\n%s", worktree.Path, second.String())
		}
	}
	if strings.Contains(second.String(), ": created") {
		t.Errorf("the second run created a path:\n%s", second.String())
	}

	// The provider key is linked into every WRITING worktree and into no other: the reviewer is
	// detached and never commits, so its worktree gets no link.
	var wantLinked []string
	for _, worktree := range fixture.derived() {
		if worktree.Branch != "" {
			wantLinked = append(wantLinked, worktree.Path)
		}
	}
	if len(*linked) != 2*len(wantLinked) {
		t.Fatalf("the two runs linked the key into %d worktree(s), want %d:\n%v",
			len(*linked), 2*len(wantLinked), *linked)
	}
	lastRun := (*linked)[len(*linked)-len(wantLinked):]
	for i, path := range wantLinked {
		if lastRun[i] != path {
			t.Errorf("the second run linked the key into %s, want %s", lastRun[i], path)
		}
	}
	for _, worktree := range fixture.derived() {
		if worktree.Branch == "" && strings.Contains(strings.Join(*linked, "\n"), worktree.Path) {
			t.Errorf("the key was linked into the reviewer's worktree %s, which never commits", worktree.Path)
		}
	}
}

// A LIVE ROOM IS READ: a derived path that is registered on another branch is KEPT, untouched, and the
// rest of the room is still attempted. Measured on the live room (2026-10-18): fe-dev sat on
// `fe-dashboard-update-depth` and architect on `g3-drop-classification`, and a refusal over the branch
// they held made every `up` - including the heal after a reboot - fail on a room that was fine.
func TestEnsureKeepsADerivedPathOnAnotherBranch(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{
		{Key: "go-dev", Type: "developer"},
		{Key: "lead", Type: "lead"},
	})
	stubLinkProviderKey(t)
	developer := fixture.derived()[0]
	mkdirAll(t, filepath.Dir(developer.Path))
	gitIn(t, fixture.main, "worktree", "add", "-b", "somebody-elses-branch", developer.Path, "main")

	var out strings.Builder
	if err := fixture.ensure(&out); err != nil {
		t.Fatalf("a registered derived path must be kept, not refused: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), developer.Path+": kept") {
		t.Errorf("the line for %s did not say kept:\n%s", developer.Path, out.String())
	}
	if branch := gitIn(t, developer.Path, "rev-parse", "--abbrev-ref", "HEAD"); branch != "somebody-elses-branch" {
		t.Errorf("the kept worktree was moved to %s: nothing may be overwritten", branch)
	}
	if entry, registered := fixture.entry(t, developer.Path); !registered || entry.Branch != "somebody-elses-branch" {
		t.Errorf("the kept worktree is no longer registered on its own branch: %+v", entry)
	}
	// EVERY derived worktree is attempted, never just up to the first one seen.
	for _, want := range []string{
		filepath.Join(fixture.room, "wt", "lead") + ": created",
		filepath.Join(fixture.room, "wt", "integration") + ": created",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the kept path stopped the run before %q:\n%s", want, out.String())
		}
	}
}

// The ruling's second refusal case, in the same words: a path that exists and is not a worktree.
func TestEnsureRefusesAPathThatIsNotAWorktree(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	path := fixture.derived()[0].Path
	mkdirAll(t, path)
	writeFile(t, filepath.Join(path, "operator.txt"), "an operator's own directory\n")

	var out strings.Builder
	err := fixture.ensure(&out)
	if err == nil {
		t.Fatalf("a directory that is not a worktree must be refused:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the refusal did not name %s: %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(path, "operator.txt")); err != nil {
		t.Errorf("the operator's file was overwritten or removed: %v", err)
	}
	if _, registered := fixture.entry(t, path); registered {
		t.Errorf("%s was registered as a worktree", path)
	}
}

// The ruling's measured reason, as a case: a parent whose submodule pin was never pushed still starts,
// and the pin is what the seat's worktree carries.
func TestEnsureStartsWithASubmodulePinThatWasNeverPushed(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{
		{Key: "lead", Type: "lead"},
		{Key: "go-dev", Type: "developer"},
		{Key: "review", Type: "reviewer"},
		{Key: "architect", Type: "architect"},
	})
	stubLinkProviderKey(t)

	var out strings.Builder
	if err := fixture.ensure(&out); err != nil {
		t.Fatalf("a pin that was never pushed must still start: %v\n%s", err, out.String())
	}
	// The ruling's table in one loop: four seat worktrees and the integration one, each on its own
	// branch, with the reviewer detached because it never commits.
	derived := fixture.derived()
	if len(derived) != 5 {
		t.Fatalf("derived %d worktree(s), want four seats and integration: %+v", len(derived), derived)
	}
	for _, worktree := range derived {
		entry, registered := fixture.entry(t, worktree.Path)
		if !registered {
			t.Fatalf("%s is not in `git worktree list`:\n%s", worktree.Path, fixture.listRaw())
		}
		switch {
		case worktree.Branch == "":
			if !entry.Detached {
				t.Errorf("%s is on %s; a reviewer must be detached", worktree.Path, entry.Branch)
			}
		case entry.Detached || entry.Branch != worktree.Branch:
			t.Errorf("%s is on %q, want %s", worktree.Path, entry.Branch, worktree.Branch)
		}
		if !clientLinkPresent(worktree.Path) {
			t.Errorf("%s does not carry the client submodule worktree", worktree.Path)
		}
	}

	lead := filepath.Join(fixture.room, "wt", "lead")
	status := gitIn(t, lead, "submodule", "status")
	if !strings.Contains(status, fixture.pins[clientSubmodule]) {
		t.Errorf("`git -C %s submodule status` does not show the %s pin %s:\n%s",
			lead, clientSubmodule, fixture.pins[clientSubmodule], status)
	}
	if !strings.Contains(status, clientSubmodule) {
		t.Errorf("`git -C %s submodule status` does not name %s:\n%s", lead, clientSubmodule, status)
	}
	if branch := gitIn(t, filepath.Join(lead, "agenthub_client"), "rev-parse", "--abbrev-ref", "HEAD"); branch != fixture.rig+"/lead" {
		t.Errorf("the client submodule worktree is on %s, want %s/lead", branch, fixture.rig)
	}
	// `.claude` is not a submodule of this hub and has no gitlink: the link must not be attempted at
	// all, and the seat's worktree must not carry one.
	if _, err := os.Lstat(filepath.Join(lead, ".claude")); err == nil {
		t.Errorf("%s carries a .claude path; .claude is not a submodule and is never linked", lead)
	}

	// The pin is unpushed for real: the source the submodule was cloned from is gone, so the URL cannot
	// serve it and `git submodule update --init` cannot end any other way than this fetch does.
	url := gitIn(t, filepath.Join(fixture.main, "agenthub_client"), "config", "remote.origin.url")
	if _, err := os.Stat(url); err == nil {
		t.Fatalf("the fixture's submodule source is still at %s, so this case proves nothing about an "+
			"unpushed pin", url)
	}
	if fetched, err := gitTry(t, filepath.Join(fixture.main, "agenthub_client"), "fetch", "origin"); err == nil {
		t.Errorf("the fixture fetched the pin from %s, so it was pushed after all:\n%s", url, fetched)
	}
}

// The live room's shape, measured by the reviewer and reproduced here: the branch the ruling derives
// already exists and a worktree somewhere else holds it. Git's own words for that are `fatal: a branch
// named 'X' already exists` (measured in a scratch repository): it names neither the repository nor the
// worktree that holds it, so the refusal has to, and it must create nothing.
func TestEnsureRefusesABranchAnotherWorktreeHolds(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	developer := fixture.derived()[0]
	elsewhere := filepath.Join(fixture.room, "wt", "go-dev-client")
	mkdirAll(t, filepath.Dir(elsewhere))
	gitIn(t, fixture.main, "worktree", "add", "-b", developer.Branch, elsewhere, "main")

	var out strings.Builder
	err := fixture.ensure(&out)
	if err == nil {
		t.Fatalf("a branch another worktree holds must be refused:\n%s", out.String())
	}
	for _, want := range []string{developer.Branch, elsewhere, fixture.main} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "fatal:") || strings.Contains(out.String(), "fatal:") {
		t.Errorf("the refusal is git's own text rather than a named one:\nerr: %v\nout: %s", err, out.String())
	}
	if _, statErr := os.Stat(developer.Path); statErr == nil {
		t.Errorf("%s was created although another worktree holds its branch", developer.Path)
	}
	if _, registered := fixture.entry(t, developer.Path); registered {
		t.Errorf("%s was registered as a worktree", developer.Path)
	}
	if branch := gitIn(t, elsewhere, "rev-parse", "--abbrev-ref", "HEAD"); branch != developer.Branch {
		t.Errorf("the worktree at %s is now on %s: nothing is re-pointed", elsewhere, branch)
	}
}

// The same refusal in the OTHER repository a seat's worktree carries. The client repository's branch
// held at a path that is NOT the pre-derivation one - `<room>/wt/<seat>-client`, which Ensure now clears
// first - is still refused by name, and that named refusal has to reach the submodule link as well,
// where git's own text is the same unhelpful sentence.
func TestEnsureRefusesAClientBranchAnotherWorktreeHolds(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	developer := fixture.derived()[0]
	clientRepository := filepath.Join(fixture.main, "agenthub_client")
	elsewhere := filepath.Join(t.TempDir(), "go-dev-client")
	gitIn(t, clientRepository, "worktree", "add", "-b", developer.Branch, elsewhere,
		fixture.pins["agenthub_client"])

	var out strings.Builder
	err := fixture.ensure(&out)
	if err == nil {
		t.Fatalf("a client branch another worktree holds must be refused:\n%s", out.String())
	}
	for _, want := range []string{developer.Branch, elsewhere, clientRepository} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "fatal:") {
		t.Errorf("the refusal is git's own text rather than a named one: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(developer.Path, "agenthub_client", ".git")); statErr == nil {
		t.Errorf("the client submodule worktree was created although another worktree holds the branch")
	}
}

// The branch exists and no worktree holds it: that is the seat's OWN branch, and the derivation attaches
// to it - `git worktree add <path> <branch>` re-points nothing, deletes nothing and adopts nothing but
// the committed state. Refusing this was what made `up` fail on the live room's go-dev (its branch
// existed, its worktree did not), so the heal after a reboot refused a seat that only needed its path.
func TestEnsureCreatesTheSeatWorktreeOnItsOwnExistingBranch(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	developer := fixture.derived()[0]
	gitIn(t, fixture.main, "branch", developer.Branch, "main")
	before := gitIn(t, fixture.main, "rev-parse", "refs/heads/"+developer.Branch)

	var out strings.Builder
	if err := fixture.ensure(&out); err != nil {
		t.Fatalf("a seat's own branch must be attached to, not refused: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), developer.Path+": created") {
		t.Errorf("the line for %s did not say created:\n%s", developer.Path, out.String())
	}
	if branch := gitIn(t, developer.Path, "rev-parse", "--abbrev-ref", "HEAD"); branch != developer.Branch {
		t.Errorf("%s is on %s, want the seat's own branch %s", developer.Path, branch, developer.Branch)
	}
	after := gitIn(t, fixture.main, "rev-parse", "refs/heads/"+developer.Branch)
	if after != before {
		t.Errorf("the branch %s was re-pointed from %s to %s: nothing is re-pointed", developer.Branch, before, after)
	}
	if entry, registered := fixture.entry(t, developer.Path); !registered || entry.Branch != developer.Branch {
		t.Errorf("%s is not registered on its own branch: %+v", developer.Path, entry)
	}
	if !clientLinkPresent(developer.Path) {
		t.Errorf("%s does not carry the client submodule worktree", developer.Path)
	}
}

// Nit (1) from the `43556ae` gate: a holder git marks PRUNABLE is named as prunable, with the operator's
// actual fix in that state - `git worktree prune` - and nothing is created. ensureOne already words the
// path-level prunable case that way; the holder case must too, or the operator is told a working tree
// that is gone is still "checked out".
func TestEnsureNamesAPrunableHolderAndThePruneThatClearsIt(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	developer := fixture.derived()[0]
	elsewhere := filepath.Join(fixture.room, "wt", "go-dev-client")
	mkdirAll(t, filepath.Dir(elsewhere))
	gitIn(t, fixture.main, "worktree", "add", "-b", developer.Branch, elsewhere, "main")
	// git marks an entry prunable once its working tree is gone; moving the directory aside produces
	// exactly that state, without deleting anything.
	if err := os.Rename(elsewhere, filepath.Join(t.TempDir(), "go-dev-client")); err != nil {
		t.Fatalf("move the holder aside, to make git mark it prunable: %v", err)
	}
	if entry, registered := fixture.entry(t, elsewhere); !registered || !entry.Prunable {
		t.Fatalf("the fixture did not produce a prunable holder: entry %+v, registered %v", entry, registered)
	}

	var out strings.Builder
	err := fixture.ensure(&out)
	if err == nil {
		t.Fatalf("a branch a prunable worktree holds must be refused:\n%s", out.String())
	}
	for _, want := range []string{developer.Branch, elsewhere, fixture.main, "prunable", "worktree prune"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "already checked out") {
		t.Errorf("a prunable holder is described as checked out although its working tree is gone: %v", err)
	}
	if _, statErr := os.Stat(developer.Path); statErr == nil {
		t.Errorf("%s was created although the branch its holder holds is only prunable", developer.Path)
	}
}

// Nit (2) from the `43556ae` gate: the client repository's branch refusal is answered BEFORE the seat's
// own worktree is created. linkSubmodules refuses it too, but it runs after createWorktree, so the
// refusal used to leave the seat's worktree standing - a half-started path nobody can use. A refusal
// must leave nothing behind.
func TestEnsureRefusesAClientBranchBeforeCreatingTheSeatWorktree(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	developer := fixture.derived()[0]
	clientRepository := filepath.Join(fixture.main, "agenthub_client")
	elsewhere := filepath.Join(t.TempDir(), "go-dev-client")
	gitIn(t, clientRepository, "worktree", "add", "-b", developer.Branch, elsewhere,
		fixture.pins["agenthub_client"])

	var out strings.Builder
	err := fixture.ensure(&out)
	if err == nil {
		t.Fatalf("a client branch another worktree holds must be refused:\n%s", out.String())
	}
	if _, statErr := os.Stat(developer.Path); statErr == nil {
		t.Errorf("%s was created before the client repository's refusal; a refusal must leave nothing "+
			"behind, and a path that exists reads as a started seat", developer.Path)
	}
	if _, registered := fixture.entry(t, developer.Path); registered {
		t.Errorf("%s was registered as a worktree although the start step refused it", developer.Path)
	}
	if _, statErr := os.Stat(filepath.Join(developer.Path, "agenthub_client", ".git")); statErr == nil {
		t.Errorf("the client submodule worktree was created although another worktree holds the branch")
	}
}

// Nit (3) from the `43556ae` gate: a path compared lexically names the same directory twice when the
// room is reached through a symlink. git registers the RESOLVED path - `git worktree add
// <symlinked>/wt/<seat>` records the real one - so the derived path and the registered one differ as
// strings while naming one directory, and the legitimate reuse was refused.
func TestEnsureReusesARoomReachedThroughASymlink(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	var first strings.Builder
	if err := fixture.ensure(&first); err != nil {
		t.Fatalf("the first Ensure: %v\n%s", err, first.String())
	}
	developer := fixture.derived()[0]
	link := filepath.Join(t.TempDir(), "room-link")
	if err := os.Symlink(fixture.room, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, fixture.room, err)
	}
	fixture.room = link

	var second strings.Builder
	if err := fixture.ensure(&second); err != nil {
		t.Fatalf("a room reached through a symlink must reuse its worktrees, not refuse them: %v\n%s",
			err, second.String())
	}
	if strings.Contains(second.String(), "refused") {
		t.Errorf("a room reached through a symlink refused a worktree:\n%s", second.String())
	}
	if want := filepath.Join(link, "wt", "go-dev") + ": kept"; !strings.Contains(second.String(), want) {
		t.Errorf("the second run did not keep %s:\n%s", filepath.Join(link, "wt", "go-dev"), second.String())
	}
	// The direct refusal is the same rule, from the other end: the worktree that holds the branch IS
	// this path, so it is the reuse carve-out and not a refusal.
	if err := branchRefusal(fixture.main, developer.Branch, filepath.Join(link, "wt", "go-dev")); err != nil {
		t.Errorf("branchRefusal refused the very worktree that holds %s, reached through a symlink: %v",
			developer.Branch, err)
	}
}

// clientEntry answers the CLIENT repository's registered entry for a path, and whether one exists. The
// main-repository reader (fixture.entry) cannot answer this: the seat's client link lives in the other
// repository its worktree carries.
func clientEntry(t *testing.T, repository, path string) (Entry, bool) {
	t.Helper()
	entries, err := Read(repository)
	if err != nil {
		t.Fatalf("read the worktrees of %s: %v", repository, err)
	}
	for _, entry := range entries {
		if canonicalPath(entry.Path) == canonicalPath(path) {
			return entry, true
		}
	}
	return Entry{}, false
}

// THE MIGRATION: a room made by hand, before the derivation existed, keeps its client-repository worktree
// at `<roomDir>/wt/<seat>-client`, and git checks a branch out in exactly ONE worktree per repository --
// so that leftover holds the seat's own branch and the client link the derivation wants
// (`<roomDir>/wt/<seat>/agenthub_client`) cannot be created. The start step clears exactly that entry
// before it derives; the branch survives, so the seat's commits are never lost.
func TestEnsureClearsAPreDerivationClientWorktree(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	seat := fixture.derived()[0]
	clientRepository := filepath.Join(fixture.main, clientSubmodule)
	legacy := seat.Path + "-client"
	link := filepath.Join(seat.Path, clientSubmodule)

	gitIn(t, clientRepository, "worktree", "add", "-b", seat.Branch, legacy, fixture.pins[clientSubmodule])
	// A commit the seat made in the old layout, so the assertions below can tell an ATTACHED link - on the
	// seat's own client work - from a branch created fresh at the parent's gitlink.
	seatCommit := commitIn(t, legacy, "the seat's own client commit")

	// Red-first at the source: while the legacy entry holds the branch, the link the derivation wants is
	// refused by git itself - the obstruction this step exists to clear. git answers the branch before it
	// makes a directory, so the attempt leaves nothing behind but the parent it needed.
	mkdirAll(t, filepath.Dir(link))
	if out, err := gitTry(t, clientRepository, "worktree", "add", "-b", seat.Branch, link,
		fixture.pins[clientSubmodule]); err == nil {
		t.Fatalf("git made a second worktree on %s while %s held it: %s", seat.Branch, legacy, out)
	}
	if err := os.Remove(seat.Path); err != nil {
		t.Fatalf("remove the empty directory the red-first attempt's parent made: %v", err)
	}

	var out strings.Builder
	if err := fixture.ensure(&out); err != nil {
		t.Fatalf("the pre-derivation client worktree was not cleared: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), legacy+": cleared") {
		t.Errorf("the run did not report clearing %s:\n%s", legacy, out.String())
	}
	if entry, registered := clientEntry(t, clientRepository, legacy); registered {
		t.Errorf("%s is still a registered worktree of the client repository: %+v", legacy, entry)
	}
	if got := gitIn(t, clientRepository, "rev-parse", "refs/heads/"+seat.Branch); got != seatCommit {
		t.Errorf("the branch %s is at %s, want the seat's own commit %s to survive the removal",
			seat.Branch, got, seatCommit)
	}
	entry, registered := clientEntry(t, clientRepository, link)
	if !registered || entry.Branch != seat.Branch {
		t.Errorf("the client link at %s is %+v, registered=%v; want it on %s", link, entry, registered, seat.Branch)
	}
	if got := gitIn(t, link, "rev-parse", "HEAD"); got != seatCommit {
		t.Errorf("the client link at %s is at %s, want the seat's own commit %s: the link ATTACHES to the "+
			"branch that survived the removal, it does not start a branch at the parent's gitlink",
			link, got, seatCommit)
	}
}

// The other boundary of removing anything at all: a legacy worktree that holds work the operator has not
// committed is REFUSED and named, and nothing is taken away - the removal is git's own `git worktree
// remove` without `--force`.
func TestEnsureRefusesToClearADirtyPreDerivationClientWorktree(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	seat := fixture.derived()[0]
	clientRepository := filepath.Join(fixture.main, clientSubmodule)
	legacy := seat.Path + "-client"
	gitIn(t, clientRepository, "worktree", "add", "-b", seat.Branch, legacy, fixture.pins[clientSubmodule])
	uncommitted := filepath.Join(legacy, "UNCOMMITTED.md")
	writeFile(t, uncommitted, "work the operator has not committed\n")

	var out strings.Builder
	err := fixture.ensure(&out)
	if err == nil {
		t.Fatalf("a dirty pre-derivation client worktree must be refused, not deleted:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), legacy) {
		t.Errorf("the refusal did not name %s: %v", legacy, err)
	}
	if entry, registered := clientEntry(t, clientRepository, legacy); !registered || entry.Branch != seat.Branch {
		t.Errorf("the dirty holder at %s was moved or removed: %+v (registered=%v)", legacy, entry, registered)
	}
	if _, err := os.Stat(uncommitted); err != nil {
		t.Errorf("the uncommitted file was taken away: %v", err)
	}
}

// commitIn writes one file in a worktree and commits it, answering the commit's SHA. A branch needs a tip
// of its own before a test can tell which commit an entry is on.
func commitIn(t *testing.T, worktree, message string) string {
	t.Helper()
	writeFile(t, filepath.Join(worktree, "SEAT-COMMIT.md"), message+"\n")
	gitIn(t, worktree, "add", "SEAT-COMMIT.md")
	gitIn(t, worktree, "commit", "-q", "-m", message)
	return gitIn(t, worktree, "rev-parse", "HEAD")
}

// The clearing is SCOPED, and this is the half that says so: a `-client` worktree on ANOTHER branch is
// not the obstruction, so it is left exactly where it is. Only the seat's own branch at the legacy path
// is cleared, nothing else in either repository is touched.
func TestEnsureLeavesAForeignClientWorktreeAlone(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	seat := fixture.derived()[0]
	clientRepository := filepath.Join(fixture.main, clientSubmodule)
	foreign := seat.Path + "-client"
	const foreignBranch = "somebody-elses-client-branch"
	gitIn(t, clientRepository, "worktree", "add", "-b", foreignBranch, foreign, fixture.pins[clientSubmodule])

	var out strings.Builder
	if err := fixture.ensure(&out); err != nil {
		t.Fatalf("a foreign -client worktree blocked the start step: %v\n%s", err, out.String())
	}
	entry, registered := clientEntry(t, clientRepository, foreign)
	if !registered || entry.Branch != foreignBranch {
		t.Errorf("the foreign worktree at %s was moved or removed: %+v (registered=%v)", foreign, entry, registered)
	}
}

// The other half of the scope: the seat's branch held at a path that is NOT the legacy one is still
// refused BY NAME and never deleted - re-pointing or dropping an operator's worktree is not this step's
// decision, and the branch it holds carries the seat's own commits.
func TestEnsureStillRefusesTheBranchHeldAtANonLegacyPath(t *testing.T) {
	fixture := newSeatFixture(t, []clientservices.SeatType{{Key: "go-dev", Type: "developer"}})
	stubLinkProviderKey(t)
	seat := fixture.derived()[0]
	clientRepository := filepath.Join(fixture.main, clientSubmodule)
	elsewhere := filepath.Join(t.TempDir(), "go-dev-client")
	gitIn(t, clientRepository, "worktree", "add", "-b", seat.Branch, elsewhere, fixture.pins[clientSubmodule])

	var out strings.Builder
	err := fixture.ensure(&out)
	if err == nil {
		t.Fatalf("a client branch holder at a non-legacy path must still be refused:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), elsewhere) {
		t.Errorf("the refusal did not name %s: %v", elsewhere, err)
	}
	entry, registered := clientEntry(t, clientRepository, elsewhere)
	if !registered || entry.Branch != seat.Branch {
		t.Errorf("the holder at %s was moved or removed: %+v (registered=%v)", elsewhere, entry, registered)
	}
}
