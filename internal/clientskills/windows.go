package clientskills

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The two verdicts the windows decide. S1 could say only `hold`, `no-signal` or `broken-record`; these two
// need a record's `since` and a typed stream, and they are the whole of the loop's judgement:
//
//	demote     a TRUSTED skill with a declared signal that occurred since its `since`
//	promotable a PROVISIONAL skill with PromotionWindow items landed since its `since` and no occurrence
//	hold       neither, which is the honest answer for a skill nothing has tested yet
//
// `broken-record` still wins over both: a decision citing an id that does not exist is not a decision, and a
// window measured from it would measure from a lie.
const (
	// VerdictDemote is a trusted skill a declared signal has contradicted.
	VerdictDemote = "demote"
	// VerdictPromotable is a provisional skill that served PromotionWindow landings without one.
	VerdictPromotable = "promotable"
)

// BroadMinFacts is the smallest window a "too broad" judgement is made from. Below it the ratio is noise:
// one refusal out of one is not a tautology, it is a stream too young to have a denominator, and refusing to
// act on it would make a demote impossible in a quiet room.
const BroadMinFacts = 4

// PromotionWindow is how many landed items promote a provisional skill. Three is OpenSpace's two with one
// more for a repo whose items are larger than a benchmark task.
const PromotionWindow = 3

// CensusWithEvidence is the S2 census: S1's tree census, then each row's record weighed against the typed
// streams since the commit that set its state. repo is the checkout whose history dates that commit; a
// skill whose `since` cannot be dated keeps S1's verdict, and the reason is returned as a note rather than
// guessed into a window.
func (t Tree) CensusWithEvidence(room string, resolve Resolver, evidence Evidence, repo string) ([]Row, []string, error) {
	rows, err := t.Census(room, resolve)
	if err != nil {
		return nil, nil, err
	}
	var notes []string
	for _, skip := range evidence.Skips {
		notes = append(notes, "unread: "+skip.Stream+": "+skip.Reason)
	}
	for i := range rows {
		row := &rows[i]
		if row.Verdict == VerdictBrokenRecord {
			continue
		}
		skill := Skill{Name: row.Name, Dir: row.Path}
		signals, err := skill.Signals()
		if err != nil {
			// censusRow already reported this as broken-record; nothing here can read a declaration twice.
			continue
		}
		sha, since, err := skillSince(repo, row.Path, Record{State: row.State, Since: row.Since})
		if err != nil {
			notes = append(notes, row.Level+"/"+row.Name+": "+err.Error())
			continue
		}
		row.Window = sha
		if len(signals) == 0 {
			row.Verdict = VerdictNoSignal
			continue
		}
		if unread := unreadStreams(signals, evidence); len(unread) > 0 {
			// A signal whose stream nobody read decides nothing: the row keeps S1's verdict and names the
			// gap. Promotion needs zero occurrences, and zero from an unopened file is not evidence.
			row.Unread = unread
			row.Verdict = VerdictHold
			continue
		}
		applyWindow(row, signals, evidence, since)
	}
	return rows, notes, nil
}

// unreadStreams is the set of streams this skill's signals depend on that were not read, in signal order.
func unreadStreams(signals []Signal, evidence Evidence) []string {
	var unread []string
	for _, signal := range signals {
		stream, ok := attributedStreams[signal.Kind]
		if !ok || !evidence.Unread(stream) || contains(unread, stream) {
			continue
		}
		unread = append(unread, stream)
	}
	return unread
}

// contains reports whether values holds value.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// applyWindow decides one row's verdict from its declared signals and the facts since its window opened.
// Only the FOUR attributed streams can demote; TOOL facts are counted and never judged, because a tool call
// that failed says nothing about the skill that recommended the command (the S2 acceptance names this case:
// a trusted skill with a `command:` seen `error` in a TOOL line is not demoted).
func applyWindow(row *Row, signals []Signal, evidence Evidence, since time.Time) {
	var refs []string
	occurrences := 0
	commands := 0
	scope := scopeSeat(row.Level)
	for _, signal := range signals {
		matched := evidence.Occurrences(signal, since, scope)
		if tooBroad(len(matched), evidence.StreamSize(signal, since, scope)) {
			// The signal fires on half its own stream: a tag every gate row carries would demote every
			// skill that declares it, which teaches the loop nothing. It is named and decides nothing.
			row.Broad = append(row.Broad, signal.String())
		} else {
			for _, occurrence := range matched {
				occurrences++
				refs = append(refs, occurrence.Stream+":"+occurrence.Ref)
			}
		}
		commands += evidence.Commands(signal, since, scope)
	}
	row.Occurrences = occurrences
	row.Commands = commands
	row.Landed = evidence.LandedFor(signals, since, scope)
	// A row with occurrences cites them whether it demotes or holds: a `hold` on 44 refusals that names none is
	// a number the reader has to take on trust. The list is deduped and capped - the count stays in Occurrences,
	// so a truncated list is still a truthful one - and a citation is only ever a real ref.
	if occurrences > 0 {
		row.Refs = capRefs(dedupe(refs))
	}
	// R2 of RULING-2026-10-21-skill-signals.md: `command:` counts USE, and a stream that never watches a
	// failure cannot hold a skill to account. A skill whose every declared signal is a command has no failure
	// stream, so its verdict is `no-signal` - the verdict that already means exactly that - with its command
	// counts kept for the reader. `hold` is for a skill that HAS a stream and no occurrence in the window.
	if !hasAttributedSignal(signals) {
		row.Verdict = VerdictNoSignal
		return
	}
	switch {
	case row.State == StateTrusted && occurrences > 0:
		row.Verdict = VerdictDemote
	case row.State == StateProvisional && occurrences == 0 && row.Landed >= PromotionWindow:
		row.Verdict = VerdictPromotable
	default:
		row.Verdict = VerdictHold
	}
}

// refCap is how many citations a row prints. The number is the evidence; the list is the way in, and a reader
// follows the first few before asking whether the rest say anything new.
const refCap = 20

// capRefs keeps the first refCap refs of a row.
func capRefs(refs []string) []string {
	if len(refs) > refCap {
		return refs[:refCap]
	}
	return refs
}

// hasAttributedSignal reports whether any declared signal is matched against a stream that carries a FAILURE.
// `command:` facts are TOOL lines - use, never outcome - so a declaration made only of them has nothing that
// could demote it, whatever the numbers say.
func hasAttributedSignal(signals []Signal) bool {
	for _, signal := range signals {
		if stream, ok := attributedStreams[signal.Kind]; ok && stream != StreamTool {
			return true
		}
	}
	return false
}

// tooBroad is the admission rule applied to a declared signal: a signal that matched at least half of its
// stream's facts inside the window says nothing about this skill, because it would equally have matched the
// skill beside it.
func tooBroad(matched, total int) bool {
	return total >= BroadMinFacts && matched*2 >= total
}

// dedupe keeps the first of each id, so a signal matched by two facts of one refusal is cited once.
func dedupe(values []string) []string {
	seen := map[string]bool{}
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		kept = append(kept, value)
	}
	sort.Strings(kept)
	return kept
}

// skillSince resolves the commit a skill's window opens at, and its date. The record's `since` is the
// commit that SET the state, so it is used when present; an absent record means "provisional since the
// skill's first commit", and that commit is read from the history of the skill's own directory.
//
// A `since` that is not in the repository is an error, not a zero time: a demotion windowed from year one
// would demote every trusted skill the first time a stream was read.
func skillSince(repo, dir string, record Record) (string, time.Time, error) {
	if repo == "" {
		return "", time.Time{}, fmt.Errorf("no checkout to date the record against")
	}
	if err := isGitWorktreeError(repo); err != nil {
		return "", time.Time{}, err
	}
	sha := record.Since
	if sha == "" {
		rel, err := filepath.Rel(repo, dir)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", time.Time{}, fmt.Errorf("%s is not inside %s", dir, repo)
		}
		out, err := exec.Command("git", "-C", repo, "log", "--reverse", "--diff-filter=A",
			"--format=%H", "--", filepath.ToSlash(filepath.Join(rel, SkillFile))).Output()
		if err != nil {
			return "", time.Time{}, fmt.Errorf("no commit added %s: %w", filepath.Join(rel, SkillFile), err)
		}
		lines := strings.Fields(string(out))
		if len(lines) == 0 {
			return "", time.Time{}, fmt.Errorf("%s is not committed, so it has no since", rel)
		}
		sha = lines[0]
	}
	out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%cI", sha).Output()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("record since %s is not a commit of %s", sha, repo)
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out)))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("commit %s has no date: %w", sha, err)
	}
	return sha, at, nil
}

// isGitWorktreeError is the census's own git probe, worded as an error.
func isGitWorktreeError(repo string) error {
	if !isGitWorktree(repo) {
		return fmt.Errorf("%s is not inside a git worktree, so a window cannot be dated", repo)
	}
	return nil
}
