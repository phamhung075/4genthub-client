// The overlap detector of `rigd` (ruling section 4.5 of
// ai_docs/operations/client-features-for-the-dev-team.md, plan item 4). Every ten minutes, and on request, it
// compares every seat branch against the room's base and reports two things.
//
//  1. TWO SEATS CHANGING THE SAME FILE. For each seat the doctor shows in its own worktree it reads the files
//     changed on that branch since the base PLUS the files changed but not yet committed, and a file two seats
//     have in common is a hit. The report goes to both seats and to the lead, and NOTHING IS BLOCKED: each
//     author keeps the change on its own branch and the lead decides the merge order.
//  2. A STALE SEAT BRANCH: unmerged commits, and no commit for the stale window. The same scan sees each
//     branch's last commit time, which is why the ruling folds this in here instead of giving it its own
//     feature.
//
// GIT FIRST, and that is all of it: the ruling's method 1 needs nothing from the seat and works for all four
// runtimes. Reading a session log would warn earlier, but only for omp and claude-code, and a pre-edit hook is
// NOT built, because a false alarm there would block work.
//
// THE BASE IS THE ROOM'S, NEVER FIXED TO `main` (4.5): during a batch the lead merges into a batch branch, and
// which line is the main line is not settled. The room's team file may name it in `room.base_branch`;
// `--base-branch` overrides it for one run; `main` is the fallback.
//
// WHAT IT CANNOT SEE, IT NAMES. A seat whose own worktree the doctor does not show is SKIPPED and named: its
// changes cannot be told apart from anyone else's. A reviewer is skipped and named too, because its worktree is
// detached and it has no branch to compare. Every read that fails produces a note, never an empty answer.
//
// AND IT DOES NOT REPEAT ITSELF: a hit is sent once, and the same file with the same seats is not reported
// again inside the repeat window unless the set of seats changes - a third seat joining is news. The memory
// is a file under the state root, so a restarted rigd does not start the window over.
package clientlifecycle

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
	"github.com/phamhung075/4genthub-client/internal/clientworktree"
)

const (
	overlapUsage = "usage: 4genteam overlap RIG [--once|--every DURATION] [--base-branch BRANCH] " +
		"[--stale-after DURATION] [--state-root DIR] [--json] [--dry-run]"

	// overlapEvery is how often rigd runs the scan (4.5: every ten minutes). It is a timer inside rigd's own
	// loop rather than a fourth child, because 7.1's reason for keeping supervision in one process - a second
	// always-on process is a second thing to supervise - applies to this as much as to a keeper.
	overlapEvery = 10 * time.Minute
	// overlapRepeat is how long one report stays quiet: the same file and the same seats are not reported
	// again inside it unless the set changes (4.5: an hour, unless a third seat joins).
	overlapRepeat = time.Hour
	// overlapStaleAfter is the N of "untouched for N hours with unmerged commits". The ruling names the
	// condition and not the number; four hours is chosen here and `--stale-after` changes it for one run.
	overlapStaleAfter = 4 * time.Hour
	// overlapDefaultBase is the base when neither the room nor the flag names one.
	overlapDefaultBase = "main"
	// overlapLeadSeat is the seat key the team file uses for the lead (`services.go` reads the same literal
	// when it decides which worktree the room has an integration one for).
	overlapLeadSeat = "lead"
)

// ---- what one scan reads, and from where -----------------------------------

// overlapDeps is what a scan touches outside its arguments, as fields so a case drives the whole detector
// without a room, a worktree, a terminal or a clock.
type overlapDeps struct {
	teamDir  func(rig string) (string, error)
	seats    func(dir string) ([]clientservices.SeatType, error)
	baseName func(dir string) string
	roomDir  func(rig string) (string, error)
	mainRoot func(string) (string, error)
	check    func(mainRoot, roomDir, rig string, seats []clientservices.SeatType) (clientworktree.CheckVerdict, error)
	git      func(dir string, args ...string) (string, error)
	say      func(rig, seat, text string)
	now      func() time.Time
	state    func(rig string) (string, error)
}

// overlapDepsFor is the scan's production wiring, and a seam so a case drives rigd's pass without a room.
var overlapDepsFor = defaultOverlapDeps

func defaultOverlapDeps(stateRoot string) *overlapDeps {
	return &overlapDeps{
		teamDir:  func(rig string) (string, error) { return clientservices.TeamDir("", rig) },
		seats:    clientservices.TeamSeats,
		baseName: overlapTeamBaseBranch,
		roomDir:  clientworktree.RoomDir,
		mainRoot: clientworktree.MainRoot,
		check:    clientworktree.Check,
		git:      overlapGit,
		// A plain message, so a seat sees the From/To envelope and knows the alert is not a colleague's note;
		// rigSay starts it and returns without waiting, so one seat that is mid-turn cannot hold up the scan.
		say:   func(rig, seat, text string) { rigSay(rig, seat, text, false) },
		now:   clientservices.Now,
		state: func(rig string) (string, error) { return filepath.Join(stateRoot, "overlap-"+rig+".json"), nil },
	}
}

// overlapGit is one git read in one worktree. `-C` rather than a chdir: the scan runs in rigd, which serves
// every room at once, and a process-wide working directory is state no caller of this should have to know.
func overlapGit(dir string, args ...string) (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git is not on PATH")
	}
	argv := append([]string{"-C", dir}, args...)
	done, err := exec.Command(path, argv...).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			reason := strings.TrimSpace(string(exit.Stderr))
			if reason != "" {
				return "", fmt.Errorf("%v: %s", err, overlapClip(reason, 200))
			}
		}
		return "", err
	}
	return strings.TrimSpace(string(done)), nil
}

// overlapClip shortens a git message for a note, and says so when it did, so a truncated reason is never read
// as the whole one.
func overlapClip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// overlapTeamBaseBranch reads the branch the room says the lead merges into. A room that names none, or a
// team file this cannot read, answers with nothing rather than a guess: the caller then uses the default, and
// an unreadable team file is already named by the rows that read the same file. It is the SAME reader
// `clientservices.TeamSeats` uses, so the detector cannot disagree with the rest of the client about team.json.
func overlapTeamBaseBranch(dir string) string {
	return clientteam.RoomBaseBranch(dir)
}

// ---- one scan ---------------------------------------------------------------

// overlapOptions is one run's inputs.
type overlapOptions struct {
	Rig        string
	BaseBranch string
	StaleAfter time.Duration
	Repeat     time.Duration
	Now        time.Time
}

// overlapFacts is what the scan knows about one seat's branch.
type overlapFacts struct {
	Seat     string    `json:"seat"`
	Branch   string    `json:"branch"`
	Worktree string    `json:"worktree"`
	Files    []string  `json:"files"`
	Unmerged int       `json:"unmerged"`
	Last     time.Time `json:"last"`
}

// overlapHit is one file two or more seats have changed.
type overlapHit struct {
	File  string         `json:"file"`
	Seats []overlapFacts `json:"seats"`
}

// overlapReport is what one scan found, and what it sent.
type overlapReport struct {
	Rig     string         `json:"rig"`
	Base    string         `json:"base"`
	StaleAt time.Duration  `json:"staleAfter"`
	Hits    []overlapHit   `json:"hits"`
	Stale   []overlapFacts `json:"stale"`
	Skipped []string       `json:"skipped"`
	Notes   []string       `json:"notObserved"`
	Sent    []string       `json:"sent"`
}

// overlapScanFor is the scan as a seam: rigd's timer, the `overlap` verb and every case go through it.
var overlapScanFor = overlapScan

// overlapScan reads every seat's branch and answers what overlaps and what has gone stale. Every step that
// cannot read its input produces a note rather than a state: a detector that could not look has no answer to
// give, and an empty report must not read as "nothing overlaps".
func overlapScan(deps *overlapDeps, opts overlapOptions) overlapReport {
	report := overlapReport{Rig: opts.Rig, StaleAt: opts.StaleAfter}
	dir, err := deps.teamDir(opts.Rig)
	if err != nil {
		report.Notes = append(report.Notes, fmt.Sprintf("%s: its team directory cannot be resolved: %v", opts.Rig, err))
		return report
	}
	seats, err := deps.seats(dir)
	if err != nil {
		report.Notes = append(report.Notes, fmt.Sprintf("%s: its seats cannot be read: %v", opts.Rig, err))
		return report
	}
	base := strings.TrimSpace(opts.BaseBranch)
	if base == "" {
		base = strings.TrimSpace(deps.baseName(dir))
	}
	if base == "" {
		base = overlapDefaultBase
	}
	report.Base = base
	roomDir, err := deps.roomDir(opts.Rig)
	if err != nil {
		report.Notes = append(report.Notes, fmt.Sprintf("%s: its room directory cannot be resolved: %v", opts.Rig, err))
		return report
	}
	mainRoot, err := deps.mainRoot("")
	if err != nil {
		report.Notes = append(report.Notes, fmt.Sprintf("%s: its checkout cannot be resolved: %v", opts.Rig, err))
		return report
	}
	verdict, err := deps.check(mainRoot, roomDir, opts.Rig, seats)
	if err != nil {
		report.Notes = append(report.Notes, fmt.Sprintf("%s: its worktrees cannot be read: %v", opts.Rig, err))
		return report
	}
	// The seats the doctor does not show in their own worktree, by the SAME verdict the worktrees row
	// computes (clientworktree.Check): the detector must not invent a second opinion about where a seat is.
	unusable := map[string]string{}
	for _, path := range verdict.Missing {
		unusable[path] = "its own worktree is not created yet"
	}
	for _, path := range verdict.Wrong {
		unusable[path] = "the path is not the seat worktree this client derives"
	}

	// THE ONE DERIVATION is DerivedWorktrees: the list `up` creates and `doctor` reports, so the detector
	// cannot invent a second path/branch rule. A seat missing from it has no address to scan.
	worktrees := map[string]clientservices.Worktree{}
	for _, derived := range clientservices.DerivedWorktrees(roomDir, opts.Rig, seats) {
		worktrees[derived.SeatKey] = derived
	}

	var facts []overlapFacts
	for _, seat := range seats {
		derived, hasDerived := worktrees[seat.Key]
		if !hasDerived {
			report.Skipped = append(report.Skipped, seat.Key+" (the room derives no worktree for it)")
			continue
		}
		worktree, branch := derived.Path, derived.Branch
		if reason, skip := unusable[worktree]; skip {
			report.Skipped = append(report.Skipped, seat.Key+" ("+reason+")")
			continue
		}
		if branch == "" {
			report.Skipped = append(report.Skipped, seat.Key+" (its worktree is detached: a reviewer never commits)")
			continue
		}
		read, note := overlapSeatFacts(deps, seat.Key, worktree, branch, base)
		if note != "" {
			report.Notes = append(report.Notes, note)
			continue
		}
		facts = append(facts, read)
	}
	sort.Strings(report.Skipped)
	sort.Slice(facts, func(i, j int) bool { return facts[i].Seat < facts[j].Seat })

	byFile := map[string][]int{}
	for index, seat := range facts {
		for _, file := range seat.Files {
			byFile[file] = append(byFile[file], index)
		}
	}
	for file, indexes := range byFile {
		if len(indexes) < 2 {
			continue
		}
		hit := overlapHit{File: file}
		for _, index := range indexes {
			hit.Seats = append(hit.Seats, facts[index])
		}
		sort.Slice(hit.Seats, func(i, j int) bool { return hit.Seats[i].Seat < hit.Seats[j].Seat })
		report.Hits = append(report.Hits, hit)
	}
	sort.Slice(report.Hits, func(i, j int) bool { return report.Hits[i].File < report.Hits[j].File })

	for _, seat := range facts {
		if seat.Unmerged == 0 || opts.Now.Sub(seat.Last) < opts.StaleAfter {
			continue
		}
		report.Stale = append(report.Stale, seat)
	}
	return report
}

// overlapSeatFacts reads one seat's branch: the files it changed since the base, the files it has not
// committed, how many commits are unmerged, and when it last moved. A read that fails answers with a note
// that names the seat and what could not be read.
func overlapSeatFacts(deps *overlapDeps, seat, worktree, branch, base string) (overlapFacts, string) {
	facts := overlapFacts{Seat: seat, Branch: branch, Worktree: worktree}
	if _, err := deps.git(worktree, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return facts, fmt.Sprintf("%s: the base branch %s is not in its worktree, so its changes cannot be "+
			"told apart from anyone else's", seat, base)
	}
	changed, err := deps.git(worktree, "diff", "--name-only", base+"...HEAD")
	if err != nil {
		return facts, fmt.Sprintf("%s: cannot read the files its branch changed: %v", seat, err)
	}
	status, err := deps.git(worktree, "status", "--porcelain")
	if err != nil {
		return facts, fmt.Sprintf("%s: cannot read the files it has not committed: %v", seat, err)
	}
	facts.Files = overlapUnique(append(splitLines(changed), porcelainPaths(status)...))
	count, err := deps.git(worktree, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return facts, fmt.Sprintf("%s: cannot read how many of its commits are unmerged: %v", seat, err)
	}
	facts.Unmerged, _ = strconv.Atoi(count)
	stamp, err := deps.git(worktree, "log", "-1", "--format=%ct", "HEAD")
	if err != nil {
		return facts, fmt.Sprintf("%s: cannot read when its branch last moved: %v", seat, err)
	}
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return facts, fmt.Sprintf("%s: git answered %q where a commit time was expected", seat, stamp)
	}
	facts.Last = time.Unix(seconds, 0).UTC()
	return facts, ""
}

// splitLines is git's line-per-answer output as a list, with the empty answer as an empty list.
func splitLines(out string) []string {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// porcelainPaths is `git status --porcelain` as the paths it names. Two columns of status come first, and a
// rename or copy names two paths, of which the NEW one is the file that exists now.
//
// It does NOT go through splitLines: that trims each line, which eats the space that means "unmodified in the
// index" and shifts every path one character left. That was a real defect - `internal/a.go` read as
// `nternal/a.go`, so the file never matched another seat's - and this comment is why the trim is not re-added.
func porcelainPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if arrow := strings.LastIndex(path, " -> "); arrow >= 0 {
			path = strings.TrimSpace(path[arrow+len(" -> "):])
		}
		path = strings.Trim(path, `"`)
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// overlapUnique is a sorted list without duplicates: two reads can name the same file (committed and then
// changed again), and a file counted twice would look like two seats agreeing.
func overlapUnique(paths []string) []string {
	seen := map[string]bool{}
	var unique []string
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		unique = append(unique, path)
	}
	sort.Strings(unique)
	return unique
}

// ---- what it sends, and remembers ------------------------------------------

// overlapRecord is what one report is remembered as: which seats it named, and when.
type overlapRecord struct {
	Seats []string  `json:"seats"`
	At    time.Time `json:"at"`
}

// overlapMemory is the file under the state root: the files already reported, and the stale branches already
// reported, so a restarted rigd does not start the window over.
type overlapMemory struct {
	Files map[string]overlapRecord `json:"files"`
	Stale map[string]overlapRecord `json:"stale"`
}

func newOverlapMemory() *overlapMemory {
	return &overlapMemory{Files: map[string]overlapRecord{}, Stale: map[string]overlapRecord{}}
}

// overlapNotify sends what the ruling says to send and remembers it. It is separate from the scan so the
// reading and the deciding can be driven apart, which is how the hour-long silence is testable without an
// hour.
func overlapNotify(deps *overlapDeps, opts overlapOptions, report *overlapReport) {
	memory, path := overlapLoad(deps, opts.Rig)
	changed := false
	for _, hit := range report.Hits {
		seats := overlapSeatKeys(hit.Seats)
		record, seen := memory.Files[hit.File]
		if seen && sameSeatSet(record.Seats, seats) && opts.Now.Sub(record.At) < opts.Repeat {
			continue
		}
		text := overlapMessage(hit)
		overlapTell(deps, opts.Rig, seats, text)
		memory.Files[hit.File] = overlapRecord{Seats: seats, At: opts.Now}
		report.Sent = append(report.Sent, "overlap "+hit.File+" -> "+overlapNames(overlapRecipients(seats)))
		changed = true
	}
	for _, seat := range report.Stale {
		record, seen := memory.Stale[seat.Seat]
		if seen && opts.Now.Sub(record.At) < opts.Repeat {
			continue
		}
		text := overlapStaleMessage(seat, opts.StaleAfter)
		overlapTell(deps, opts.Rig, []string{seat.Seat}, text)
		memory.Stale[seat.Seat] = overlapRecord{Seats: []string{seat.Seat}, At: opts.Now}
		report.Sent = append(report.Sent, "stale "+seat.Seat+" -> "+overlapNames(overlapRecipients([]string{seat.Seat})))
		changed = true
	}
	if changed || overlapForget(memory, report) {
		overlapSave(path, memory)
	}
}

// overlapRecipients is who a report is SENT to, in the order the message names them: the seats the report is
// about, and the lead appended unless the lead IS one of them, so the lead reads it once. ONE function, used by
// the sending end and by the dry run, because the plan must not promise a line the real send does not make -
// and by the "sent" record above, which is the same claim about who was told.
func overlapRecipients(seats []string) []string {
	for _, seat := range seats {
		if seat == overlapLeadSeat {
			return append([]string{}, seats...)
		}
	}
	return append(append([]string{}, seats...), overlapLeadSeat)
}

// overlapNames renders recipients the way these messages speak of them: "fe-dev, go-dev and the lead". The lead
// is rendered as "the lead" whether it is appended as the second reader or is itself one of the seats.
func overlapNames(seats []string) string {
	names := make([]string, 0, len(seats))
	for _, seat := range seats {
		if seat == overlapLeadSeat {
			seat = "the " + overlapLeadSeat
		}
		names = append(names, seat)
	}
	if len(names) < 2 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// overlapTell sends one message to the named seats and to the lead, and skips a seat that IS the lead so the
// lead reads it once.
func overlapTell(deps *overlapDeps, rig string, seats []string, text string) {
	for _, seat := range overlapRecipients(seats) {
		deps.say(rig, seat, text)
	}
}

// overlapForget drops records nothing refers to any more, so the file does not grow with every file two seats
// ever touched. It answers whether it dropped anything, because that is also worth writing down.
func overlapForget(memory *overlapMemory, report *overlapReport) bool {
	dropped := false
	live := map[string]bool{}
	for _, hit := range report.Hits {
		live[hit.File] = true
	}
	for file := range memory.Files {
		if !live[file] {
			delete(memory.Files, file)
			dropped = true
		}
	}
	stale := map[string]bool{}
	for _, seat := range report.Stale {
		stale[seat.Seat] = true
	}
	for key := range memory.Stale {
		if !stale[key] {
			delete(memory.Stale, key)
			dropped = true
		}
	}
	return dropped
}

// overlapLoad reads the memory. A file that cannot be read is started over rather than refused: the cost of
// forgetting is one repeated report, and the cost of refusing is a detector that stops detecting.
func overlapLoad(deps *overlapDeps, rig string) (*overlapMemory, string) {
	memory := newOverlapMemory()
	path, err := deps.state(rig)
	if err != nil {
		return memory, ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return memory, path
	}
	if json.Unmarshal(raw, memory) != nil || memory.Files == nil || memory.Stale == nil {
		return newOverlapMemory(), path
	}
	return memory, path
}

// overlapSave writes the memory. A write that fails is not an error the report carries: the scan still found
// what it found, and the worst outcome is a repeated report after a restart.
func overlapSave(path string, memory *overlapMemory) {
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(raw, '\n'), 0o600)
}

// overlapSeatKeys is the seats a hit names, sorted, which is what decides whether a report is news.
func overlapSeatKeys(seats []overlapFacts) []string {
	var keys []string
	for _, seat := range seats {
		keys = append(keys, seat.Seat)
	}
	sort.Strings(keys)
	return keys
}

// sameSeatSet is the ruling's "unless a third seat joins": the same file with the same seats is not news, and
// a different set is.
func sameSeatSet(have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if have[i] != want[i] {
			return false
		}
	}
	return true
}

// overlapMessage is ruling 4.5's message in its own words: the file, both seats with their branch, their
// unmerged count and when they last moved, and the two sentences that say nothing is blocked.
func overlapMessage(hit overlapHit) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Two seats are changing the same file. File: %s.", hit.File)
	for _, seat := range hit.Seats {
		fmt.Fprintf(&out, " Seat %s on branch %s (%d commits not yet merged, last at %s).",
			seat.Seat, seat.Branch, seat.Unmerged, seat.Last.UTC().Format(time.RFC3339))
	}
	out.WriteString(" Nothing is blocked. Each author keeps the change on its own branch; the lead decides " +
		"the merge order. Reply to the lead, not to each other.")
	return out.String()
}

// overlapStaleMessage is the stale-branch alert in the same voice: what was seen, and that nothing is blocked.
func overlapStaleMessage(seat overlapFacts, after time.Duration) string {
	return fmt.Sprintf("A seat branch has unmerged commits and has not moved for %s. Seat %s on branch %s (%d "+
		"commits not yet merged, last at %s). Nothing is blocked. The lead decides whether it is merged, rebased "+
		"or dropped.", after, seat.Seat, seat.Branch, seat.Unmerged, seat.Last.UTC().Format(time.RFC3339))
}

// ---- the verb, and rigd's timer --------------------------------------------

type overlapCommand struct{}

func (overlapCommand) Name() string { return "overlap" }
func (overlapCommand) Summary() string {
	return "report two seats changing one file, and seat branches left with unmerged commits"
}
func (overlapCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, overlapUsage,
			"Reads every seat branch of a room and reports two things: a file two seats have changed (their\n"+
				"branch's files since the base, plus the files not committed yet), and a seat branch with unmerged\n"+
				"commits that has not moved for the stale window. The scan is git's own answer, so it covers all four\n"+
				"runtimes and needs nothing from a seat; a seat whose own worktree the doctor does not show is skipped\n"+
				"and named, because its changes cannot be told apart. Nothing is blocked: both authors keep their\n"+
				"change, and the lead decides the merge order.\n\n"+
				"The base is the room's: team.json may name it in room.base_branch, --base-branch overrides it, and\n"+
				"main is the fallback. One report is sent once; the same file with the same seats stays quiet for an\n"+
				"hour unless the set changes. rigd runs this every "+overlapEvery.String()+" on its own; this verb is\n"+
				"the run on request, one scan by default.")
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("overlap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	every := flags.Duration("every", 0, "run a scan every DURATION until stopped (0 = one scan and exit)")
	base := flags.String("base-branch", "", "the branch to compare against (default: the room's setting, then main)")
	stale := flags.Duration("stale-after", overlapStaleAfter, "how long a seat branch may go untouched with unmerged commits")
	stateRoot := flags.String("state-root", "", "where OpenRig keeps its per-seat state: the flag, else this machine's own")
	asJSON := flags.Bool("json", false, "print one scan's report as JSON (one scan, never a loop)")
	dryRun := flags.Bool("dry-run", false, "print what would be sent and send nothing, and remember nothing")
	if err := flags.Parse(args); err != nil {
		return clientcmd.ExitUsage
	}
	if flags.NArg() < 1 {
		fmt.Fprintf(stderr, "4genteam overlap: RIG is required. %s\n", overlapUsage)
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam overlap: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	rig := flags.Arg(0)
	deps := overlapDepsFor(root)
	opts := overlapOptions{Rig: rig, BaseBranch: *base, StaleAfter: *stale, Repeat: overlapRepeat}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	for {
		opts.Now = deps.now()
		report := overlapScanFor(deps, opts)
		if *dryRun {
			// A dry run reads and says what it WOULD send: it does not notify and does not write the memory,
			// so the safe first run of a detector that messages the whole room cannot message anyone.
			printOverlapPlan(stdout, report)
			if *every <= 0 {
				return clientcmd.ExitOK
			}
		} else {
			overlapNotify(deps, opts, &report)
			if *asJSON {
				printOverlapJSON(stdout, report)
			} else {
				printOverlapReport(stdout, report)
			}
			if *every <= 0 {
				return clientcmd.ExitOK
			}
		}
		select {
		case <-interrupted:
			return clientcmd.ExitOK
		case <-time.After(*every):
		}
	}
}

// printOverlapReport is one scan as the operator reads it: what was sent, what was skipped, and what could not
// be read. The messages are shown as they were sent, so the report and the alert cannot drift apart.
func printOverlapReport(out io.Writer, report overlapReport) {
	fmt.Fprintf(out, "overlap %s: base %s, %d file overlap(s), %d stale seat branch(es)\n",
		report.Rig, report.Base, len(report.Hits), len(report.Stale))
	for _, hit := range report.Hits {
		fmt.Fprintf(out, "  %s\n", overlapMessage(hit))
	}
	for _, seat := range report.Stale {
		fmt.Fprintf(out, "  %s\n", overlapStaleMessage(seat, report.StaleAt))
	}
	if len(report.Skipped) > 0 {
		fmt.Fprintf(out, "  skipped: %s\n", strings.Join(report.Skipped, ", "))
	}
	if len(report.Notes) > 0 {
		fmt.Fprintf(out, "  not observed: %s\n", strings.Join(report.Notes, "; "))
	}
	sent := len(report.Sent)
	fmt.Fprintf(out, "  %d report(s) sent this scan; a report already sent inside the hour is not repeated\n", sent)
}

// printOverlapPlan is a dry run: the messages exactly as they would be sent, marked as not sent, so the first
// run of a detector that messages a whole room is safe to watch.
func printOverlapPlan(out io.Writer, report overlapReport) {
	fmt.Fprintf(out, "overlap %s: base %s, %d file overlap(s), %d stale seat branch(es) - DRY RUN, nothing sent\n",
		report.Rig, report.Base, len(report.Hits), len(report.Stale))
	for _, hit := range report.Hits {
		seats := overlapRecipients(overlapSeatKeys(hit.Seats))
		fmt.Fprintf(out, "  would send to %s: %s\n", overlapNames(seats), overlapMessage(hit))
	}
	for _, seat := range report.Stale {
		recipients := overlapRecipients([]string{seat.Seat})
		fmt.Fprintf(out, "  would send to %s: %s\n", overlapNames(recipients),
			overlapStaleMessage(seat, report.StaleAt))
	}
	if len(report.Skipped) > 0 {
		fmt.Fprintf(out, "  skipped: %s\n", strings.Join(report.Skipped, ", "))
	}
	if len(report.Notes) > 0 {
		fmt.Fprintf(out, "  not observed: %s\n", strings.Join(report.Notes, "; "))
	}
}

// printOverlapJSON is one scan's report as JSON, for a caller that reads rather than watches.
func printOverlapJSON(out io.Writer, report overlapReport) {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(out, "{\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintln(out, string(raw))
}

// overlapRigScan is rigd's own scan of one room: the same function the verb calls, with the production
// wiring, so the operator's run on request and the loop can never do different things.
func overlapRigScan(rig string, now time.Time) overlapReport {
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return overlapReport{Rig: rig, Notes: []string{"the state root cannot be resolved: " + err.Error()}}
	}
	deps := overlapDepsFor(root)
	opts := overlapOptions{Rig: rig, StaleAfter: overlapStaleAfter, Repeat: overlapRepeat, Now: now}
	report := overlapScanFor(deps, opts)
	overlapNotify(deps, opts, &report)
	return report
}

// overlapDue is whether the detector's ten minutes have passed. It is a method so a case can drive the timer
// without waiting ten minutes.
func (m *agentManager) overlapDue(now time.Time) bool {
	if !m.lastOverlap.IsZero() && now.Sub(m.lastOverlap) < overlapEvery {
		return false
	}
	m.lastOverlap = now
	return true
}

// scanOverlaps runs the overlap detector for every room agent supervises, and logs only what it SENT: a pass
// that found nothing is not news, and a log line every ten minutes per room would bury the reports that matter.
func (m *agentManager) scanOverlaps(rigs []string, now time.Time) {
	for _, rig := range rigs {
		report := overlapRigScan(rig, now)
		for _, line := range report.Sent {
			m.log("overlap " + rig + ": " + line)
		}
		for _, note := range report.Notes {
			m.log("overlap " + rig + ": not observed: " + note)
		}
	}
}
