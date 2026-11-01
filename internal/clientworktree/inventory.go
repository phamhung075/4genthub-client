package clientworktree

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
)

// This file is the READ half of worktree management: what `worktree status` prints and what
// `worktree clean` decides from. It is one reading on purpose, so a clean can never act on a state the
// status view would have described differently.
//
// WHAT IT READS IS WHAT IS ON DISK, walked, not the `git worktree list` of one checkout. Measured
// 2026-11-01 on this machine: `rooms/aa2-rs/wt/<seat>` is a hub worktree holding an inner project
// worktree, sometimes with `scratch/<other-worktree>` inside that; one room held worktrees of three
// repositories; `wt/lead-BoneSolverStability` was a plain directory no git knew about; and
// `wt/*/agenthub_client.obsolete` carried 300+ modified files. No single repository's list names those,
// and the walk is what finds a nested worktree in a git-ignored folder - `git worktree remove` on the
// outer one would delete that folder without a word, because git never looks inside what it ignores.
//
// EVERY GIT READ HERE RUNS WITH GIT_OPTIONAL_LOCKS=0. A plain `git status` refreshes the index and takes
// its lock, and this view runs against worktrees live seats are committing in.

// The kinds of thing found under a room's wt/. Only KindLinked may ever be removed.
const (
	KindLinked    = "worktree"  // a linked git worktree
	KindClone     = "clone"     // a repository of its own (`.git` is a directory)
	KindSubmodule = "submodule" // a submodule's own clone (`.git` file, and not a linked worktree)
	KindBroken    = "broken"    // has a `.git` git cannot follow
	KindPlain     = "plain"     // no `.git` at all
)

const cacheDirSignature = "Signature: 8a477f597d28d172789f06886806bc55"

// Cache is one rebuildable directory: a `target` with a valid CACHEDIR.TAG, or a `node_modules`.
type Cache struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// Tree is one thing found under a room's wt/, with every fact the status view prints.
type Tree struct {
	Room        string   `json:"room"`
	Path        string   `json:"path"`
	Kind        string   `json:"kind"`
	Repo        string   `json:"repo,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	Detached    bool     `json:"detached,omitempty"`
	Head        string   `json:"head,omitempty"`
	Base        string   `json:"base,omitempty"`
	Merged      bool     `json:"merged"`
	Ahead       int      `json:"ahead"`
	Reachable   bool     `json:"reachable"`
	Uncommitted int      `json:"uncommitted"`
	Untracked   int      `json:"untracked"`
	Ignored     []string `json:"ignored,omitempty"`
	SizeBytes   int64    `json:"size_bytes"`
	Caches      []Cache  `json:"caches,omitempty"`
	LastCommit  string   `json:"last_commit,omitempty"`
	Seat        string   `json:"seat,omitempty"`
	SeatLive    bool     `json:"seat_live"`
	BusyPIDs    []int    `json:"busy_pids,omitempty"`
	Locked      bool     `json:"locked,omitempty"`
	Obsolete    bool     `json:"obsolete,omitempty"`
	Parent      string   `json:"parent,omitempty"`
	OutOfScope  bool     `json:"out_of_scope,omitempty"`
	// What `worktree clean` decided; empty in the status view.
	Action     string   `json:"action,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
	PreserveAs string   `json:"preserve_as,omitempty"`
}

// Branch is one local branch of a repository.
type Branch struct {
	Repo     string `json:"repo"`
	Name     string `json:"name"`
	Tip      string `json:"tip"`
	Merged   bool   `json:"merged"`
	Ahead    int    `json:"ahead"`
	Date     string `json:"date"`
	Subject  string `json:"subject"`
	Worktree string `json:"worktree,omitempty"`
	// What `worktree clean` decided; empty in the status view.
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ForeignEntry is a worktree a repository registers outside every room's wt/. It is listed so nobody is
// surprised by a branch it holds, and it is never cleaned: the same rule `worktree prune` keeps.
type ForeignEntry struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

// Repo is one repository the scope reached.
type Repo struct {
	Name     string         `json:"name"`
	Main     string         `json:"main"`
	Base     string         `json:"base,omitempty"`
	Stashes  int            `json:"stashes"`
	Prunable int            `json:"prunable"`
	Foreign  []ForeignEntry `json:"foreign,omitempty"`
	Branches []Branch       `json:"branches"`

	commonDir string
	entries   []Entry
}

// node is a Tree plus what only the walk knows: who it is inside, what is inside it.
type node struct {
	Tree
	parent     *node
	children   []*node
	repo       *Repo
	gitDir     string
	unreadable string
	envLink    bool
	hasModules bool
	done       bool // removed from disk by this run
	wtRoot     string
	live       *roomLive
}

// scanOptions is what a reading is over.
type scanOptions struct {
	root   string
	rooms  []string
	all    bool
	main   string
	base   string
	noSize bool
}

type inventory struct {
	opts      scanOptions
	nodes     []*node
	repos     map[string]*Repo
	repoOrder []string
	allRooms  []string
	notes     []string
	onlyRepo  string
}

// gitEnv is the environment of every git call in this file. The three variables a git hook hands its
// children would point the call at the hook's repository instead of the directory it was asked about.
func gitEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_INDEX_FILE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// gitRead runs a read in dir and answers stdout alone, untrimmed but for the final newline.
func gitRead(dir string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(gitEnv(), "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("git %s (in %s): %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// gitWrite runs a write in dir, in git's own words on failure. It never carries --force anywhere: every
// caller passes its arguments literally, and a test greps this file's package for the flag.
func gitWrite(dir string, args ...string) error {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = gitEnv()
	raw, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(raw)))
	}
	return nil
}

// runtimeRoomStatus and processCwds are the two seams the liveness reading runs through, in the repo's
// style of a package-level variable: a case cannot start a runtime daemon, and it must not read this
// machine's real seats.
var runtimeRoomStatus = func(room string) ([]clientruntime.SeatStatus, error) {
	return clientruntime.NewClient("").GetRoomStatus(room)
}

type processCwd struct {
	pid int
	cwd string
}

var processCwds = func() []processCwd {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []processCwd
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd")); err == nil {
			out = append(out, processCwd{pid: pid, cwd: cwd})
		}
	}
	return out
}

// roomLive is one room's seats and which of them are up.
type roomLive struct {
	seats   []string
	live    map[string]bool
	cwds    map[string]string
	unknown string // why this room's liveness could not be read; everything in it is then refused
}

// readRoomLive unions every source of "this seat runs" and takes the seat as live when ANY says so: the
// runtime's own answer, and a state file whose pid is up. A stale state file only costs a refusal; a missed
// live seat costs that seat's working tree. A seat's own socket is deliberately not dialled: this view
// runs beside live rooms and has no business connecting to a seat's runner. Whatever a seat's records miss,
// busyPIDs still reads from the seat's process itself.
func readRoomLive(root, room, roomDir string) *roomLive {
	state := &roomLive{live: map[string]bool{}, cwds: map[string]string{}}
	known := map[string]bool{}
	if spec, err := clientteam.LoadLiveRoomSpec(root, room); err == nil {
		for _, seat := range spec.Seats {
			known[seat.SeatKey] = true
			if seat.Cwd != nil {
				state.cwds[seat.SeatKey] = *seat.Cwd
			}
		}
	}
	if statuses, err := runtimeRoomStatus(room); err == nil {
		for _, seat := range statuses {
			known[seat.Seat] = true
			if strings.EqualFold(strings.TrimSpace(seat.State), "running") {
				state.live[seat.Seat] = true
			}
		}
	}
	run := filepath.Join(roomDir, "run")
	entries, err := os.ReadDir(run)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		state.unknown = fmt.Sprintf("cannot read %s: %v", run, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(run, name)
		switch {
		case strings.HasSuffix(name, ".state.json"):
			seat := strings.TrimSuffix(name, ".state.json")
			known[seat] = true
			var record struct {
				PID int `json:"pid"`
			}
			if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &record) == nil && pidAlive(record.PID) {
				state.live[seat] = true
			}
		case strings.HasSuffix(name, ".sock"):
			known[strings.TrimSuffix(name, ".sock")] = true
		}
	}
	for seat := range known {
		state.seats = append(state.seats, seat)
	}
	sort.Slice(state.seats, func(i, j int) bool { return len(state.seats[i]) > len(state.seats[j]) })
	return state
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ownerSeat is the seat a path under wt/ belongs to: `<seat>`, a lane `<seat>-<name>`, and the
// integration worktree is the lead's.
func (l *roomLive) ownerSeat(first string) string {
	for _, seat := range l.seats {
		if first == seat || strings.HasPrefix(first, seat+"-") {
			return seat
		}
	}
	if first == clientservices.Integration {
		return "lead"
	}
	return ""
}

// listRooms names the rooms under the store, sorted.
func listRooms(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "rooms"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var rooms []string
	for _, entry := range entries {
		if entry.IsDir() {
			rooms = append(rooms, entry.Name())
		}
	}
	sort.Strings(rooms)
	return rooms, nil
}

// scan is the whole reading: walk the rooms in scope, resolve every git thing found, read each repository
// once, and then inspect every node.
func scan(opts scanOptions) (*inventory, error) {
	inv := &inventory{opts: opts, repos: map[string]*Repo{}}
	rooms, err := listRooms(opts.root)
	if err != nil {
		return nil, err
	}
	inv.allRooms = rooms
	scope := opts.rooms
	if opts.all {
		scope = rooms
	}
	if opts.main != "" {
		repo, err := inv.repoOf(opts.main)
		if err != nil {
			return nil, err
		}
		inv.onlyRepo = repo.commonDir
		// The rooms holding one of this repository's worktrees are read too, so a worktree nested in
		// another repository's is still seen; only this repository's are then acted on.
		seen := map[string]bool{}
		for _, room := range scope {
			seen[room] = true
		}
		for _, entry := range repo.entries {
			if room := roomOfPath(opts.root, entry.Path); room != "" && !seen[room] && len(opts.rooms) == 0 {
				seen[room] = true
				scope = append(scope, room)
			}
		}
		sort.Strings(scope)
	}
	for _, room := range scope {
		if err := inv.walkRoom(room); err != nil {
			return nil, err
		}
	}
	procs := processCwds()
	for _, n := range inv.nodes {
		inv.inspect(n, procs)
	}
	inv.nameForeign()
	return inv, nil
}

// roomOfPath answers the room a path sits in the wt/ of, or "".
func roomOfPath(root, path string) string {
	rel, err := filepath.Rel(canonicalPath(filepath.Join(root, "rooms")), canonicalPath(path))
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 3 || parts[1] != "wt" {
		return ""
	}
	return parts[0]
}

func (inv *inventory) walkRoom(room string) error {
	roomDir, err := RoomDir(inv.opts.root, room)
	if err != nil {
		return err
	}
	wt := filepath.Join(roomDir, "wt")
	entries, err := os.ReadDir(wt)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cannot read %s: %v", wt, err)
	}
	live := readRoomLive(inv.opts.root, room, roomDir)
	if live.unknown != "" {
		inv.notes = append(inv.notes, fmt.Sprintf("room %s: %s", room, live.unknown))
	}
	for _, entry := range entries {
		if !entry.IsDir() { // a symlink under wt/ is not followed, and a loose file is nobody's worktree
			continue
		}
		seat := live.ownerSeat(entry.Name())
		inv.visit(filepath.Join(wt, entry.Name()), nil, room, seat, live, wt)
	}
	return nil
}

// visit walks one directory and charges what it finds to the innermost thing that holds it, so a size is
// never counted twice and a nested worktree is always known to its parent.
func (inv *inventory) visit(dir string, owner *node, room, seat string, live *roomLive, wtRoot string) {
	_, gitErr := os.Lstat(filepath.Join(dir, ".git"))
	if gitErr == nil || owner == nil {
		n := &node{Tree: Tree{Room: room, Path: dir, Seat: seat, Kind: KindPlain}, parent: owner, wtRoot: wtRoot, live: live}
		if gitErr == nil {
			n.Kind = KindBroken // until inspect resolves it
		}
		rel, _ := filepath.Rel(wtRoot, dir)
		n.Obsolete = strings.Contains(rel, ".obsolete") || strings.Contains(room, ".obsolete")
		if owner != nil {
			n.Parent = owner.Path
			owner.children = append(owner.children, n)
		}
		inv.nodes = append(inv.nodes, n)
		owner = n
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		owner.unreadable = err.Error()
		return
	}
	for _, entry := range entries {
		name, path := entry.Name(), filepath.Join(dir, entry.Name())
		if name == ".git" {
			continue
		}
		if !entry.IsDir() {
			owner.SizeBytes += inv.fileBytes(path)
			continue
		}
		if isCacheDir(path, name) {
			size := inv.treeBytes(path)
			owner.SizeBytes += size
			owner.Caches = append(owner.Caches, Cache{Path: path, Bytes: size})
			continue
		}
		inv.visit(path, owner, room, seat, live, wtRoot)
	}
}

// isCacheDir is the ONE definition of a rebuildable directory. A `target` without a valid CACHEDIR.TAG is
// not one - such a folder was measured on this machine and nobody could say what it held - and a symlink
// is never one, because the walk only calls this for real directories.
func isCacheDir(path, name string) bool {
	switch name {
	case "node_modules":
		return true
	case "target":
		raw, err := os.ReadFile(filepath.Join(path, "CACHEDIR.TAG"))
		return err == nil && bytes.HasPrefix(raw, []byte(cacheDirSignature))
	}
	return false
}

func (inv *inventory) fileBytes(path string) int64 {
	if inv.opts.noSize {
		return 0
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Blocks * 512
	}
	return info.Size()
}

func (inv *inventory) treeBytes(path string) int64 {
	if inv.opts.noSize {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			total += inv.fileBytes(p)
		}
		return nil
	})
	return total
}

// repoOf reads a repository once, from any directory inside it, keyed by git's common directory.
func (inv *inventory) repoOf(dir string) (*Repo, error) {
	raw, err := gitRead(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	common := canonicalPath(raw)
	if repo, ok := inv.repos[common]; ok {
		return repo, nil
	}
	entries, err := Read(dir)
	if err != nil {
		return nil, err
	}
	repo := &Repo{commonDir: common, entries: entries, Branches: []Branch{}}
	if len(entries) > 0 {
		repo.Main = entries[0].Path
		repo.Name = filepath.Base(entries[0].Path)
	}
	for _, entry := range entries {
		if entry.Prunable {
			repo.Prunable++
		}
	}
	repo.Base = resolveBase(common, inv.opts.base)
	if repo.Base == "" {
		inv.notes = append(inv.notes, fmt.Sprintf("repo %s: no base branch found, so nothing in it reads as merged", repo.Name))
	}
	if out, err := gitRead(dir, "--git-dir="+common, "rev-list", "--walk-reflogs", "--count", "refs/stash"); err == nil {
		repo.Stashes, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	repo.Branches = readBranches(common, repo)
	inv.repos[common] = repo
	inv.repoOrder = append(inv.repoOrder, common)
	return repo, nil
}

// resolveBase picks the ref "merged" is measured against: the branch the caller named, else origin's
// HEAD, else main, else master. Of a local branch and its origin copy it takes the one further along,
// because a checkout may land locally first (aa2-rs) or fetch ahead of a local main that nobody moved
// (this client's own repo). Two diverged tips read as the local one, the conservative answer.
func resolveBase(common, flagged string) string {
	gitDirArg := "--git-dir=" + common
	has := func(ref string) bool {
		_, err := gitRead(common, gitDirArg, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		return err == nil
	}
	name := flagged
	if name == "" {
		if out, err := gitRead(common, gitDirArg, "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"); err == nil {
			name = strings.TrimPrefix(strings.TrimSpace(out), "origin/")
		}
		if name == "" || !(has("refs/heads/"+name) || has("refs/remotes/origin/"+name)) {
			name = "main"
			if !has("refs/heads/main") && !has("refs/remotes/origin/main") {
				name = "master"
			}
		}
	}
	local, remote := "refs/heads/"+name, "refs/remotes/origin/"+name
	switch {
	case has(local) && has(remote):
		if _, err := gitRead(common, gitDirArg, "merge-base", "--is-ancestor", local, remote); err == nil {
			return "origin/" + name
		}
		return name
	case has(local):
		return name
	case has(remote):
		return "origin/" + name
	}
	return ""
}

// readBranches is every local branch with the facts the view prints.
func readBranches(common string, repo *Repo) []Branch {
	gitDirArg := "--git-dir=" + common
	out, err := gitRead(common, gitDirArg, "for-each-ref",
		"--format=%(refname:short)%00%(objectname)%00%(committerdate:short)%00%(subject)%00%(worktreepath)", "refs/heads")
	if err != nil || out == "" {
		return []Branch{}
	}
	branches := []Branch{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\x00", 5)
		if len(fields) != 5 {
			continue
		}
		branch := Branch{Repo: repo.Name, Name: fields[0], Tip: fields[1], Date: fields[2], Subject: fields[3], Worktree: fields[4]}
		if repo.Base != "" {
			if count, err := gitRead(common, gitDirArg, "rev-list", "--count", repo.Base+".."+branch.Tip); err == nil {
				branch.Ahead, _ = strconv.Atoi(strings.TrimSpace(count))
				branch.Merged = branch.Ahead == 0
			}
		}
		branches = append(branches, branch)
	}
	return branches
}

// nameForeign records, per repository, the worktrees it registers outside every room's wt/.
func (inv *inventory) nameForeign() {
	for _, common := range inv.repoOrder {
		repo := inv.repos[common]
		for i, entry := range repo.entries {
			if i == 0 || entry.Prunable || roomOfPath(inv.opts.root, entry.Path) != "" {
				continue
			}
			repo.Foreign = append(repo.Foreign, ForeignEntry{Path: entry.Path, Branch: entry.Branch})
		}
	}
}

// inspect fills one node's git facts, and is what a clean re-runs right before it acts.
func (inv *inventory) inspect(n *node, procs []processCwd) {
	n.BusyPIDs = busyPIDs(n.Path, procs)
	n.SeatLive = n.live.live[n.Seat]
	for seat, cwd := range n.live.cwds {
		if n.live.live[seat] && isWithin(cwd, n.Path) {
			n.Seat, n.SeatLive = seat, true
		}
	}
	if n.Kind == KindPlain {
		return
	}
	n.Kind = KindBroken
	gitPath := filepath.Join(n.Path, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return
	}
	top, err := gitRead(n.Path, "rev-parse", "--show-toplevel")
	if err != nil || canonicalPath(top) != canonicalPath(n.Path) {
		return
	}
	dirs, err := gitRead(n.Path, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return
	}
	parts := strings.Split(dirs, "\n")
	if len(parts) != 2 {
		return
	}
	n.gitDir = canonicalPath(parts[0])
	switch {
	case n.gitDir != canonicalPath(parts[1]):
		n.Kind = KindLinked
	case info.IsDir():
		n.Kind = KindClone
	default:
		n.Kind = KindSubmodule
	}
	repo, err := inv.repoOf(n.Path)
	if err != nil {
		n.Kind = KindBroken
		return
	}
	n.repo, n.Repo, n.Base = repo, repo.Name, repo.Base
	if inv.onlyRepo != "" && repo.commonDir != inv.onlyRepo {
		n.OutOfScope = true
	}
	n.Locked = false
	for _, entry := range repo.entries {
		if canonicalPath(entry.Path) == canonicalPath(n.Path) {
			n.Locked = entry.Locked
		}
	}
	if modules, err := os.Stat(filepath.Join(n.gitDir, "modules")); err == nil && modules.IsDir() {
		n.hasModules = true
	}
	head, err := gitRead(n.Path, "log", "-1", "--format=%H%x00%cs")
	if err != nil {
		n.Kind = KindBroken
		return
	}
	fields := strings.SplitN(head, "\x00", 2)
	n.Head, n.LastCommit = fields[0], fields[len(fields)-1]
	n.Branch, n.Detached = "", false
	if branch, err := gitRead(n.Path, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		n.Branch = strings.TrimSpace(branch)
	} else {
		n.Detached = true
	}
	n.Merged, n.Ahead = false, 0
	if repo.Base != "" {
		if count, err := gitRead(n.Path, "rev-list", "--count", repo.Base+".."+n.Head); err == nil {
			n.Ahead, _ = strconv.Atoi(strings.TrimSpace(count))
			n.Merged = n.Ahead == 0
		}
	}
	n.Reachable = false
	if out, err := gitRead(n.Path, "for-each-ref", "--count=1", "--contains", n.Head, "--format=%(refname)",
		"refs/heads", "refs/remotes", "refs/tags"); err == nil && strings.TrimSpace(out) != "" {
		n.Reachable = true
	}
	inv.readChanges(n)
	inv.filterCaches(n)
	n.Ignored = ignoredResidue(n)
}

// busyPIDs names every process whose working directory is in path: a seat's process, a shell, or an
// offload job in that tree, whichever the room's own records know about it or not.
func busyPIDs(path string, procs []processCwd) []int {
	var pids []int
	for _, proc := range procs {
		if isWithin(proc.cwd, path) {
			pids = append(pids, proc.pid)
		}
	}
	return pids
}

// isWithin answers whether path is dir or below it, comparing resolved paths.
func isWithin(path, dir string) bool {
	path, dir = canonicalPath(path), canonicalPath(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// descendants is every node inside n, at any depth.
func (n *node) descendants() []*node {
	var out []*node
	for _, child := range n.children {
		out = append(out, child)
		out = append(out, child.descendants()...)
	}
	return out
}

// readChanges counts what `git status` says about this worktree alone. A line on a nested worktree's own
// path is that worktree's state, judged when it is inspected: the outer gitlink reads modified whenever
// the inner link sits on another commit, which was every seat's client link on this machine. A STAGED
// change on that path is the outer worktree's own, and it counts.
func (inv *inventory) readChanges(n *node) {
	n.Uncommitted, n.Untracked, n.envLink = 0, 0, false
	nested := map[string]bool{}
	for _, child := range n.descendants() {
		if rel, err := filepath.Rel(n.Path, child.Path); err == nil {
			nested[rel] = true
		}
	}
	out, err := gitRead(n.Path, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		n.unreadable = err.Error()
		return
	}
	for _, line := range strings.Split(out, "\x00") {
		if len(line) < 4 {
			continue
		}
		code, path := line[:2], strings.TrimSuffix(line[3:], "/")
		if nested[path] && code[0] == ' ' || nested[path] && code == "??" {
			continue
		}
		if code == "??" {
			if path == ".env" && isSymlink(filepath.Join(n.Path, path)) {
				n.envLink = true // this client's own link (LinkProviderKey)
				continue
			}
			n.Untracked++
			continue
		}
		n.Uncommitted++
	}
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// filterCaches keeps only the caches git agrees are disposable: nothing tracked in it, and ignored. A
// `node_modules` somebody committed is source, whatever its name says.
func (inv *inventory) filterCaches(n *node) {
	kept := n.Caches[:0]
	for _, cache := range n.Caches {
		rel, err := filepath.Rel(n.Path, cache.Path)
		if err != nil {
			continue
		}
		tracked, err := gitRead(n.Path, "ls-files", "--", rel)
		if err != nil || tracked != "" {
			continue
		}
		if _, err := gitRead(n.Path, "check-ignore", "-q", rel+"/"); err != nil {
			continue
		}
		kept = append(kept, cache)
	}
	n.Caches = kept
}

// ignoredResidue names the git-ignored content `git worktree remove` would delete without a word, minus
// what is known to come back by itself: the caches, this client's `.env` link and the code graph. Without
// this reading an untagged `target` would go with its worktree, which is the case the tag rule exists for.
func ignoredResidue(n *node) []string {
	out, err := gitRead(n.Path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return []string{"(the ignored files could not be read)"}
	}
	skip := map[string]bool{}
	for _, cache := range n.Caches {
		skip[cache.Path] = true
	}
	for _, child := range n.descendants() {
		skip[child.Path] = true
	}
	var residue []string
	for _, entry := range strings.Split(out, "\x00") {
		rel := strings.TrimSuffix(entry, "/")
		if rel == "" || rel == "graphify-out" {
			continue
		}
		path := filepath.Join(n.Path, rel)
		if rel == ".env" && isSymlink(path) {
			continue
		}
		if holdsFiles(path, skip) {
			residue = append(residue, rel)
		}
	}
	sort.Strings(residue)
	return residue
}

// holdsFiles answers whether path holds any file outside the skipped subtrees; a folder of nothing but
// skipped subtrees and empty directories leaves nothing behind for anyone to lose.
func holdsFiles(path string, skip map[string]bool) bool {
	if skip[path] {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if holdsFiles(filepath.Join(path, entry.Name()), skip) {
			return true
		}
	}
	return false
}

// byteText is a size the way `du -h` prints one.
func byteText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value, suffix := float64(n), "KMGT"
	index := -1
	for value >= unit && index < len(suffix)-1 {
		value /= unit
		index++
	}
	return fmt.Sprintf("%.1f%c", value, suffix[index])
}
