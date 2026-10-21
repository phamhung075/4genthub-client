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

// proposeEvidence loads the typed streams a non-row cite is resolved against. It is the SAME load the census
// verb does (`skills evidence`), which is what keeps the two in step: a ref the report prints is a ref this
// verb can resolve, because one function produced both. A seam for the same reason queueRow is one - a test
// hands over facts instead of writing four stream files - and read LAZILY, so a proposal that cites only queue
// rows never opens a stream it does not need.
var proposeEvidence = func(room, repo string) Evidence {
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return Evidence{}
	}
	return LoadEvidence(root, room, repo)
}

// callerWorktree is the ONE directory a proposal may be written under: the seat's own derived worktree.
// It is a seam for the same reason RoomDir is - a test drives the verb over its own fixture - and it is a
// function rather than a flag so no argument can aim a proposal at another seat's tree.
var callerWorktree = func(room string) (string, error) {
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		return "", err
	}
	// The room's own seat keys are read before the seat is resolved, because they are half of what a BARE
	// session name means: `reviewer` is a seat only if the room says it is one.
	seats, err := clientservices.LiveSeats(root, room)
	if err != nil {
		return "", err
	}
	seat := strings.TrimSpace(os.Getenv("AGENTHUB_SEAT"))
	if seat == "" {
		seat = seatFromSession(os.Getenv("AGENTHUB_SESSION_NAME"), room, seatKeys(seats))
	}
	if seat == "" {
		return "", usageError("no seat: a proposal is written into the caller's own worktree, and neither " +
			"$AGENTHUB_SEAT nor $AGENTHUB_SESSION_NAME names one")
	}
	// RoomDir takes the root this verb resolved, never one it re-derives: the seat's worktree is read under
	// the same store as everything else the proposal touched (the RoomDir rule, 2026-10-21).
	roomDir, err := clientservices.RoomDir(root, room)
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

// seatFromSession reads the seat out of a session name or a row's destination. The store holds THREE
// spellings - `agenthub--<room>--<seat>`, `<seat>@<room>` and the bare `<seat>` - and this is the one reader
// for all of them: a seat cannot be invisible to the census because a row was written through another spelling.
// A name for another room answers empty rather than guessing: the room in it is the guard.
//
// The bare spelling is only a seat when the room's OWN seat keys say so (`clientservices.LiveSeats`): fifty
// rows in this room name a seat bare, and a destination the room does not spell as a seat - `owner-session`,
// prose - must stay unattributed rather than demote a skill. An empty set narrows the reader to the two
// spelled forms and is never an error.
func seatFromSession(session, room string, seats map[string]bool) string {
	value := strings.TrimSpace(session)
	if value == "" {
		return ""
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		if value[at+1:] == room {
			return value[:at]
		}
		return ""
	}
	if parts := strings.Split(value, "--"); len(parts) >= 3 {
		if parts[len(parts)-2] != room {
			return ""
		}
		return parts[len(parts)-1]
	}
	if seats[value] {
		return value
	}
	return ""
}

// seatKeys is a room's seats as the set the seat reader consults: its own keys, which are the authority for the
// bare spelling. It is a set, not a list, because every row of every review asks it the same question.
func seatKeys(seats []clientservices.SeatType) map[string]bool {
	keys := make(map[string]bool, len(seats))
	for _, seat := range seats {
		if key := strings.TrimSpace(seat.Key); key != "" {
			keys[key] = true
		}
	}
	return keys
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

// cite is one --from value decided: a rail row, or one fact of a typed stream. Exactly one of the two is set.
type cite struct {
	ID   string
	Row  *clientruntime.QueueRow
	Fact *Fact
}

// resolveCites decides every cited value before the proposal looks at the skill, and refuses the whole
// proposal when one does not resolve - the error says which value and what was looked for.
//
// A value is a queue row (`qitem-…`, today's rule, kept) or ONE OF THE CENSUS'S OWN REFS: a refusal id, a
// `seat <seat> <ts>` line, a `jev:<ts> <tier>` line or a `<from>-><to>` send line. The refs are not a second
// vocabulary to keep in step - they are the `ref` field of the facts `skills evidence` prints, resolved
// through the census's own loader, so a cite the report shows is a cite this verb can always resolve.
func resolveCites(room, repo string, cites []string) ([]cite, error) {
	out := make([]cite, 0, len(cites))
	var evidence *Evidence
	for _, id := range cites {
		if strings.HasPrefix(id, QueueIDPrefix) {
			row, err := queueRow(room, id)
			if err != nil {
				return nil, usageError(fmt.Sprintf(
					"--from %s does not resolve in room %q: %v", id, room, err))
			}
			if row == nil {
				return nil, usageError(fmt.Sprintf("--from %s does not resolve in room %q", id, room))
			}
			out = append(out, cite{ID: id, Row: row})
			continue
		}
		if evidence == nil {
			loaded := proposeEvidence(room, repo)
			evidence = &loaded
		}
		fact, ok := evidence.fact(id)
		if !ok {
			return nil, usageError(fmt.Sprintf(
				"--from %s is neither a %s id nor a ref `4genteam skills evidence` prints for room %q",
				id, QueueIDPrefix, room))
		}
		out = append(out, cite{ID: id, Fact: fact})
	}
	return out, nil
}

// fact is the one stream fact whose ref is exactly this value. An exact match, not a prefix: a ref is what a
// report prints, and a value that only starts like one is a cite nobody can look up.
func (e Evidence) fact(ref string) (*Fact, bool) {
	for i := range e.Facts {
		if e.Facts[i].Ref == ref {
			return &e.Facts[i], true
		}
	}
	return nil, false
}

// carriesSignal answers the second refusal: the cited evidence must carry a signal the skill declared, or the
// proposal cites evidence the skill is not accountable for. A queue row's typed facts are its tags, so only
// `row-tag:` matches a row; every other kind matches a fact of ITS OWN stream whose value is the signal's -
// the same pairing `Occurrences` makes when it demotes a skill, so a cite cannot reach a rule the census
// would never fire.
func carriesSignal(signals []Signal, cites []cite) (Signal, string) {
	if len(signals) == 0 {
		return Signal{}, "the skill declares no signals, so no cited evidence can carry one"
	}
	kinds := make([]string, 0, len(signals))
	for _, signal := range signals {
		kinds = append(kinds, signal.String())
	}
	for _, signal := range signals {
		stream, ok := attributedStreams[signal.Kind]
		if !ok {
			continue
		}
		for _, cited := range cites {
			if stream == StreamQueue {
				if cited.Row == nil {
					continue
				}
				for _, tag := range cited.Row.Tags {
					if tag == signal.Value {
						return signal, ""
					}
				}
				continue
			}
			if cited.Fact != nil && cited.Fact.Stream == stream && cited.Fact.Value == signal.Value {
				return signal, ""
			}
		}
	}
	return Signal{}, fmt.Sprintf(
		"the skill declares %s, and none of the cited evidence carries one (a queue row's typed facts are its "+
			"tags, so only row-tag: can match a row; any other kind needs a ref that `4genteam skills evidence` "+
			"prints)", strings.Join(kinds, ", "))
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

	resolved, err := resolveCites(room, worktree, cites)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	signals, err := skill.Signals()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills propose: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	signal, reason := carriesSignal(signals, resolved)
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
