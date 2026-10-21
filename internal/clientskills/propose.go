// The `4genteam skills propose` sub-verb: cited evidence becomes a PROPOSAL in the caller's own worktree -
// the skill's record naming the change and its evidence, and a SKILL.md revision taken verbatim from a
// draft. Nothing is committed and nothing outside the caller's worktree is touched, so the change is
// uncommitted by construction and the gate that lands it reviews a diff rather than a decision (design
// note S3). Detection and proposal may run unattended; landing is still a commit a seat is accountable for.
package clientskills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

const proposeUsage = `usage: 4genteam skills propose [ROOM] --skill NAME --change fix|derived|captured
                              --from QITEM[,QITEM] [--draft FILE] [--risk TEXT] [--skills-root DIR]

Write a PROPOSAL for one skill, into the caller's own worktree and nowhere else: a provisional record.json
naming the change, the cited evidence and the risk --risk states, and - when --draft is given - that
file's bytes as the new SKILL.md, verbatim. Nothing is committed: the change is left uncommitted for a
gate to review.

It refuses, each by name: an evidence id that does not resolve, a skill that declares no signal the
cited rows carry, and a proposal that changes no line. A skill this tree delivers at two levels is
refused too unless --skill names the level as LEVEL/NAME, because two levels are two files.

The caller is $AGENTHUB_SEAT, or the seat $AGENTHUB_SESSION_NAME names; without one there is no worktree
to write into, so it refuses. ROOM comes from the argument, or $AGENTHUB_ROOM.
`

// queueRow reads one cited row. It is a package-level seam, like clientservices.RoomDir: the tests answer
// from memory instead of a daemon, and a row the daemon does not have is the refusal the verb must make.
var queueRow = func(room, id string) (*clientruntime.QueueRow, error) {
	return clientruntime.NewClient("").QueueShow(room, id)
}

// callerWorktree is the ONE directory a proposal may be written under: the seat's own derived worktree.
// It is a seam for the same reason RoomDir is - a test drives the verb over its own fixture - and it is a
// function rather than a flag so no argument can aim a proposal at another seat's tree.
var callerWorktree = func(room string) (string, error) {
	seat := strings.TrimSpace(os.Getenv("AGENTHUB_SEAT"))
	if seat == "" {
		seat = seatFromSession(os.Getenv("AGENTHUB_SESSION_NAME"), room)
	}
	if seat == "" {
		return "", usageError("no seat: a proposal is written into the caller's own worktree, and neither " +
			"$AGENTHUB_SEAT nor $AGENTHUB_SESSION_NAME names one")
	}
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return "", err
	}
	roomDir, err := clientservices.RoomDir(room)
	if err != nil {
		return "", err
	}
	seats, err := clientservices.LiveSeats(root, room)
	if err != nil {
		return "", err
	}
	for _, worktree := range clientservices.DerivedWorktrees(roomDir, room, seats) {
		if worktree.SeatKey == seat {
			return worktree.Path, nil
		}
	}
	return "", usageError(fmt.Sprintf(
		"the room %q names no seat %q, so it derives no worktree to write into", room, seat))
}

// seatFromSession reads the seat out of a canonical session name, `agenthub--<room>--<seat>`. A name that
// does not end in this room's seat answers empty rather than guessing: the room in it is the guard.
func seatFromSession(session, room string) string {
	parts := strings.Split(strings.TrimSpace(session), "--")
	if len(parts) < 3 || parts[len(parts)-2] != room {
		return ""
	}
	return parts[len(parts)-1]
}

// pathInside reports whether target lies inside dir, after resolving symlinks in both: a proposal aimed at
// the shared checkout through a link must not read as "inside my worktree".
func pathInside(dir, target string) bool {
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolvedDir = dir
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		resolvedTarget = target
	}
	relative, err := filepath.Rel(resolvedDir, resolvedTarget)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// findSkill picks the ONE skill a proposal edits. A name two levels deliver is two files, so it is refused
// and the refusal says which levels, unless the ask carried the level as `LEVEL/NAME`.
func findSkill(tree Tree, room, asked string) (RoomSkill, error) {
	level, name := "", asked
	if slash := strings.Index(asked, "/"); slash >= 0 {
		level, name = asked[:slash], asked[slash+1:]
	}
	skills, err := tree.RoomSkills(room)
	if err != nil {
		return RoomSkill{}, err
	}
	var found []RoomSkill
	for _, skill := range skills {
		if skill.Name != name {
			continue
		}
		if level != "" && skill.Level != level {
			continue
		}
		found = append(found, skill)
	}
	switch len(found) {
	case 0:
		return RoomSkill{}, usageError(fmt.Sprintf("the room %q's tree holds no skill %q", room, asked))
	case 1:
		return found[0], nil
	}
	levels := make([]string, 0, len(found))
	for _, skill := range found {
		levels = append(levels, skill.Level+"/"+skill.Name)
	}
	return RoomSkill{}, usageError(fmt.Sprintf(
		"the room %q's tree delivers %q at %d levels (%s); pass one of those as --skill",
		room, name, len(found), strings.Join(levels, ", ")))
}

// splitCites reads the --from list: comma separated, trimmed, and refused when empty or repeated, because
// evidence that repeats itself is a citation nobody can read.
func splitCites(value string) ([]string, error) {
	var cites []string
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		cites = append(cites, id)
	}
	if len(cites) == 0 {
		return nil, usageError("--from is empty: a proposal cites at least one queue row")
	}
	return cites, nil
}

// citeRows resolves every cited row and refuses the whole proposal when one does not: the error says which
// id and what the daemon answered, so an unreachable rail reads differently from a row that is not there.
func citeRows(room string, cites []string) ([]*clientruntime.QueueRow, error) {
	rows := make([]*clientruntime.QueueRow, 0, len(cites))
	for _, id := range cites {
		if !strings.HasPrefix(id, QueueIDPrefix) {
			return nil, usageError(fmt.Sprintf(
				"--from %s is not a %s id: a proposal cites queue rows", id, QueueIDPrefix))
		}
		row, err := queueRow(room, id)
		if err != nil {
			return nil, usageError(fmt.Sprintf(
				"--from %s does not resolve in room %q: %v", id, room, err))
		}
		if row == nil {
			return nil, usageError(fmt.Sprintf("--from %s does not resolve in room %q", id, room))
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// carriesSignal answers the second refusal: the cited rows must carry a signal the skill declared, or the
// proposal cites evidence the skill is not accountable for. A queue row's typed facts are its tags, so the
// only kind that can match is row-tag - a skill that declared stream kinds alone has nothing a row carries.
func carriesSignal(signals []Signal, rows []*clientruntime.QueueRow) (Signal, string) {
	if len(signals) == 0 {
		return Signal{}, "the skill declares no signals, so no cited row can carry one"
	}
	kinds := make([]string, 0, len(signals))
	for _, signal := range signals {
		kinds = append(kinds, signal.String())
	}
	for _, signal := range signals {
		if signal.Kind != SignalRowTag {
			continue
		}
		for _, row := range rows {
			for _, tag := range row.Tags {
				if tag == signal.Value {
					return signal, ""
				}
			}
		}
	}
	return Signal{}, fmt.Sprintf(
		"the skill declares %s, and none of the cited rows carries one (a row's typed facts are its tags, "+
			"so only row-tag: can match)", strings.Join(kinds, ", "))
}

// headCommit is the commit a proposal windows from: the tree's HEAD, which is where the proposal starts
// measuring. An empty answer is not an error - a tree outside git has no HEAD, and the record then keeps the
// `since` it already had, with the report saying so.
func headCommit(root string) string {
	if _, err := exec.LookPath("git"); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runSkillsPropose(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, proposeUsage)
		return clientcmd.ExitOK
	}
	opts, positional, err := clientruntime.SplitFlags(args, []clientruntime.FlagSpec{
		{Name: "skill", Value: true},
		{Name: "change", Value: true},
		{Name: "from", Value: true},
		{Name: "draft", Value: true},
		{Name: "risk", Value: true},
		{Name: "skills-root", Value: true},
	})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.ExitUsage
	}
	room := strings.TrimSpace(os.Getenv("AGENTHUB_ROOM"))
	if len(positional) > 1 {
		fmt.Fprintf(stderr, "4genteam skills propose: at most one ROOM, got %d\n", len(positional))
		return clientcmd.ExitUsage
	}
	if len(positional) == 1 {
		room = positional[0]
	}
	if room == "" {
		_, _ = io.WriteString(stderr, "4genteam skills propose: no room; pass ROOM or set AGENTHUB_ROOM\n")
		return clientcmd.ExitUsage
	}
	name := strings.TrimSpace(opts["skill"])
	if name == "" {
		_, _ = io.WriteString(stderr, "4genteam skills propose: --skill NAME is required\n")
		return clientcmd.ExitUsage
	}
	change := strings.TrimSpace(opts["change"])
	if change == "" {
		_, _ = io.WriteString(stderr, "4genteam skills propose: --change is required (fix, derived, captured)\n")
		return clientcmd.ExitUsage
	}
	switch change {
	case ChangeFix, ChangeDerived, ChangeCaptured:
	default:
		fmt.Fprintf(stderr, "4genteam skills propose: --change %q is none of %q, %q, %q\n",
			change, ChangeFix, ChangeDerived, ChangeCaptured)
		return clientcmd.ExitUsage
	}
	cites, err := splitCites(opts["from"])
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.ExitUsage
	}

	root, err := resolveSkillsRoot(opts["skills-root"])
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	tree := Tree{Root: root}
	skill, err := findSkill(tree, room, name)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}

	// The write target is decided BEFORE the refusals look at evidence: a proposal that cannot be written
	// where it belongs is a caller error about the path, not about the citation.
	worktree, err := callerWorktree(room)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	if !pathInside(worktree, skill.Dir) {
		fmt.Fprintf(stderr, "4genteam skills propose: %s is outside your own worktree %s; a proposal is "+
			"written only into the caller's worktree\n", skill.Dir, worktree)
		return clientcmd.ExitUsage
	}

	rows, err := citeRows(room, cites)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	signals, err := skill.Signals()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	signal, reason := carriesSignal(signals, rows)
	if reason != "" {
		fmt.Fprintf(stderr, "4genteam skills propose: %s\n", reason)
		return clientcmd.ExitUsage
	}

	// The record a proposal writes is a STUB for a change under review: provisional, whatever the skill
	// claimed before, because the proposal restarts the window at HEAD (below) and the gate that lands it
	// states the trust it earned. The risk line is the one the caller names; with none, it stays empty -
	// inventing a risk would be writing a decision nobody took.
	record := DefaultRecord()
	record.Change = change
	record.Evidence = cites
	record.Risk = strings.TrimSpace(opts["risk"])
	var notes []string
	if head := headCommit(root); head != "" {
		record.Since = head
	} else {
		notes = append(notes, root+" is not inside a git worktree, so the record has no since")
	}
	rendered, err := RenderRecord(record)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}

	var draft []byte
	if path := opts["draft"]; path != "" {
		draft, err = os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam skills propose: cannot read --draft: %v\n", err)
			return clientcmd.ExitUsage
		}
	}

	recordPath := filepath.Join(skill.Dir, RecordFile)
	skillPath := filepath.Join(skill.Dir, SkillFile)
	existingRecord, recordPresent, err := readIfPresent(recordPath)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.ExitUsage
	}
	existingSkill, skillPresent, err := readIfPresent(skillPath)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.ExitUsage
	}
	recordChanges := !recordPresent || !bytes.Equal(existingRecord, rendered)
	skillChanges := draft != nil && (!skillPresent || !bytes.Equal(existingSkill, draft))
	if !recordChanges && !skillChanges {
		fmt.Fprintf(stderr, "4genteam skills propose: the proposal changes no line: %s already holds this "+
			"record and %s already holds the draft\n", recordPath, skillPath)
		return clientcmd.ExitUsage
	}

	if recordChanges {
		if err := WriteRecord(skill.Dir, record); err != nil {
			fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
			return clientcmd.CodeOf(err, clientcmd.ExitUsage)
		}
	}
	if skillChanges {
		if err := os.WriteFile(skillPath, draft, 0o644); err != nil {
			fmt.Fprintf(stderr, "4genteam skills propose: cannot write %s: %v\n", skillPath, err)
			return clientcmd.ExitUsage
		}
	}

	fmt.Fprintf(stdout, "skill: %s/%s  change: %s  state: %s\n", skill.Level, skill.Name, change, record.State)
	if record.Since != "" {
		fmt.Fprintf(stdout, "since: %s\n", record.Since)
	}
	fmt.Fprintf(stdout, "evidence: %s\n", strings.Join(cites, ","))
	fmt.Fprintf(stdout, "signal: %s\n", signal.String())
	if recordChanges {
		fmt.Fprintf(stdout, "wrote: %s\n", recordPath)
	}
	if skillChanges {
		fmt.Fprintf(stdout, "wrote: %s (from --draft)\n", skillPath)
	}
	for _, note := range notes {
		fmt.Fprintf(stdout, "note: %s\n", note)
	}
	fmt.Fprintln(stdout, "note: nothing was committed; the change is a proposal in your worktree")
	return clientcmd.ExitOK
}

// readIfPresent is the file's bytes and whether it was there. A read that failed for any other reason is an
// error: reading a file this verb is about to overwrite as "absent" would hide a permission the caller
// should hear about.
func readIfPresent(path string) ([]byte, bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cannot read %s: %w", path, err)
	}
	return body, true, nil
}
