package clientworktree

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// This file is the two verbs founded on the reading in inventory.go.
//
// `status` prints it. `clean` decides from it, and acts only with --apply.
//
// THE SAFETY OF A CLEAN IS WHAT IT REFUSES. Owner, 2026-11-01: the rooms had grown to 37 GB and nobody
// could tell what had merged, and a cleaner that loses work would be worse than the sprawl. So a worktree
// goes only when ALL of this is true: it is a linked git worktree under a room's wt/; it is not named
// .obsolete; no seat is live on it and no process has its working directory in it; it has no uncommitted
// or untracked file and no ignored file that is not known to come back; its branch has no commit the base
// lacks; everything nested inside it has gone first. Anything else is listed as a decision, with every
// reason, and left exactly as it was. Nothing here passes --force, nothing touches a remote ref, a stash
// or a tag, and a branch is deleted only by `git branch -d`, so git keeps its own veto.
//
// Every worktree is read AGAIN right before it is removed, because the plan may be minutes old by then.

const (
	actionRemove  = "remove"
	actionRemoved = "removed"
	actionDecide  = "decide"
	actionDelete  = "delete"
	actionDeleted = "deleted"
	actionKeep    = "keep"
)

// StatusReport is what `worktree status --json` prints.
type StatusReport struct {
	Worktrees []Tree   `json:"worktrees"`
	Repos     []Repo   `json:"repos"`
	Notes     []string `json:"notes,omitempty"`
}

// CacheAction is one cache `worktree clean` drops or would drop.
type CacheAction struct {
	Cache
	Dropped bool `json:"dropped"`
}

// CleanReport is what `worktree clean --json` prints.
type CleanReport struct {
	Applied   bool          `json:"applied"`
	Worktrees []Tree        `json:"worktrees"`
	Branches  []Branch      `json:"branches"`
	Caches    []CacheAction `json:"caches"`
	Reclaim   int64         `json:"reclaim_bytes"`
	Failures  []string      `json:"failures,omitempty"`
	Notes     []string      `json:"notes,omitempty"`
}

type cleanOptions struct {
	apply       bool
	branch      string
	keep        []string
	dropIgnored bool
}

// refusals is every reason this node stays. Empty means it may be removed.
func refusals(n *node, opts cleanOptions) []string {
	var why []string
	switch n.Kind {
	case KindPlain:
		if plainHoldsFiles(n) {
			why = append(why, "a plain directory with no .git: nothing can tell what it holds")
		}
	case KindBroken:
		why = append(why, "its .git cannot be followed, so what it holds cannot be read")
	case KindClone:
		why = append(why, "a repository of its own, not a linked worktree: its commits exist nowhere else")
	case KindSubmodule:
		why = append(why, "a submodule's own clone, not a linked worktree: its commits exist nowhere else")
	}
	if n.Obsolete {
		why = append(why, "named .obsolete: retired work waiting for an owner decision")
	}
	if n.live != nil && n.live.unknown != "" {
		why = append(why, "the room's seats could not be read: "+n.live.unknown)
	}
	if n.SeatLive {
		why = append(why, "live seat "+n.Seat)
	}
	if len(n.BusyPIDs) > 0 {
		why = append(why, fmt.Sprintf("in use by process(es) %v", n.BusyPIDs))
	}
	if n.unreadable != "" {
		why = append(why, "could not be read: "+n.unreadable)
	}
	if n.Locked {
		why = append(why, "git locks it")
	}
	if n.Kind == KindLinked {
		if n.Uncommitted > 0 {
			why = append(why, fmt.Sprintf("%d uncommitted", n.Uncommitted))
		}
		if n.Untracked > 0 {
			why = append(why, fmt.Sprintf("%d untracked", n.Untracked))
		}
		if n.Base == "" {
			why = append(why, "its repository has no base branch to compare with")
		} else if !n.Detached && !n.Merged {
			why = append(why, fmt.Sprintf("%d commit(s) ahead of %s", n.Ahead, n.Base))
		}
		if len(n.Ignored) > 0 && !opts.dropIgnored {
			why = append(why, "ignored files git would delete with it (--drop-ignored to accept): "+listText(n.Ignored))
		}
		if n.hasModules {
			why = append(why, "holds a submodule clone, and git removes that only with --force, which this client never passes")
		}
	}
	for _, child := range n.children {
		if child.Action != actionRemove && child.Action != actionRemoved {
			why = append(why, "holds "+child.Path+", which stays")
		}
	}
	return why
}

// plainHoldsFiles answers whether a plain directory holds anything but what nested worktrees account for.
func plainHoldsFiles(n *node) bool {
	skip := map[string]bool{}
	for _, child := range n.descendants() {
		skip[child.Path] = true
	}
	return holdsFiles(n.Path, skip)
}

func listText(items []string) string {
	if len(items) > 5 {
		return strings.Join(items[:5], ", ") + fmt.Sprintf(" and %d more", len(items)-5)
	}
	return strings.Join(items, ", ")
}

var refUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// preserveName is the branch that keeps a detached commit nothing else reaches. It is a branch and not a
// hidden ref on purpose: it reads as unmerged in every later status, so the commit stays somebody's
// decision instead of a thing nobody can find.
func preserveName(n *node) string {
	rel, _ := filepath.Rel(n.wtRoot, n.Path)
	short := n.Head
	if len(short) > 7 {
		short = short[:7]
	}
	return "preserved/" + n.Room + "/" + strings.Trim(refUnsafe.ReplaceAllString(rel, "-"), "-.") + "-" + short
}

// deepestFirst orders nodes so that everything nested is handled before what holds it.
func deepestFirst(nodes []*node) []*node {
	out := append([]*node(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i].Path, string(filepath.Separator)) > strings.Count(out[j].Path, string(filepath.Separator))
	})
	return out
}

// selected answers whether a node is in this clean's reach at all. With --branch only the worktrees
// holding that branch are, and a plain directory or a stranger stays out of a land's report.
func selected(n *node, opts cleanOptions) bool {
	if n.OutOfScope {
		return false
	}
	return opts.branch == "" || n.Branch == opts.branch
}

// plan decides every node, innermost first, so an outer worktree can read what its inner ones will do.
func plan(inv *inventory, opts cleanOptions) {
	for _, n := range deepestFirst(inv.nodes) {
		n.Reasons, n.PreserveAs, n.Action = nil, "", ""
		if !selected(n, opts) {
			// No action at all: an outer worktree reads this as a nested tree that stays, which it is. Calling
			// it removable here would let the outer removal take a tree this run never looked at.
			continue
		}
		n.Reasons = refusals(n, opts)
		switch {
		case len(n.Reasons) > 0:
			n.Action = actionDecide
		default:
			n.Action = actionRemove
			if n.Kind == KindLinked && n.Detached && !n.Reachable {
				n.PreserveAs = preserveName(n)
			}
		}
	}
}

// cacheEligible is whether a cache may be dropped from a worktree that stays. Its content is rebuildable,
// but a live seat is building into it, and an .obsolete tree stays exactly as it was found.
func cacheEligible(n *node, opts cleanOptions) bool {
	return selected(n, opts) && n.Kind == KindLinked && !n.Obsolete && !n.SeatLive && !n.Locked &&
		len(n.BusyPIDs) == 0 && (n.live == nil || n.live.unknown == "")
}

// branchScope answers whether this clean may touch a branch. A named room reaches its own `<room>/`
// branches and what its removals free; --all and --main reach the whole repository.
func branchScope(inv *inventory, name string, freed map[string]bool, opts cleanOptions) bool {
	if opts.branch != "" {
		return name == opts.branch
	}
	if inv.opts.all || inv.opts.main != "" || freed[name] {
		return true
	}
	for _, room := range inv.opts.rooms {
		if strings.HasPrefix(name, room+"/") {
			return true
		}
	}
	return false
}

func keptByGlob(name string, globs []string) bool {
	for _, glob := range globs {
		if ok, err := path.Match(glob, name); err == nil && ok {
			return true
		}
	}
	return false
}

// planBranches decides every branch of every repository reached. A branch is deleted only when it is
// merged, is not the base, matches no --keep, and holds no worktree that stays.
func planBranches(inv *inventory, opts cleanOptions) {
	removing := map[string]string{} // canonical path of a worktree this run removes -> its branch
	freed := map[string]bool{}
	for _, n := range inv.nodes {
		if n.Action == actionRemove || n.Action == actionRemoved {
			if n.Branch != "" && selected(n, opts) {
				removing[canonicalPath(n.Path)] = n.Branch
				freed[n.Branch] = true
			}
		}
	}
	for _, common := range inv.repoOrder {
		repo := inv.repos[common]
		if inv.onlyRepo != "" && common != inv.onlyRepo {
			continue
		}
		base := strings.TrimPrefix(repo.Base, "origin/")
		for i := range repo.Branches {
			branch := &repo.Branches[i]
			branch.Action, branch.Reason = "", ""
			if !branchScope(inv, branch.Name, freed, opts) {
				continue
			}
			held := branch.Worktree != "" && pathExists(branch.Worktree) && removing[canonicalPath(branch.Worktree)] == ""
			switch {
			case branch.Name == base || branch.Name == "main" || branch.Name == "master":
				branch.Action, branch.Reason = actionKeep, "the base line"
			case keptByGlob(branch.Name, opts.keep):
				branch.Action, branch.Reason = actionKeep, "matches --keep"
			case repo.Base == "":
				branch.Action, branch.Reason = actionDecide, "its repository has no base branch to compare with"
			case !branch.Merged:
				branch.Action = actionDecide
				branch.Reason = fmt.Sprintf("%d commit(s) ahead of %s", branch.Ahead, repo.Base)
			case held:
				branch.Action, branch.Reason = actionKeep, "merged, but checked out at "+branch.Worktree
			default:
				branch.Action = actionDelete
			}
		}
	}
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// apply carries the plan out, innermost first, and turns anything that went differently into a decision
// or a failure. It re-reads every worktree first.
func apply(inv *inventory, opts cleanOptions, out *CleanReport) {
	touched := map[string]bool{}
	for _, n := range deepestFirst(inv.nodes) {
		if n.Action != actionRemove || !selected(n, opts) {
			continue
		}
		inv.inspect(n, processCwds())
		if why := refusals(n, opts); len(why) > 0 {
			n.Action, n.Reasons = actionDecide, why
			continue
		}
		if n.Kind == KindPlain {
			// Nothing but empty folders is left in it, read a moment ago; a nested worktree that left it empty
			// may already have taken it away.
			if err := os.RemoveAll(n.Path); err != nil {
				n.Action, n.Reasons = actionDecide, []string{"could not be removed: " + err.Error()}
			} else {
				n.Action, n.done = actionRemoved, true
			}
			continue
		}
		if n.Detached && !n.Reachable {
			n.PreserveAs = preserveName(n)
			if err := preserveCommit(n); err != nil {
				n.Action, n.Reasons = actionDecide, []string{"the detached commit could not be preserved, so it stays: " + err.Error()}
				out.Failures = append(out.Failures, n.Path+": "+err.Error())
				continue
			}
		}
		if n.envLink {
			if err := os.Remove(filepath.Join(n.Path, ".env")); err != nil {
				n.Action, n.Reasons = actionDecide, []string{"its .env link could not be removed: " + err.Error()}
				continue
			}
		}
		if err := gitWrite(n.repo.commonDir, "--git-dir="+n.repo.commonDir, "worktree", "remove", n.Path); err != nil {
			n.Action, n.Reasons = actionDecide, []string{"git refused to remove it: " + err.Error()}
			out.Failures = append(out.Failures, n.Path+": "+err.Error())
			continue
		}
		n.Action, n.done = actionRemoved, true
		touched[n.repo.commonDir] = true
		afterRemoval(n)
	}
	dropCaches(inv, opts, out)
	for _, common := range inv.repoOrder {
		if touched[common] {
			if err := gitWrite(common, "--git-dir="+common, "worktree", "prune"); err != nil {
				out.Failures = append(out.Failures, "git worktree prune in "+inv.repos[common].Main+": "+err.Error())
			}
		}
	}
	planBranches(inv, opts)
	deleteBranches(inv, out)
}

// preserveCommit makes the branch that keeps a detached commit, unless a run before this one already did.
func preserveCommit(n *node) error {
	if tip, err := gitRead(n.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+n.PreserveAs); err == nil {
		if strings.TrimSpace(tip) == n.Head {
			return nil
		}
		return fmt.Errorf("%s already exists at another commit", n.PreserveAs)
	}
	return gitWrite(n.Path, "branch", n.PreserveAs, n.Head)
}

// afterRemoval leaves the parent the way git expects. When the removed path was the parent's gitlink, git
// reads a missing directory as a deleted submodule and an empty one as unchanged, so the parent's own
// removal needs the empty directory there. Any other folder left empty below wt/ is taken away.
func afterRemoval(n *node) {
	if n.parent != nil && n.parent.Kind == KindLinked {
		if rel, err := filepath.Rel(n.parent.Path, n.Path); err == nil {
			if stage, err := gitRead(n.parent.Path, "ls-files", "-s", "--", rel); err == nil && strings.HasPrefix(stage, "160000 ") {
				_ = os.MkdirAll(n.Path, 0o755)
				return
			}
		}
	}
	for dir := filepath.Dir(n.Path); dir != n.wtRoot && strings.HasPrefix(dir, n.wtRoot); dir = filepath.Dir(dir) {
		if n.parent != nil && dir == n.parent.Path {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
	}
}

// dropCaches drops every rebuildable cache of a worktree that stays. What went with a removed worktree is
// already gone. Each one is re-read here, so a directory that stopped being a cache is left alone.
func dropCaches(inv *inventory, opts cleanOptions, out *CleanReport) {
	procs := processCwds()
	for _, n := range inv.nodes {
		if n.done || len(n.Caches) == 0 {
			continue
		}
		n.BusyPIDs = busyPIDs(n.Path, procs)
		if !cacheEligible(n, opts) {
			continue
		}
		for _, cache := range n.Caches {
			info, err := os.Lstat(cache.Path)
			if err != nil || !info.IsDir() || !isCacheDir(cache.Path, filepath.Base(cache.Path)) {
				continue
			}
			if err := os.RemoveAll(cache.Path); err != nil {
				out.Failures = append(out.Failures, cache.Path+": "+err.Error())
				continue
			}
			markCache(out, cache)
		}
	}
}

// markCache records a dropped cache. One the plan had going with a worktree that then stayed is not on
// the list yet, and it was still dropped.
func markCache(out *CleanReport, cache Cache) {
	for i := range out.Caches {
		if out.Caches[i].Path == cache.Path {
			out.Caches[i].Dropped = true
			return
		}
	}
	out.Caches = append(out.Caches, CacheAction{Cache: cache, Dropped: true})
}

// deleteBranches runs `git branch -d` from a checkout whose HEAD contains the branch. `-d` measures
// "merged" against HEAD (or the branch's upstream), so running it from a checkout on another line would
// refuse a branch that is merged; git keeps its veto over the answer either way.
func deleteBranches(inv *inventory, out *CleanReport) {
	for _, common := range inv.repoOrder {
		repo := inv.repos[common]
		if inv.onlyRepo != "" && common != inv.onlyRepo {
			continue
		}
		for i := range repo.Branches {
			branch := &repo.Branches[i]
			if branch.Action != actionDelete {
				continue
			}
			checkout := checkoutContaining(repo, branch.Tip)
			if checkout == "" {
				branch.Action, branch.Reason = actionDecide, "merged, but no checkout's HEAD contains it, so `git branch -d` would refuse"
				continue
			}
			if err := gitWrite(checkout, "branch", "-d", branch.Name); err != nil {
				branch.Action, branch.Reason = actionDecide, "git refused: "+err.Error()
				continue
			}
			branch.Action = actionDeleted
		}
	}
}

func checkoutContaining(repo *Repo, tip string) string {
	for _, entry := range repo.entries {
		if !pathExists(entry.Path) || entry.Prunable {
			continue
		}
		if _, err := gitRead(entry.Path, "merge-base", "--is-ancestor", tip, "HEAD"); err == nil {
			return entry.Path
		}
	}
	return ""
}

// buildCleanReport folds the nodes and branches into what is printed, keeping only what this clean
// reached. The reclaim is what the plan frees, or what the apply actually freed.
func buildCleanReport(inv *inventory, opts cleanOptions, report *CleanReport) {
	report.Worktrees = []Tree{}
	report.Branches = []Branch{}
	if report.Caches == nil {
		report.Caches = []CacheAction{}
	}
	report.Reclaim = 0
	for _, n := range deepestFirst(inv.nodes) { // the order they are removed in, so the output reads true
		if !selected(n, opts) {
			continue
		}
		report.Worktrees = append(report.Worktrees, n.Tree)
		if n.Action == actionRemove || n.Action == actionRemoved {
			report.Reclaim += n.SizeBytes
		}
	}
	for _, cache := range report.Caches {
		if cache.Dropped || !report.Applied {
			report.Reclaim += cache.Bytes
		}
	}
	for _, common := range inv.repoOrder {
		for _, branch := range inv.repos[common].Branches {
			if branch.Action != "" {
				report.Branches = append(report.Branches, branch)
			}
		}
	}
	report.Notes = inv.notes
}

// plannedCaches is every cache a stay-put worktree would lose: a removed one's go with it, counted in its size.
func plannedCaches(inv *inventory, opts cleanOptions) []CacheAction {
	caches := []CacheAction{}
	for _, n := range inv.nodes {
		if n.Action == actionRemove || !cacheEligible(n, opts) {
			continue
		}
		for _, cache := range n.Caches {
			caches = append(caches, CacheAction{Cache: cache})
		}
	}
	return caches
}

const statusUsage = `usage: 4genteam worktree status (ROOM | --all | --main DIR) [--base BRANCH] [--no-size] [--json]
                                [--state-root DIR]

Read every worktree under a room's wt/, nested ones included, and every local branch of the repositories
they belong to. It writes nothing and takes no git lock, so it is safe beside a live room.

worktree: repo, branch or detached, merged into the base or commits ahead, uncommitted and untracked
counts, size, last commit date, the seat that owns it and whether that seat is live.
branch: merged or commits ahead, date, subject, the worktree holding it.
repository: its base, stash count, prunable entries, and the worktrees it registers outside every room.

options:
  ROOM              one room
  --all             every room under the store
  --main DIR        one repository, in every room holding a worktree of it; adds its branches when no
                    worktree reaches them (a project whose rooms have been cleaned)
  --base BRANCH     measure "merged" against this (default: origin's HEAD, else main, else master)
  --no-size         skip the size walk
  --json            the same report as one JSON document
  --state-root DIR  the store the rooms are read under (default: this machine's own)
`

const cleanUsage = `usage: 4genteam worktree clean (ROOM | --all | --main DIR) [--apply] [--branch BRANCH] [--keep GLOB]...
                               [--drop-ignored] [--base BRANCH] [--no-size] [--json] [--state-root DIR]

Dry run unless --apply. It removes clean merged worktrees (nested ones first), deletes merged local
branches and drops rebuildable caches (a target with a valid CACHEDIR.TAG, a node_modules nothing
tracks). Everything else is listed as a decision, with every reason, and left as it was:

  uncommitted or untracked files, commits the base lacks, a live seat, a process working in it, a lock,
  a .obsolete name, a plain directory, a clone or a broken link, a worktree nested inside that stays, a
  worktree outside every room, ignored files that would not come back (see --drop-ignored).

It passes no --force, never touches a remote ref, a stash or a tag, and deletes a branch only with
git branch -d. A detached commit no ref reaches gets the branch preserved/<room>/<worktree>-<sha> first.

options:
  ROOM              one room: its worktrees, its <room>/ branches, and what its removals free
  --all             every room; every merged branch of the repositories they use
  --main DIR        one repository: its worktrees in any room, every merged branch of it
  --apply           act; without it nothing changes
  --branch BRANCH   only this branch and the worktrees holding it, the call for right after a land; a
                    branch left behind exits 3
  --keep GLOB       never delete a branch matching this (repeatable), for a long-lived merged line
  --drop-ignored    accept losing a worktree's ignored files with it (an untagged target included)
  --base BRANCH     measure "merged" against this (default: origin's HEAD, else main, else master)
  --no-size         skip the size walk
  --json            the same report as one JSON document
  --state-root DIR  the store the rooms are read under (default: this machine's own)

Exit: 0 when everything it set out to do happened, a decision is not a failure; 3 when an action failed;
5 when a seat other than the room's lead asks for --apply.
`

// stringList is a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parseScope parses a verb's flags on either side of the room name, and answers the room when one is named.
func parseScope(flagSet *flag.FlagSet, args []string, usage string, stdout io.Writer) ([]string, error) {
	var rooms []string
	rest := args
	for {
		if err := flagSet.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(stdout, usage)
				return nil, errHelpShown
			}
			return nil, usageError(err.Error())
		}
		if flagSet.NArg() == 0 {
			break
		}
		rooms = append(rooms, flagSet.Arg(0))
		rest = append([]string(nil), flagSet.Args()[1:]...)
	}
	if len(rooms) > 1 {
		return nil, usageError(fmt.Sprintf("unexpected argument %q - this verb takes one room name", rooms[1]))
	}
	for _, room := range rooms {
		if strings.TrimSpace(room) == "" {
			return nil, usageError("the room name is empty")
		}
	}
	return rooms, nil
}

func runStatus(args []string, stdout io.Writer) error {
	root, args, err := verbRoot(args)
	if err != nil {
		return err
	}
	flags := newVerbFlags("status")
	all := flags.Bool("all", false, "")
	mainFlag := flags.String("main", "", "")
	base := flags.String("base", "", "")
	noSize := flags.Bool("no-size", false, "")
	asJSON := flags.Bool("json", false, "")
	rooms, err := parseScope(flags, args, statusUsage, stdout)
	if err != nil {
		return err
	}
	opts := scanOptions{root: root, rooms: rooms, all: *all, main: *mainFlag, base: *base, noSize: *noSize}
	if err := checkScope(opts); err != nil {
		return err
	}
	inv, err := scan(opts)
	if err != nil {
		return err
	}
	report := StatusReport{Worktrees: []Tree{}, Repos: []Repo{}, Notes: inv.notes}
	for _, n := range inv.nodes {
		if !n.OutOfScope {
			report.Worktrees = append(report.Worktrees, n.Tree)
		}
	}
	for _, common := range inv.repoOrder {
		if inv.onlyRepo == "" || common == inv.onlyRepo {
			report.Repos = append(report.Repos, *inv.repos[common])
		}
	}
	if *asJSON {
		return writeJSON(stdout, report)
	}
	printStatus(stdout, report)
	return nil
}

func checkScope(opts scanOptions) error {
	if len(opts.rooms) == 0 && !opts.all && opts.main == "" {
		return usageError("name a scope: ROOM, --all or --main DIR - there is no default room")
	}
	if opts.all && (len(opts.rooms) > 0 || opts.main != "") {
		return usageError("--all already covers every room: pass ROOM or --main DIR without it")
	}
	return nil
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func treeState(t Tree) string {
	switch {
	case t.Kind != KindLinked:
		return t.Kind
	case t.Detached && !t.Reachable:
		return "detached-unreachable"
	case t.Detached:
		return "detached"
	case t.Merged:
		return "merged"
	default:
		return fmt.Sprintf("ahead=%d", t.Ahead)
	}
}

func treeLine(t Tree) string {
	line := fmt.Sprintf("%s %s", treeState(t), t.Path)
	if t.Repo != "" {
		line += " repo=" + t.Repo
	}
	if t.Branch != "" {
		line += " branch=" + t.Branch
	}
	if t.Kind == KindLinked {
		line += fmt.Sprintf(" dirty=%d untracked=%d", t.Uncommitted, t.Untracked)
		if t.LastCommit != "" {
			line += " last=" + t.LastCommit
		}
	}
	line += " size=" + byteText(t.SizeBytes)
	if t.Seat != "" {
		state := "stopped"
		if t.SeatLive {
			state = "live"
		}
		line += fmt.Sprintf(" seat=%s(%s)", t.Seat, state)
	}
	if len(t.BusyPIDs) > 0 {
		line += fmt.Sprintf(" busy=%v", t.BusyPIDs)
	}
	if t.Obsolete {
		line += " obsolete"
	}
	return line
}

func printStatus(w io.Writer, report StatusReport) {
	room := ""
	var total int64
	for _, tree := range report.Worktrees {
		if tree.Room != room {
			room = tree.Room
			fmt.Fprintf(w, "room %s\n", room)
		}
		total += tree.SizeBytes
		fmt.Fprintf(w, "  %s\n", treeLine(tree))
	}
	for _, repo := range report.Repos {
		merged := 0
		for _, branch := range repo.Branches {
			if branch.Merged {
				merged++
			}
		}
		fmt.Fprintf(w, "repo %s (%s) base=%s branches=%d merged=%d stashes=%d prunable=%d\n",
			repo.Name, repo.Main, repo.Base, len(repo.Branches), merged, repo.Stashes, repo.Prunable)
		for _, branch := range repo.Branches {
			state := "merged"
			if !branch.Merged {
				state = fmt.Sprintf("ahead=%d", branch.Ahead)
			}
			fmt.Fprintf(w, "  %s %s %s %s\n", state, branch.Name, branch.Date, branch.Subject)
		}
		for _, foreign := range repo.Foreign {
			fmt.Fprintf(w, "  foreign %s %s\n", foreign.Path, foreign.Branch)
		}
	}
	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
	fmt.Fprintf(w, "total: %d worktree(s), %s\n", len(report.Worktrees), byteText(total))
}

func runClean(args []string, stdout io.Writer) error {
	root, args, err := verbRoot(args)
	if err != nil {
		return err
	}
	flags := newVerbFlags("clean")
	all := flags.Bool("all", false, "")
	mainFlag := flags.String("main", "", "")
	base := flags.String("base", "", "")
	noSize := flags.Bool("no-size", false, "")
	asJSON := flags.Bool("json", false, "")
	applyFlag := flags.Bool("apply", false, "")
	branch := flags.String("branch", "", "")
	dropIgnored := flags.Bool("drop-ignored", false, "")
	var keep stringList
	flags.Var(&keep, "keep", "")
	rooms, err := parseScope(flags, args, cleanUsage, stdout)
	if err != nil {
		return err
	}
	scope := scanOptions{root: root, rooms: rooms, all: *all, main: *mainFlag, base: *base, noSize: *noSize}
	if err := checkScope(scope); err != nil {
		return err
	}
	if *applyFlag {
		if err := refuseSeat(scope); err != nil {
			return err
		}
	}
	opts := cleanOptions{apply: *applyFlag, branch: *branch, keep: keep, dropIgnored: *dropIgnored}
	inv, err := scan(scope)
	if err != nil {
		return err
	}
	plan(inv, opts)
	planBranches(inv, opts)
	report := CleanReport{Applied: *applyFlag, Caches: plannedCaches(inv, opts)}
	if opts.apply {
		apply(inv, opts, &report)
	}
	buildCleanReport(inv, opts, &report)
	if *asJSON {
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
	} else {
		printClean(stdout, report)
	}
	return cleanExit(report, opts)
}

// refuseSeat keeps --apply from a seat that is not the lead of the room it names. The merge routine makes
// the lead the one who removes a branch after a land; any other seat reaching for this is one seat's
// process removing another's tree.
func refuseSeat(scope scanOptions) error {
	seat := strings.TrimSpace(os.Getenv("AGENTHUB_SEAT"))
	if seat == "" {
		return nil
	}
	own := strings.TrimSpace(os.Getenv("AGENTHUB_ROOM"))
	if seat == "lead" && len(scope.rooms) == 1 && scope.rooms[0] == own && !scope.all {
		return nil
	}
	return &clientcmd.CodedError{
		Message: fmt.Sprintf("seat %s may read this but not --apply it: only a room's lead, on its own room, or the owner cleans", seat),
		Code:    clientcmd.ExitRefused,
	}
}

func cleanExit(report CleanReport, opts cleanOptions) error {
	if len(report.Failures) > 0 {
		return &clientcmd.CodedError{
			Message: fmt.Sprintf("%d action(s) failed: %s", len(report.Failures), strings.Join(report.Failures, "; ")),
			Code:    clientcmd.ExitUnavailable,
		}
	}
	if opts.apply && opts.branch != "" {
		// A land asked for one thing, and a script after a merge must not have to read output to learn it
		// did not happen.
		found := false
		for _, branch := range report.Branches {
			found = true
			if branch.Action != actionDeleted {
				return &clientcmd.CodedError{
					Message: fmt.Sprintf("branch %s stays: %s", branch.Name, branch.Reason),
					Code:    clientcmd.ExitUnavailable,
				}
			}
		}
		if !found {
			return &clientcmd.CodedError{
				Message: fmt.Sprintf("no repository in this scope has a branch %s", opts.branch),
				Code:    clientcmd.ExitUnavailable,
			}
		}
	}
	return nil
}

func printClean(w io.Writer, report CleanReport) {
	word := func(done bool, planned, did string) string {
		if done {
			return did
		}
		return planned
	}
	var decisions []string
	for _, tree := range report.Worktrees {
		switch tree.Action {
		case actionRemove, actionRemoved:
			if tree.PreserveAs != "" {
				fmt.Fprintf(w, "%s commit %s as branch %s\n", word(tree.Action == actionRemoved, "would preserve", "preserved"), tree.Head, tree.PreserveAs)
			}
			fmt.Fprintf(w, "%s %s (%s)\n", word(tree.Action == actionRemoved, "would remove", "removed"), tree.Path, byteText(tree.SizeBytes))
		case actionDecide:
			decisions = append(decisions, fmt.Sprintf("decide %s: %s", tree.Path, strings.Join(tree.Reasons, "; ")))
		}
	}
	for _, cache := range report.Caches {
		if report.Applied && !cache.Dropped {
			continue
		}
		label := "would drop cache"
		if cache.Dropped {
			label = "dropped cache"
		}
		fmt.Fprintf(w, "%s %s (%s)\n", label, cache.Path, byteText(cache.Bytes))
	}
	for _, branch := range report.Branches {
		switch branch.Action {
		case actionDelete, actionDeleted:
			fmt.Fprintf(w, "%s %s %s %s\n", word(branch.Action == actionDeleted, "would delete branch", "deleted branch"), branch.Repo, branch.Name, branch.Subject)
		case actionDecide:
			decisions = append(decisions, fmt.Sprintf("decide branch %s %s: %s, %s %s", branch.Repo, branch.Name, branch.Reason, branch.Date, branch.Subject))
		}
	}
	for _, decision := range decisions {
		fmt.Fprintln(w, decision)
	}
	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
	for _, failure := range report.Failures {
		fmt.Fprintf(w, "FAILED %s\n", failure)
	}
	tail := "dry run, nothing changed; pass --apply to act"
	if report.Applied {
		tail = "applied"
	}
	amount := "to reclaim"
	if report.Applied {
		amount = "reclaimed"
	}
	fmt.Fprintf(w, "%d decision(s), %s %s: %s\n", len(decisions), byteText(report.Reclaim), amount, tail)
}
