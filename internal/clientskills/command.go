// The `4genteam skills` verb's `evidence` sub-verb: the census. It reads each skill's record, its declared
// signals and the typed streams since the commit that set the record's state, and prints one verdict per
// skill - hold, no-signal, broken-record, promotable or demote. A stream it could not read is named in the
// report's notes instead of being counted as silence.
package clientskills

import (
	"context"
	"encoding/json"

	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// EvidenceSchema is the JSON report's version. A consumer checks it before reading.
const EvidenceSchema = "4genteam.skills.evidence.v1"

const skillsUsage = `usage: 4genteam skills evidence [ROOM] [--skill NAME] [--skills-root DIR] [--json]

Read every skill a room delivers - share/, the room's own, and each seat folder's - and print its state
(provisional|trusted), its declared signals, its evidence, and one verdict: hold, no-signal (no declared
signals) or broken-record (a record that cannot be read, or an evidence id that does not resolve).

The verb only READS: records, SKILL.md front matter, the room's queue rows and the tree's git history. It
exits 0 whatever the verdicts are, because a report is the answer.

The tree is --skills-root, else $AGENTHUB_CLIENT_SKILLS (the one variable the runtime installs from), else
./skills or ./agenthub_client/skills when one of them holds share/.

ROOM comes from the argument, or $AGENTHUB_ROOM.
`

// skillsCommand is the family verb. Sub-verbs dispatch on the first positional, so `skills evidence` costs
// one extra word and leaves room for `skills propose` in S3 without a second registration.
type skillsCommand struct{}

func (skillsCommand) Name() string    { return "skills" }
func (skillsCommand) Summary() string { return "the skill tree's evidence records and verdicts" }

func (skillsCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) || len(args) == 0 {
		_, _ = io.WriteString(stdout, skillsUsage)
		if len(args) == 0 {
			return clientcmd.ExitUsage
		}
		return clientcmd.ExitOK
	}
	switch args[0] {
	case "evidence":
		return runSkillsEvidence(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "4genteam skills: unknown sub-verb %q (known: evidence)\n", args[0])
		return clientcmd.ExitUsage
	}
}

// Commands returns the client's `skills` verbs for the dispatcher.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{skillsCommand{}}
}

// EvidenceReport is the JSON answer: the tree it read, the tally, and one row per skill.
type EvidenceReport struct {
	Schema     string   `json:"schema"`
	Room       string   `json:"room"`
	SkillsRoot string   `json:"skills_root"`
	Counts     Counts   `json:"counts"`
	Skills     []Row    `json:"skills"`
	Notes      []string `json:"notes,omitempty"`
}

func runSkillsEvidence(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, skillsUsage)
		return clientcmd.ExitOK
	}
	opts, positional, err := clientruntime.SplitFlags(args, []clientruntime.FlagSpec{
		{Name: "skill", Value: true},
		{Name: "skills-root", Value: true},
		{Name: "json"},
	})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills evidence: %v\n", err)
		return clientcmd.ExitUsage
	}
	room := strings.TrimSpace(os.Getenv("AGENTHUB_ROOM"))
	if len(positional) > 1 {
		fmt.Fprintf(stderr, "4genteam skills evidence: at most one ROOM, got %d\n", len(positional))
		return clientcmd.ExitUsage
	}
	if len(positional) == 1 {
		room = positional[0]
	}
	if room == "" {
		_, _ = io.WriteString(stderr,
			"4genteam skills evidence: no room; pass ROOM or set AGENTHUB_ROOM\n")
		return clientcmd.ExitUsage
	}

	root, err := resolveSkillsRoot(opts["skills-root"])
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills evidence: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	tree := Tree{Root: root}

	resolver, note := evidenceResolver(room, root)
	stateRoot, _ := clientenv.ResolveStateRoot("")
	evidence := LoadEvidence(stateRoot, room, root)
	note = railRows(&evidence, room, note)
	rows, windowNotes, err := tree.CensusWithEvidence(room, resolver, evidence, root)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam skills evidence: %v\n", err)
		return clientcmd.CodeOf(err, clientcmd.ExitUsage)
	}
	if name := opts["skill"]; name != "" {
		filtered := rows[:0]
		for _, row := range rows {
			if row.Name == name {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
		if len(rows) == 0 {
			fmt.Fprintf(stderr, "4genteam skills evidence: no skill named %q in %s\n", name, room)
			return clientcmd.ExitUsage
		}
	}

	report := EvidenceReport{
		Schema:     EvidenceSchema,
		Room:       room,
		SkillsRoot: root,
		Counts:     Count(rows),
		Skills:     rows,
	}
	if note != "" {
		report.Notes = append(report.Notes, note)
	}
	report.Notes = append(report.Notes, windowNotes...)

	if opts["json"] == "true" {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam skills evidence: cannot encode the report: %v\n", err)
			return clientcmd.ExitRemote
		}
		_, _ = stdout.Write(append(body, '\n'))
		return clientcmd.ExitOK
	}
	writeEvidenceText(stdout, report)
	return clientcmd.ExitOK
}

// writeEvidenceText prints the human report. The first line is the measurement, then one line per skill:
// level/name, state, verdict, and the counts a reader needs to know whether the row is silent by accident.
func writeEvidenceText(w io.Writer, report EvidenceReport) {
	fmt.Fprintf(w, "room: %s  skills: %d  hold: %d  no-signal: %d  broken-record: %d  demote: %d  promotable: %d  trusted: %d\n",
		report.Room, report.Counts.Skills, report.Counts.Hold, report.Counts.NoSignal,
		report.Counts.Broken, report.Counts.Demote, report.Counts.Promotable, report.Counts.Trusted)
	fmt.Fprintf(w, "tree: %s\n", report.SkillsRoot)
	for _, note := range report.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
	for _, row := range report.Skills {
		label := row.Level + "/" + row.Name
		fmt.Fprintf(w, "%-52s %-11s %-13s signals:%d evidence:%d",
			label, row.State, row.Verdict, len(row.Signals), len(row.Evidence))
		if row.Occurrences > 0 || row.Commands > 0 || row.Landed > 0 {
			fmt.Fprintf(w, " occ:%d cmd:%d landed:%d", row.Occurrences, row.Commands, row.Landed)
		}
		if len(row.Refs) > 0 {
			fmt.Fprintf(w, " refs:%s", strings.Join(row.Refs, ","))
		}
		if len(row.Unread) > 0 {
			fmt.Fprintf(w, " unread:%s", strings.Join(row.Unread, ","))
		}
		if len(row.Broad) > 0 {
			fmt.Fprintf(w, " broad:%s", strings.Join(row.Broad, ","))
		}
		if len(row.Broken) > 0 {
			fmt.Fprintf(w, " broken:%s", strings.Join(row.Broken, ","))
		}
		if row.Fault != "" {
			fmt.Fprintf(w, " fault:%s", row.Fault)
		}
		fmt.Fprintln(w)
	}
}

// resolveSkillsRoot finds the tree: the flag, the runtime's own variable, or one of the two checked-out
// locations. A candidate counts only when it holds share/ - the same test Tree.Require makes - so a wrong
// path is refused by name instead of quietly listing nothing.
func resolveSkillsRoot(flagValue string) (string, error) {
	candidates := []string{flagValue}
	if strings.TrimSpace(flagValue) == "" {
		candidates = append(candidates,
			strings.TrimSpace(os.Getenv(RootEnv)),
			SkillsDir,
			filepath.Join("agenthub_client", SkillsDir),
		)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(candidate, ShareDir)); err == nil && info.IsDir() {
			absolute, err := filepath.Abs(candidate)
			if err != nil {
				return candidate, nil
			}
			return absolute, nil
		}
	}
	return "", usageError("the client's skill tree was not found: pass --skills-root DIR or set " + RootEnv)
}

// shaPattern is a commit name an evidence id may cite.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// QueueIDPrefix marks a queue row as evidence. The rail's own ids carry it, so the distinction is the
// id's shape rather than a guess.
const QueueIDPrefix = "qitem-"

// evidenceResolver builds the lookup the census calls per evidence id. It answers three ways, and the
// difference between the last two is the point: an id it could NOT check (daemon down, tree outside git) is
// unchecked, while an id it checked and did not find is broken. The returned note says what could not be
// looked at, so a report never reads as "nothing is wrong" when something was simply not asked.
func evidenceResolver(room, root string) (Resolver, string) {
	client := clientruntime.NewClient("")
	queueUp := client.Reachable()
	gitUp := isGitWorktree(root)
	var notes []string
	if !queueUp {
		notes = append(notes, "the room's queue is not reachable, so "+QueueIDPrefix+" ids were not checked")
	}
	if !gitUp {
		notes = append(notes, root+" is not inside a git worktree, so commit evidence was not checked")
	}

	resolver := func(id string) Resolution {
		switch {
		case strings.HasPrefix(id, QueueIDPrefix):
			if !queueUp {
				return Resolution{Reason: "queue not reachable"}
			}
			if _, err := client.QueueShow(room, id); err != nil {
				return Resolution{Checked: true, Reason: err.Error()}
			}
			return Resolution{Checked: true, Found: true}
		case shaPattern.MatchString(id):
			if !gitUp {
				return Resolution{Reason: "not a git worktree"}
			}
			err := exec.Command("git", "-C", root, "cat-file", "-e", id+"^{commit}").Run()
			if err != nil {
				return Resolution{Checked: true, Reason: "no such commit"}
			}
			return Resolution{Checked: true, Found: true}
		default:
			return Resolution{Reason: "neither a " + QueueIDPrefix + " id nor a commit name"}
		}
	}
	return resolver, strings.Join(notes, "; ")
}

// railRows hands the census the room's rows, because the census reads no socket: the runtime's own reader
// answers here, and a rail that cannot be reached leaves the landed count at zero - a skill is then not
// promoted on a number nobody measured, and the note says why.
func railRows(evidence *Evidence, room, note string) string {
	client := clientruntime.NewClient("")
	if !client.Reachable() {
		evidence.Skips = append(evidence.Skips, Skip{Stream: StreamQueue,
			Reason: "the room's queue is not reachable, so landings were not counted"})
		return joinNotes(note, "the room's queue is not reachable, so landings were not counted")
	}
	raw, err := client.QueueList(room, clientruntime.QueueListFilter{})
	if err != nil {
		evidence.Skips = append(evidence.Skips, Skip{Stream: StreamQueue,
			Reason: "the room's queue could not be listed: " + err.Error()})
		return joinNotes(note, "the room's queue could not be listed: "+err.Error())
	}
	rows := make([]RailRow, 0, len(raw))
	for _, row := range raw {
		created, _ := time.Parse(time.RFC3339Nano, row.TsCreated)
		updated, _ := time.Parse(time.RFC3339Nano, row.TsUpdated)
		rows = append(rows, RailRow{ID: row.QitemID, State: row.State, Tags: row.Tags,
			Created: created, Updated: updated})
	}
	evidence.Rows = rows
	return note
}

// joinNotes appends one note to another without a stray separator.
func joinNotes(existing, added string) string {
	if existing == "" {
		return added
	}
	return existing + "; " + added
}

// isGitWorktree reports whether dir is inside a repository, using one command rather than treating every
// failed lookup as a missing object.
func isGitWorktree(dir string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}
