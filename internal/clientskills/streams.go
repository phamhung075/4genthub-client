package clientskills

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The typed streams a declared signal is matched against. Each is a RECORD A TOOL ALREADY WROTE, never a
// model's summary of one: the guard's refusal spool, the comm policy's audit, the Jev decision log, the
// seats' tool-calls files and the room's rail. The four paths are the ones the design note measured
// (`DESIGN-2026-10-21-skill-evidence-loop.md`, "Evidence: four streams"), and `clientactivity` reads the same
// three files for the human timeline; the census keeps its own reader because it needs the RAW field a
// signal names - a SEND `Reason`, a JEV `Kind`, a TOOL `target` - which the timeline flattens into a line.
const (
	StreamGuard = "guard"
	StreamSend  = "send"
	StreamJEV   = "jev"
	StreamQueue = "queue"
	StreamTool  = "tool"
)

// Attributed signals, in the order a report names them: the four that can demote, then the one that counts.
var attributedStreams = map[string]string{
	SignalGuard:   StreamGuard,
	SignalSend:    StreamSend,
	SignalJEV:     StreamJEV,
	SignalRowTag:  StreamQueue,
	SignalCommand: StreamTool,
}

// Fact is one line of a typed stream, reduced to what a signal can be matched against. Value is the field
// the stream's signal names: a refusal rule, a send Reason, a Jev Kind, a queue tag, a tool target. Every
// Fact carries the id a reader can follow (a refusal id, a qitem, `from->to`), because a demotion nobody
// can look up is a claim.
type Fact struct {
	Stream string    `json:"stream"`
	Value  string    `json:"value"`
	At     time.Time `json:"at"`
	Ref    string    `json:"ref"`
}

// RailRow is one queue row as the census needs it: the row's identity, its state, its tags and its two
// timestamps. It is NOT clientruntime.QueueRow - the census reads no socket, so the caller fetches the rows
// and hands them over; a test writes four fields instead of a daemon's whole record.
type RailRow struct {
	ID      string
	State   string
	Tags    []string
	Created time.Time
	Updated time.Time
}

// Evidence is every typed fact the windows can see, plus the room's rows for the landed count. A stream
// that could not be read is simply absent, and Skipped says so: an absent stream must never read as "no
// occurrence", which would promote a skill nobody watched.
type Evidence struct {
	Facts []Fact
	Rows  []RailRow
	// Skips names every stream that was NOT read, by stream. It decides a skill's verdict: a stream nobody
	// opened cannot report an occurrence, and counting that as silence would promote on a file no one read.
	Skips []Skip
}

// Skip is one stream the loader could not read, and why.
type Skip struct {
	Stream string
	Reason string
}

// Unread reports whether the stream was skipped.
func (e Evidence) Unread(stream string) bool {
	for _, skip := range e.Skips {
		if skip.Stream == stream {
			return true
		}
	}
	return false
}

// LoadEvidence reads the streams for one room. stateRoot is the client's state root (`clientenv.ResolveStateRoot`);
// repo is a checkout whose git dir carries the refusal spool - the tree the census reads, since that is where
// a refused commit would have been made. A read that fails is recorded in Skipped, never returned as an error:
// one unreadable stream narrows what the census knows, and the report says so.
func LoadEvidence(stateRoot, room, repo string) Evidence {
	var evidence Evidence

	evidence.add(readGuard(repo, stateRoot, room, &evidence))
	evidence.add(readSends(stateRoot, room, &evidence))
	evidence.add(readJev(stateRoot, &evidence))
	evidence.add(readTools(stateRoot, room, &evidence))

	sort.SliceStable(evidence.Facts, func(i, j int) bool { return evidence.Facts[i].At.Before(evidence.Facts[j].At) })
	return evidence
}

// add keeps a stream's skip when it had one.
func (e *Evidence) add(skip Skip) {
	if skip.Reason != "" {
		e.Skips = append(e.Skips, skip)
	}
}

// Occurrences returns the refused-or-escalated facts a declared signal matches at or after since. A
// `command:` signal is NOT here: TOOL is counts only, so a tool call that failed can never demote a skill
// (the acceptance for S2 names exactly this case).
func (e Evidence) Occurrences(signal Signal, since time.Time) []Occurrence {
	stream, ok := attributedStreams[signal.Kind]
	if !ok || stream == StreamTool {
		return nil
	}
	if stream == StreamQueue {
		return rowOccurrences(e.Rows, signal.Value, since)
	}
	var found []Occurrence
	seen := map[string]bool{}
	for _, fact := range e.Facts {
		if fact.Stream != stream || fact.At.Before(since) {
			continue
		}
		if fact.Value != signal.Value {
			continue
		}
		key := fact.Stream + "\x00" + fact.Ref + "\x00" + fact.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		found = append(found, Occurrence{At: fact.At, Stream: fact.Stream, Value: fact.Value, Ref: fact.Ref})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].At.Before(found[j].At) })
	return found
}

// rowOccurrences is the rail's half of a `row-tag:` signal: every row carrying the tag that was CREATED
// inside the window. A row's own transitions are not read - the tag was put on the row when it was written,
// and that is the moment the typed finding appeared.
func rowOccurrences(rows []RailRow, tag string, since time.Time) []Occurrence {
	var found []Occurrence
	for _, row := range rows {
		if row.Created.Before(since) {
			continue
		}
		for _, has := range row.Tags {
			if has == tag {
				found = append(found, Occurrence{At: row.Created, Stream: StreamQueue, Value: tag, Ref: row.ID})
				break
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].At.Before(found[j].At) })
	return found
}

// StreamSize counts the facts of a signal's stream inside the window: the denominator of the "too broad"
// rule. A stream with nothing in the window has size zero, and no ratio can be taken from it.
func (e Evidence) StreamSize(signal Signal, since time.Time) int {
	stream, ok := attributedStreams[signal.Kind]
	if !ok {
		return 0
	}
	if stream == StreamQueue {
		size := 0
		for _, row := range e.Rows {
			if !row.Created.Before(since) {
				size++
			}
		}
		return size
	}
	size := 0
	for _, fact := range e.Facts {
		if fact.Stream == stream && !fact.At.Before(since) {
			size++
		}
	}
	return size
}

// Commands counts the TOOL lines whose target carries a declared command, at or after since. Literal,
// case-sensitive, no globs - a command signal is a substring of what the tool was asked to do.
func (e Evidence) Commands(signal Signal, since time.Time) int {
	if signal.Kind != SignalCommand || signal.Value == "" {
		return 0
	}
	count := 0
	for _, fact := range e.Facts {
		if fact.Stream != StreamTool || fact.At.Before(since) {
			continue
		}
		if strings.Contains(fact.Value, signal.Value) {
			count++
		}
	}
	return count
}

// LandedFor counts the room's rows that reached `done` at or after since AND carry one of the skill's OWN
// declared `row-tag:` values. A landing is a row, and the only fact a row carries is its tags - so a landing a
// reader cannot attribute to the skill is some other skill's work, and counting it would promote this one on
// evidence it never carried. A skill that declares no row-tag therefore counts zero: it has no landing of its
// own, and the promotion the census grants on landings is not for it.
func (e Evidence) LandedFor(signals []Signal, since time.Time) int {
	own := map[string]bool{}
	for _, signal := range signals {
		if signal.Kind == SignalRowTag && signal.Value != "" {
			own[signal.Value] = true
		}
	}
	if len(own) == 0 {
		return 0
	}
	count := 0
	for _, row := range e.Rows {
		if row.State != "done" || row.Updated.Before(since) {
			continue
		}
		for _, tag := range row.Tags {
			if own[tag] {
				count++
				break
			}
		}
	}
	return count
}

// Occurrence is one matched fact, as a row reports it: when, from which stream, and the id to follow.
type Occurrence struct {
	At     time.Time `json:"at"`
	Stream string    `json:"stream"`
	Value  string    `json:"value"`
	Ref    string    `json:"ref"`
}

// guardRule is the rule name a `guard:` signal is matched against, with the separator folded to `-`.
//
// MEASURED, 2026-10-21: the guard has TWO producers and they spell the same kind differently. The commit
// wrapper writes `rule: "docs_only"` into its refusal spool (scripts/git-hooks/git_commit_capture.py, the one
// `record_refusal` call), while the seat guard writes `rule: "docs-only"` - and ten other hyphenated kinds -
// into each seat's tool log. Without the fold, every `guard:` signal a person writes from the guard's own
// vocabulary is dead: the reviewer's S2 verdict measured exactly this (rule=docs_only vs 757 blocked tool
// lines carrying a rule) and it would have been a counter that can never fire.
func guardRule(rule string) string {
	return strings.ReplaceAll(strings.TrimSpace(rule), "_", "-")
}

// readGuard reads the guard's refusals from BOTH producers: the commit wrapper's spool
// (<git dir>/parked-diffs/refusals.jsonl, one line per refused commit) and each seat's own tool log
// (<state root>/seats/<room>/*/tool-calls.jsonl, the lines the guard wrote with `outcome: blocked`).
//
// THE STREAM IS UNREAD ONLY WHEN NO PRODUCER FILE WAS THERE AT ALL. Both are append-on-event files - a
// wrapper that never refused a commit writes no spool, a seat the guard never blocked writes no blocked
// line - so an absent file is measured silence and not a gap, and a present-but-empty one is the same. The
// two are reported as one stream because a `guard:` signal names a rule, and a rule fires wherever the guard
// enforces it.
func readGuard(repo, stateRoot, room string, evidence *Evidence) Skip {
	readAny := false
	if repo != "" {
		if path, err := refusalSpoolPath(repo); err == nil {
			if _, statErr := os.Stat(path); statErr == nil {
				readAny = true
				readJSONLines(path, func(raw []byte) {
					var line struct {
						ID   string `json:"id"`
						At   string `json:"ts"`
						Rule string `json:"rule"`
					}
					if json.Unmarshal(raw, &line) != nil || line.Rule == "" {
						return
					}
					at, err := time.Parse(time.RFC3339Nano, line.At)
					if err != nil {
						return
					}
					evidence.Facts = append(evidence.Facts, Fact{Stream: StreamGuard,
						Value: guardRule(line.Rule), At: at, Ref: line.ID})
				})
			}
		}
	}
	// The seat's own directory, where the runtime points PI_CODING_AGENT_DIR and the guard writes its log:
	// <state root>/rooms/<room>/seats/<seat>/tool-calls.jsonl, the same path the TOOL stream reads.
	paths, _ := filepath.Glob(filepath.Join(stateRoot, "rooms", room, "seats", "*", "tool-calls.jsonl"))
	for _, path := range paths {
		readAny = true
		readJSONLines(path, func(raw []byte) {
			var line struct {
				At      string `json:"at"`
				Seat    string `json:"seat"`
				Outcome string `json:"outcome"`
				Rule    string `json:"rule"`
			}
			if json.Unmarshal(raw, &line) != nil || line.Rule == "" || line.Outcome != "blocked" {
				return
			}
			at, err := time.Parse(time.RFC3339Nano, line.At)
			if err != nil {
				return
			}
			evidence.Facts = append(evidence.Facts, Fact{Stream: StreamGuard, Value: guardRule(line.Rule),
				At: at, Ref: "seat " + line.Seat + " " + line.At})
		})
	}
	if !readAny {
		return Skip{Stream: StreamGuard,
			Reason: "neither a guard refusal spool nor a seat tool log was found for " + room}
	}
	return Skip{}
}

// refusalSpoolPath is the guard's own spool, asked of git rather than assumed: a linked worktree's git dir is
// not `<repo>/.git`, and the spool lives in the directory git names.
func refusalSpoolPath(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return "", fmt.Errorf("git dir of %s: %w", repo, err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "parked-diffs", "refusals.jsonl"), nil
}

// readSends reads each seat's comm-policy audit and keeps the REFUSED decisions: `Allowed:false` with the
// policy's `Reason` is a send the room's links did not license, which is what a `send:` signal names.
func readSends(stateRoot, room string, evidence *Evidence) Skip {
	if stateRoot == "" {
		return Skip{Stream: StreamSend, Reason: "no state root, so the send audit was not read"}
	}
	paths, _ := filepath.Glob(filepath.Join(stateRoot, "seats", room, "*", "audit.jsonl"))
	if len(paths) == 0 {
		return Skip{Stream: StreamSend, Reason: "no seat of " + room + " has a send audit"}
	}
	for _, path := range paths {
		readJSONLines(path, func(raw []byte) {
			var line struct {
				At      time.Time
				From    string
				To      string
				Allowed bool
				Reason  string
			}
			// Lifecycle lines share the file and carry no From; only a send decision has one.
			if json.Unmarshal(raw, &line) != nil || line.From == "" || line.At.IsZero() || line.Allowed {
				return
			}
			evidence.Facts = append(evidence.Facts, Fact{Stream: StreamSend, Value: line.Reason,
				At: line.At, Ref: line.From + "->" + line.To})
		})
	}
	return Skip{}
}

// readJev reads the machine-wide Jev decision log and keeps every decision's `kind`. The log is not per room,
// so a `jev:` signal names a kind any seat's escalation of that shape produces.
func readJev(stateRoot string, evidence *Evidence) Skip {
	if stateRoot == "" {
		return Skip{Stream: StreamJEV, Reason: "no state root, so the Jev decisions were not read"}
	}
	path := filepath.Join(stateRoot, jevLogFile)
	if _, err := os.Stat(path); err != nil {
		return Skip{Stream: StreamJEV, Reason: "no " + jevLogFile + " in " + stateRoot}
	}
	readJSONLines(path, func(raw []byte) {
		// The log carries no id (measured 2026-10-21: keys timestamp, kind, model, confidence_threshold,
		// summary, detail, choices, tier, category, outcome, questions - 0 of 512 lines had one), so the ref a
		// reader follows is the line's own identity: its timestamp, in the log's order. A `jev:` ref with
		// nothing after it is a demotion nobody can look up, which is what the S2 verdict's minor measured.
		var line struct {
			Timestamp time.Time `json:"timestamp"`
			Kind      string    `json:"kind"`
			Tier      string    `json:"tier"`
		}
		if json.Unmarshal(raw, &line) != nil || line.Kind == "" || line.Timestamp.IsZero() {
			return
		}
		ref := "jev:" + line.Timestamp.UTC().Format(time.RFC3339Nano)
		if line.Tier != "" {
			ref += " " + line.Tier
		}
		evidence.Facts = append(evidence.Facts, Fact{Stream: StreamJEV, Value: line.Kind,
			At: line.Timestamp, Ref: ref})
	})
	return Skip{}
}

// jevLogFile is the log clientjev writes.
const jevLogFile = "jev_decisions.log"

// readTools reads each seat's tool-calls file. Every line becomes a TOOL fact whose Value is the target a
// `command:` signal matches; the outcome is NOT kept, because TOOL is counts only and a kept outcome is an
// invitation to demote on it.
func readTools(stateRoot, room string, evidence *Evidence) Skip {
	if stateRoot == "" {
		return Skip{Stream: StreamTool, Reason: "no state root, so the seats' tool calls were not read"}
	}
	paths, _ := filepath.Glob(filepath.Join(stateRoot, "rooms", room, "seats", "*", "tool-calls.jsonl"))
	if len(paths) == 0 {
		return Skip{Stream: StreamTool, Reason: "no seat of " + room + " has a tool-calls file"}
	}
	for _, path := range paths {
		seat := filepath.Base(filepath.Dir(path))
		readJSONLines(path, func(raw []byte) {
			var line struct {
				At     time.Time `json:"at"`
				Target string    `json:"target"`
			}
			if json.Unmarshal(raw, &line) != nil || line.At.IsZero() || line.Target == "" {
				return
			}
			evidence.Facts = append(evidence.Facts, Fact{Stream: StreamTool, Value: line.Target,
				At: line.At, Ref: seat})
		})
	}
	return Skip{}
}

// readJSONLines feeds every non-empty line of path to each, and a missing file is nothing to read.
func readJSONLines(path string, each func([]byte)) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if line := scanner.Bytes(); len(line) > 0 {
			each(line)
		}
	}
}
