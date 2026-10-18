// Package clientworktree owns the room's derived seat worktrees: the start step that creates them
// (Ensure), the ruling's read-only check over them (Check), and the two operator verbs
// (`worktree list`, `worktree prune`) that read and clean up `git worktree list`.
//
// THE LIST IS DERIVED, NEVER WRITTEN DOWN. The ONE derivation is clientservices.DerivedWorktrees,
// ruled in ai_docs/core-architecture/seat-worktrees.md and compiled there for 7.8's reason; this
// package only carries it out. Nothing here keeps a second list, so a seat's isolation cannot be
// removed by editing a file and cannot be added by one either.
//
// WHY THE SUBMODULE IS A LINKED WORKTREE AND NEVER `git submodule update`. A seat's worktree carries
// `agenthub_client`, a linked worktree of the MAIN CHECKOUT's own submodule repository:
// `git -C <main>/agenthub_client worktree add <wt>/agenthub_client <gitlink>`. The reason is measured,
// not stylistic. In a new worktree `git submodule update --init` clones from the submodule URL, and
// every pin this room records is a commit that was never pushed, so the clone fails with
// `fatal: git upload-pack: not our ref`. A worktree placed at the submodule path needs no clone: the
// pin is already in the submodule repository's object store, and the parent accepts the path as the
// submodule. `agenthub_client` goes on the seat's own branch of the client repository, so a seat's
// client commits land where the lead can see them with no fetch.
//
// `.claude` IS NOT A SUBMODULE AND IS NEVER ONE. Measured on this hub (2026-10-18): `.gitmodules`
// records `claude-automation` and `agenthub_client`, `git rev-parse HEAD:.claude` answers `path '.claude'
// exists on disk, but not in 'HEAD'`, and every derived worktree carries no `.claude` at all. The ruling
// that made it a detached submodule link was written against a checkout that recorded one; reading the
// gitlink here failed with git's own words and refused every start of every seat.
//
// A LIVE ROOM IS READ, NOT RE-DERIVED. A derived path that is already a registered worktree is KEPT:
// never re-pointed, never refused for the branch it holds (a seat's worktree moves onto the lane it is
// working), never given a submodule worktree it lacks. Only a path that is not registered is derived,
// which is what makes `up` a heal after a reboot instead of a refusal over six worktrees that are fine.
// The one write a kept path may still receive is its `.env` link, which is this client's own file and
// what the seat's runtime reads the provider key from.
//
// NOTHING IS OVERWRITTEN. A path that exists and is not a registered worktree is refused and named, and
// the refusal is an error that names every refused path - never a half-started room that reads as one
// that worked.
//
// THE ONE REMOVAL, and it is the migration off a hand-made room. A room made before this derivation
// existed kept the client repository's worktree at `<roomDir>/wt/<seat>-client`; git checks a branch out
// in exactly ONE worktree per repository, and the client link the derivation wants is
// `<roomDir>/wt/<seat>/agenthub_client` on that same branch, so that leftover obstructs every start of
// that seat with git's `a branch named ... already exists`. clearStaleClientWorktrees removes exactly
// that entry - the legacy path AND the seat's own branch, nothing else - before the derivation runs. The
// branch survives, so no seat's commit is lost; a `-client` worktree on another branch, and the branch
// held at any other path, are left for the operator exactly as before.
//
// The removal is only half of it: it leaves that seat's client branch unheld, and the link the derivation
// wants is ON that branch. So the client link ATTACHES to a branch that already exists (`git worktree
// add <path> <branch>`, which re-points nothing) and creates it at the parent's gitlink only when it does
// not exist yet. The seat's own worktree attaches the same way: a branch that exists with no holder is
// the seat's own branch, and checking it out at the seat's derived path re-points nothing.
//
// PRUNE IS THE NARROW HALF. It removes only `/tmp/wt-*`, `/tmp/gate-*` and the entries git marks
// prunable, and it removes them with `git worktree remove` and NOT `--force`, so a dirty worktree is
// named and left standing. A worktree whose detached HEAD is on no branch and is not in `main` is
// refused and named: removing it deletes its reflog, which is the only reference to that commit. Every
// other foreign entry - `~/__projects__/4genthub-urgent-subtask-details` is the ruling's example - is
// listed and never touched.
package clientworktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// clientSubmodule is the submodule a seat's worktree carries, on a branch of the seat's own.
const clientSubmodule = "agenthub_client"

// Entry is one entry of `git worktree list`: where it is, the branch it is on (empty when its HEAD is
// detached), and the two states git reports about it that prune must respect. Prunable means git has
// already lost the entry's working tree; locked means git is holding the entry back from every
// automatic cleanup, so prune names it and never removes it.
type Entry struct {
	Path     string
	Branch   string // "" when detached
	Detached bool
	Prunable bool
	Locked   bool
}

// gitWorktrees is the seam the tests replace - a package-level variable rather than an interface added
// for testing, which is the repo's rule. It sits BEHIND Read, so a test that drives a verb's decision
// drives the very reader the verb uses.
var gitWorktrees = readGitWorktrees

// Read is the ONE reader of `git worktree list --porcelain` in this package: Ensure, Check, `list` and
// `prune` all go through it, so the four can never disagree about which worktrees exist.
func Read(mainRoot string) ([]Entry, error) { return gitWorktrees(mainRoot) }

// readGitWorktrees runs git and parses what it prints.
func readGitWorktrees(mainRoot string) ([]Entry, error) {
	out, err := runGit(mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, &clientcmd.CodedError{
			Message: fmt.Sprintf("cannot read the worktrees of %s: %v", mainRoot, err),
			Code:    clientcmd.ExitUnavailable,
		}
	}
	return parseWorktreeList(out), nil
}

// parseWorktreeList is the porcelain format, read one record at a time. A `worktree` line opens a
// record and every following known line fills it in; an unknown line (bare, for example) is skipped
// rather than refused, because git adds lines to this format and a reader that refuses the unknown
// would stop working the day one appeared.
func parseWorktreeList(out string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			entries = append(entries, Entry{Path: strings.TrimPrefix(line, "worktree ")})
		case len(entries) == 0:
			continue
		case strings.HasPrefix(line, "branch "):
			entries[len(entries)-1].Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			entries[len(entries)-1].Detached = true
		case strings.HasPrefix(line, "prunable"):
			entries[len(entries)-1].Prunable = true
		case strings.HasPrefix(line, "locked"):
			entries[len(entries)-1].Locked = true
		}
	}
	return entries
}

// linkProviderKey links a directory's `.env` to the ONE env file. It is a seam, in the repo's style of
// a package-level variable rather than an interface added for testing: it links a machine's own key
// file, which a test cannot supply.
var linkProviderKey = LinkProviderKey

// LinkProviderKey is the ONE link: a directory's `.env` points at the client's env file, which is where
// omp reads the key from its launch directory. A regular file at that path is not ours and is REFUSED
// and named; a link of ours aimed at a key file that moved is retargeted.
func LinkProviderKey(dir string) error {
	source, err := clientenv.DefaultPath()
	if err != nil {
		return &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
	}
	if info, statErr := os.Stat(source); statErr != nil || info.IsDir() {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf(
				"%s does not exist: omp reads the key from the launch directory's .env", source),
			Code: clientcmd.ExitUsage,
		}
	}
	link := filepath.Join(dir, ".env")
	info, err := os.Lstat(link)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		if linked, statErr := os.Stat(link); statErr == nil {
			if srcInfo, srcErr := os.Stat(source); srcErr == nil && os.SameFile(linked, srcInfo) {
				return nil
			}
		}
		// Our own link, aimed at a key file that moved: retargeting is what the link is for.
		if err := os.Remove(link); err != nil {
			return &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
		}
	case err == nil:
		return &clientcmd.CodedError{
			Message: fmt.Sprintf(
				"%s already exists and is not a symlink: refusing to replace a file this client did "+
					"not create. Move it aside, or point it at %s, to use the one key.", link, source),
			Code: clientcmd.ExitUsage,
		}
	}
	if err := os.Symlink(source, link); err != nil {
		return &clientcmd.CodedError{Message: err.Error(), Code: clientcmd.ExitUsage}
	}
	return nil
}

// MainRoot is the checkout whose worktrees these are: the --main flag when the caller passed one, else
// $OPENRIG_SKILLS_ROOT, else the repository the client was run in, else a usage error that names the flag,
// the variable and the command that failed. It is never guessed, because a worktree registered in the
// wrong checkout is a worktree the seat cannot commit from - and the working directory is deliberately
// the LAST of the three, for the reason inside.
func MainRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Clean(flagValue), nil
	}
	// THE CHECKOUT VARIABLE COMES BEFORE THE WORKING DIRECTORY, and that order is the fix for a measured
	// fault: a seat's own client worktree is a worktree of the CLIENT repository, so
	// `git rev-parse --show-toplevel` run there names a different repository from the one the seats'
	// worktrees belong to, and every derived path is then reported as existing-but-not-a-worktree. The
	// variable is the same one clientservices.TeamDir reads first, so one variable points at the
	// repository whose `skills/`, `scripts/team/` and worktrees this client uses.
	if root := os.Getenv(clientservices.SkillsRootEnv); root != "" {
		return filepath.Clean(root), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("cannot find the checkout whose worktrees these are: the working "+
				"directory is unreadable (%v); pass --main DIR or set %s", err, clientservices.SkillsRootEnv),
			Code: clientcmd.ExitUsage,
		}
	}
	out, err := runGit(cwd, "rev-parse", "--show-toplevel")
	if err != nil || out == "" {
		return "", &clientcmd.CodedError{
			Message: fmt.Sprintf("cannot find the checkout whose worktrees these are: "+
				"`git rev-parse --show-toplevel` in %s named no repository (%v); pass --main DIR or set %s",
				cwd, err, clientservices.SkillsRootEnv),
			Code: clientcmd.ExitUsage,
		}
	}
	return filepath.Clean(out), nil
}

// SeatProjectRoot names the ONE repository the room's seat directories live in, read from the definition
// the start already resolved: each seat's own cwd, and git's common directory behind it.
//
// WHY IT EXISTS, measured 2026-10-18: `up --room-only --allow-peak` run from a seat's client worktree -
// and, the same way, from the client main checkout - refused 6 of 6 derived paths, each saying the path
// "exists and is not a worktree registered in `git worktree list` of <CLIENT repo>", while the same
// command from the project root printed six kept lines, exit 0. MainRoot falls back to the working
// directory, so the refusal named the repository `up` happened to run in rather than the one the room's
// seats belong to. The definition records the answer, so it is read rather than guessed.
//
// ok=false when NO seat directory could be read: a directory that is gone or is no repository says
// nothing, and the caller keeps the working directory's answer. A non-nil error means the readable seat
// directories name MORE THAN ONE repository; the caller keeps its own answer and says so, because a room
// whose seats span two repositories cannot be derived from a single root and guessing one would derive
// into the wrong tree.
func SeatProjectRoot(cwds []string) (string, bool, error) {
	var roots []string
	seen := map[string]bool{}
	for _, cwd := range cwds {
		if cwd == "" {
			continue
		}
		out, err := runGit(cwd, "rev-parse", "--git-common-dir")
		if err != nil || out == "" {
			continue
		}
		common := out
		if !filepath.IsAbs(common) {
			common = filepath.Join(cwd, common)
		}
		root := filepath.Clean(filepath.Dir(common))
		key := canonicalPath(root)
		if seen[key] {
			continue
		}
		seen[key] = true
		roots = append(roots, root)
	}
	switch len(roots) {
	case 0:
		return "", false, nil
	case 1:
		return roots[0], true, nil
	default:
		return "", false, fmt.Errorf("the seat directories belong to %s", strings.Join(roots, " and "))
	}
}

// canonicalPath is the ONE path identity this package compares with. A registered worktree's path
// exists, so EvalSymlinks resolves it; a derived path that does not exist yet cannot be resolved and
// keeps filepath.Clean. Comparing the raw strings (filepath.Clean alone) names the same directory twice
// when the room is reached through a symlink - git records the RESOLVED path for a worktree, measured:
// `git worktree add <symlinked>/wt/<seat>` lists the real one - so the derived path and the registered
// one differ as strings while naming one directory, and a legitimate reuse was refused.
func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// Ensure is the ruling's Start, over clientservices.DerivedWorktrees(roomDir, rig, seats): the room's
// seats in their own order, then the room's integration worktree when the room has a lead.
//
// For each derived worktree it creates the path when it is missing, adds the two submodule worktrees,
// and links the provider key where a seat will be launched - and where a writing worktree is one whose
// branch is not empty, which leaves the reviewer out: it is detached, so it is never launched to
// commit, and nothing in a reviewer's worktree links a key this client did not put there.
//
// EVERY derived worktree is attempted, never just up to the first refusal: `up` must not be left with
// half a room and a message about one path, so all refusals are collected, each is printed on out with
// its reason, and the returned error names every refused path.
//
// Before any of that it clears the pre-derivation client worktree a hand-made room left behind - the
// migration, and the only thing this step removes (clearStaleClientWorktrees).
func Ensure(mainRoot, roomDir, rig string, seats []clientservices.SeatType, out io.Writer) error {
	entries, err := Read(mainRoot)
	if err != nil {
		return err
	}
	registered := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		registered[canonicalPath(entry.Path)] = entry
	}
	derived := clientservices.DerivedWorktrees(roomDir, rig, seats)
	legacy, err := clearStaleClientWorktrees(mainRoot, roomDir, derived, out)
	if err != nil {
		return err
	}
	var refused []string
	for _, worktree := range derived {
		state, err := ensureOne(mainRoot, worktree, registered, legacy[worktree.SeatKey], out)
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s (%v)", worktree.Path, err))
			fmt.Fprintf(out, "%s: refused: %v\n", worktree.Path, err)
			continue
		}
		fmt.Fprintf(out, "%s: %s\n", worktree.Path, state)
	}
	if len(refused) > 0 {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("refused %d of %d derived worktree(s), naming every one: %s",
				len(refused), len(derived), strings.Join(refused, "; ")),
			Code: clientcmd.ExitUnavailable,
		}
	}
	return nil
}

// legacyClientSuffix names the ONE path in this package that is not derived: the place a room made by
// hand, before this derivation existed, put the client repository's worktree -
// `<roomDir>/wt/<seat>-client`. It is the migration's whole subject, so clearStaleClientWorktrees and
// this constant are deleted together once no room carries one.
const legacyClientSuffix = "-client"

// legacyClient is what the migration found at one seat's pre-derivation client path, and it is what the
// link placement needs: the branch the cleared worktree held, so the link attaches to the seat's own
// client commits, and whether the path is still there because git refused to remove work it holds - in
// which case nothing is placed for that seat, because a second client location is exactly what the
// ruling forbids.
type legacyClient struct {
	branch  string
	blocked bool
}

// clearStaleClientWorktrees is the migration, and it is deliberately narrow: for one derived writing
// seat it removes the CLIENT repository's worktree when - and only when - that worktree sits at exactly
// `<roomDir>/wt/<seat>-client`. The PATH is the subject, not the branch it holds: a seat's legacy
// worktree sits on the lane it worked, and a test for the derived branch name never matched one.
//
// Why removal and not a refusal: git checks a branch out in exactly one worktree per repository, and the
// client link the derivation wants - `<roomDir>/wt/<seat>/agenthub_client` - belongs on that same branch,
// so the leftover makes the link impossible to place. The branch survives the removal - only the working
// tree goes - so a seat's own commits are never lost, and the branch it held is answered per seat so the
// link can be placed on it afterwards.
//
// The removal is `git worktree remove` WITHOUT `--force`, so a legacy worktree holding uncommitted or
// untracked work is refused by git. That is a NOTE and not an error: the path is named, the seat keeps
// working there until it commits, the run continues, and Check names the path until its link exists.
func clearStaleClientWorktrees(mainRoot, roomDir string, derived []clientservices.Worktree, out io.Writer) (map[string]legacyClient, error) {
	found := make(map[string]legacyClient, len(derived))
	repository := filepath.Join(mainRoot, clientSubmodule)
	if info, err := os.Stat(repository); err != nil || !info.IsDir() {
		// linkSubmodules owns this diagnosis and words it as the missing submodule it is; there is no
		// worktree list to read until that is answered.
		return found, nil
	}
	entries, err := Read(repository)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byPath[canonicalPath(entry.Path)] = entry
	}
	for _, worktree := range derived {
		// EVERY derived seat, a detached one included: the record says `<seat>-client` is the
		// pre-derivation layout, and a reviewer's legacy is that layout too. Its link is detached at the
		// pin either way, so there is no branch a removal would need to preserve.
		legacyPath := filepath.Join(roomDir, "wt", worktree.SeatKey+legacyClientSuffix)
		entry, held := byPath[canonicalPath(legacyPath)]
		if !held {
			continue
		}
		if entry.Detached {
			// The ruling's one thing a removal must never take is the only reference to a commit: a
			// detached legacy whose HEAD main does not contain exists only in that worktree's reflog, so
			// it is refused and named, the same rule prune applies.
			inMain, err := headInMain(repository, entry.Path)
			switch {
			case err != nil:
				fmt.Fprintf(out, "%s: NOT cleared, cannot read its HEAD: %v\n", entry.Path, err)
				found[worktree.SeatKey] = legacyClient{blocked: true}
				continue
			case !inMain:
				fmt.Fprintf(out, "%s: refused, its detached HEAD is on no branch and is not in main, "+
					"and removing it would delete the only reference to that commit\n", entry.Path)
				found[worktree.SeatKey] = legacyClient{blocked: true}
				continue
			}
		}
		if _, err := runGit(repository, "worktree", "remove", entry.Path); err != nil {
			fmt.Fprintf(out, "%s: NOT cleared, the pre-derivation client worktree still holds work: %v\n"+
				"%s keeps its client code there until it commits; its link at %s waits for that\n",
				entry.Path, err, worktree.SeatKey, filepath.Join(worktree.Path, clientSubmodule))
			found[worktree.SeatKey] = legacyClient{blocked: true}
			continue
		}
		if entry.Branch != "" {
			found[worktree.SeatKey] = legacyClient{branch: entry.Branch}
			fmt.Fprintf(out, "%s: cleared (the pre-derivation client worktree; %s survives)\n",
				entry.Path, entry.Branch)
		} else {
			fmt.Fprintf(out, "%s: cleared (the pre-derivation client worktree; it was detached, nothing to lose)\n",
				entry.Path)
		}
	}
	return found, nil
}

// ensureOne is one derived worktree's three steps, and the ONE place the create/reuse/refuse decision
// is made. Every refusal is answered BEFORE anything is created, so a refusal never leaves a
// half-started path behind; it answers the word Ensure prints for that path.
func ensureOne(mainRoot string, worktree clientservices.Worktree, registered map[string]Entry, legacy legacyClient, out io.Writer) (string, error) {
	entry, isRegistered := registered[canonicalPath(worktree.Path)]
	if isRegistered {
		if entry.Prunable {
			// It is in the listing and its working tree is gone: git will refuse both to reuse it and to
			// add one at that path, so the path is named and nothing is invented to repair it.
			return "", fmt.Errorf(
				"%s is registered but git marks it prunable - its working tree is gone; run "+
					"`git -C %s worktree prune` and start again", worktree.Path, mainRoot)
		}
		// KEPT, not re-derived: the room has a worktree here and `up` reads it. Whatever branch it is on
		// is the branch the seat works on now, so it is neither refused nor moved. The two writes are the
		// ones the ruling still places: the missing client link, and this client's own `.env` link. A
		// legacy the migration could not clear keeps the seat's client code: no link is placed, so there
		// is never a second location, and Check names the path until the seat commits.
		if !legacy.blocked {
			placeClientLink(mainRoot, worktree, legacy.branch, out)
		}
		if err := linkKey(worktree); err != nil {
			return "", err
		}
		return "kept", nil
	}
	if _, err := os.Lstat(worktree.Path); err == nil {
		return "", fmt.Errorf(
			"%s exists and is not a worktree registered in `git worktree list` of %s; nothing is overwritten",
			worktree.Path, mainRoot)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("cannot read %s: %v", worktree.Path, err)
	}
	// A seat the migration moved commits its client code on the branch its legacy held, so a path this
	// run creates is linked on that branch too - not on the derived name, which may carry nothing.
	branch := legacy.branch
	if branch == "" {
		branch = worktree.Branch
	}
	if err := refuseClientBranch(mainRoot, worktree, branch); err != nil {
		return "", err
	}
	if err := createWorktree(mainRoot, worktree); err != nil {
		return "", err
	}
	if !legacy.blocked {
		// A legacy the migration could not clear keeps the seat's client code, so nothing is placed:
		// a link here would be the second client location the ruling forbids.
		if err := linkSubmodule(mainRoot, worktree, branch); err != nil {
			return "", err
		}
	}
	if err := linkKey(worktree); err != nil {
		return "", err
	}
	return "created", nil
}

// branchRefusal answers git's refusal about a branch that a WORKTREE HOLDS, before git is asked to
// create anything, and in words a room's operator can act on. Measured, not assumed: for a branch that
// already exists `git worktree add -b <branch>` prints `fatal: a branch named '<branch>' already
// exists`, whether or not a worktree holds it, and names neither the repository nor - when a worktree
// does hold it - that worktree.
//
// This client never re-points and never deletes a branch. `path` is the worktree that was not created,
// named so the refusal is actionable on its own.
//
// A branch that exists with NO holder is NOT answered here, because the two callers do different things
// with it: createWorktree refuses it in the parent repository (rejectBareBranch), and linkSubmodules
// attaches to it in the client repository, whose link belongs on that branch.
func branchRefusal(repository, branch, path string) error {
	if branch == "" {
		return nil
	}
	if entries, err := Read(repository); err == nil {
		for _, entry := range entries {
			if entry.Branch != branch {
				continue
			}
			if canonicalPath(entry.Path) == canonicalPath(path) {
				// This very path holds the branch: that is the reuse case, and not a refusal.
				return nil
			}
			if entry.Prunable {
				// git still names the branch, but the working tree is gone: the operator's fix is to
				// clear the entry, not to hunt for a worktree that no longer exists.
				return fmt.Errorf("the branch %s is registered to the worktree at %s in %s, and git marks "+
					"that entry prunable - its working tree is gone; run `git -C %s worktree prune` and "+
					"start again, so %s can be created", branch, entry.Path, repository, repository, path)
			}
			return fmt.Errorf("the branch %s is already checked out at %s in %s, and a branch is "+
				"checked out in exactly one worktree, so %s was not created",
				branch, entry.Path, repository, path)
		}
	}
	return nil
}

// clientBranchExists answers whether a repository already has a branch, which is the ONE thing that
// decides how a worktree is placed: a branch that exists is ATTACHED TO (`git worktree add <path>
// <branch>`, which re-points nothing), and a branch that does not is created at `main`. The two forms
// are not interchangeable - `git worktree add -b <branch>` is what git answers `fatal: a branch named
// ... already exists` to.
func clientBranchExists(repository, branch string) (bool, error) {
	_, err := runGit(repository, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false, fmt.Errorf("cannot read the branches of %s: %v", repository, err)
	}
	return false, nil
}

// createWorktree adds the one worktree the derivation named. A branch a worktree HOLDS is refused by
// name before git runs (branchRefusal); a branch that exists with NO holder is the seat's own branch -
// it carries the seat's commits, and checking it out at the seat's derived path touches neither the
// branch nor anyone else's worktree - so it is attached to. Only a branch that does not exist is
// created, at `main`. A reviewer's worktree, whose branch is empty, is detached.
//
// The parent directory is made before git runs because a room's `wt/` is this client's own directory
// and nothing else creates it.
func createWorktree(mainRoot string, worktree clientservices.Worktree) error {
	if err := branchRefusal(mainRoot, worktree.Branch, worktree.Path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(worktree.Path), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %v", filepath.Dir(worktree.Path), err)
	}
	var err error
	switch {
	case worktree.Branch == "":
		_, err = runGit(mainRoot, "worktree", "add", "--detach", worktree.Path, "main")
	default:
		exists, existsErr := clientBranchExists(mainRoot, worktree.Branch)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			_, err = runGit(mainRoot, "worktree", "add", worktree.Path, worktree.Branch)
		} else {
			_, err = runGit(mainRoot, "worktree", "add", "-b", worktree.Branch, worktree.Path, "main")
		}
	}
	if err != nil {
		return fmt.Errorf("cannot create the worktree at %s: %v", worktree.Path, err)
	}
	return nil
}

// refuseClientBranch answers the CLIENT repository's branch refusal before the parent worktree is
// created. linkSubmodule asks the same question, but it runs after createWorktree, so its refusal left
// the seat's worktree created without its client worktree - a half-started path nobody can use, and an
// `up` transcript that reads `created` for it. Asking first means a refusal leaves nothing behind.
func refuseClientBranch(mainRoot string, worktree clientservices.Worktree, branch string) error {
	if branch == "" {
		return nil
	}
	return branchRefusal(filepath.Join(mainRoot, clientSubmodule), branch,
		filepath.Join(worktree.Path, clientSubmodule))
}

// linkSubmodule places the client worktree inside a seat's worktree, at the parent's own gitlink, on the
// branch it is given: the derived branch for a path this run creates, and the branch the seat's legacy
// worktree held for a path the migration freed. A path that is already a worktree (its `.git` is there)
// is left alone, which is what makes a second Ensure a no-op.
//
// The client submodule carries a branch, and it has two forms: a branch that already exists is ATTACHED
// to, and a branch that does not is created at the parent's gitlink. An empty branch is a reviewer's,
// whose link is detached at the pin. `.claude` is not linked at all: it is not a submodule of this hub
// and has no gitlink to place.
func linkSubmodule(mainRoot string, worktree clientservices.Worktree, branch string) error {
	target := filepath.Join(worktree.Path, clientSubmodule)
	if _, err := os.Lstat(filepath.Join(target, ".git")); err == nil {
		return nil
	}
	repository := filepath.Join(mainRoot, clientSubmodule)
	if info, err := os.Stat(repository); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a checked-out submodule repository of %s", repository, mainRoot)
	}
	pin, err := gitlink(mainRoot, clientSubmodule)
	if err != nil {
		return fmt.Errorf("cannot read the %s gitlink recorded in %s: %v", clientSubmodule, mainRoot, err)
	}
	args := []string{"worktree", "add"}
	if branch == "" {
		// A reviewer's worktree never commits, so its client link is detached at the pin.
		args = append(args, "--detach", target, pin)
	} else {
		// The client repository carries its own branch, and git refuses an existing branch a worktree
		// holds in the same unhelpful words - so it is refused by name too.
		if err := branchRefusal(repository, branch, target); err != nil {
			return err
		}
		exists, err := clientBranchExists(repository, branch)
		if err != nil {
			return err
		}
		if exists {
			// The branch carries the seat's own client commits - it is exactly the branch this link
			// belongs on. `git worktree add <target> <branch>` places the link on it and re-points
			// nothing, deletes nothing and adopts nothing by itself.
			args = append(args, target, branch)
		} else {
			args = append(args, "-b", branch, target, pin)
		}
	}
	if _, err := runGit(repository, args...); err != nil {
		return fmt.Errorf("cannot add the %s worktree at %s: %v", clientSubmodule, target, err)
	}
	return nil
}

// placeClientLink is the one write a KEPT path receives besides its `.env` link: the client link the
// ruling says stays at `<roomDir>/wt/<seat>/agenthub_client`. It attaches to the branch the seat's
// legacy `<seat>-client` held when the migration removed it, else to the branch the derivation names,
// and creates that branch at the parent's gitlink when it does not exist.
//
// A failure is a printed NOTE, not a refusal: the branch may be held by a worktree somewhere this run
// does not own, and failing here would leave the rest of the room unattempted. Check reports the path
// as Wrong until its link exists, which is the truthful state.
func placeClientLink(mainRoot string, worktree clientservices.Worktree, legacyBranch string, out io.Writer) {
	target := filepath.Join(worktree.Path, clientSubmodule)
	if _, err := os.Lstat(filepath.Join(target, ".git")); err == nil {
		return
	}
	branch := legacyBranch
	if branch == "" {
		branch = worktree.Branch
	}
	if err := linkSubmodule(mainRoot, worktree, branch); err != nil {
		fmt.Fprintf(out, "%s: NOTE the client link was not placed: %v\n", worktree.Path, err)
	}
}

// gitlink is the commit the parent records for one submodule, which is the pin every seat worktree and
// every gate is started at.
func gitlink(mainRoot, submodule string) (string, error) {
	out, err := runGit(mainRoot, "rev-parse", "HEAD:"+submodule)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("%s records no commit for %s", mainRoot, submodule)
	}
	return fields[0], nil
}

// linkKey links the provider key into a WRITING worktree's own `.env`, through LinkProviderKey
// implementation: omp reads the key from the launch directory, and a seat launched in its own worktree
// is not launched in the room. A worktree with no branch is a reviewer's: it is detached and never
// commits, so it is not a writing seat's and gets no link.
func linkKey(worktree clientservices.Worktree) error {
	if worktree.Branch == "" {
		return nil
	}
	if err := linkProviderKey(worktree.Path); err != nil {
		return fmt.Errorf("cannot link the provider key into %s: %v", worktree.Path, err)
	}
	return nil
}

// runGit runs git in dir and returns its output. Every git call in this package goes through it, so
// there is ONE place that decides the working directory and one place that reports a failure with
// git's own words.
func runGit(dir string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	raw, err := command.CombinedOutput()
	out := strings.TrimSpace(string(raw))
	if err != nil {
		said := ""
		if out != "" {
			said = ": " + out
		}
		return out, fmt.Errorf("git %s (in %s): %w%s", strings.Join(args, " "), dir, err, said)
	}
	return out, nil
}
