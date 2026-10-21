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
	Facts   []Fact
	Rows    []RailRow
	Skipped []string
}

// LoadEvidence reads the streams for one room. stateRoot is the client's state root (`clientenv.ResolveStateRoot`);
// repo is a checkout whose git dir carries the refusal spool - the tree the census reads, since that is where
// a refused commit would have been made. A read that fails is recorded in Skipped, never returned as an error:
// one unreadable stream narrows what the census knows, and the report says so.
func LoadEvidence(stateRoot, room, repo string) Evidence {
	var evidence Evidence

	evidence.add(readGuard(repo, &evidence))
	evidence.add(readSends(stateRoot, room, &evidence))
	evidence.add(readJev(stateRoot, &evidence))
	evidence.add(readTools(stateRoot, room, &evidence))

	sort.SliceStable(evidence.Facts, func(i, j int) bool { return evidence.Facts[i].At.Before(evidence.Facts[j].At) })
	return evidence
}

// add keeps a stream's note when it had one.
func (e *Evidence) add(note string) {
	if note != "" {
		e.Skipped = append(e.Skipped, note)
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

// Landed counts the room's rows that reached `done` at or after since. A row is an item; the queue is the
// only record of one landing, because a landing IS the gate's closure.
func (e Evidence) Landed(since time.Time) int {
	count := 0
	for _, row := range e.Rows {
		if row.State == "done" && !row.Updated.Before(since) {
			count++
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

// readGuard reads <git dir>/parked-diffs/refusals.jsonl - the spool a refused commit is written to, one line
// per refusal. The rule is the field a `guard:` signal names (`docs-only`, `protected-path`).
func readGuard(repo string, evidence *Evidence) string {
	if repo == "" {
		return "no checkout, so the guard's refusals were not read"
	}
	path, err := refusalSpoolPath(repo)
	if err != nil {
		return fmt.Sprintf("the guard's refusal spool was not read: %v", err)
	}
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
		evidence.Facts = append(evidence.Facts, Fact{Stream: StreamGuard, Value: line.Rule, At: at, Ref: line.ID})
	})
	return ""
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
func readSends(stateRoot, room string, evidence *Evidence) string {
	if stateRoot == "" {
		return "no state root, so the send audit was not read"
	}
	paths, _ := filepath.Glob(filepath.Join(stateRoot, "seats", room, "*", "audit.jsonl"))
	if len(paths) == 0 {
		return ""
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
	return ""
}

// readJev reads the machine-wide Jev decision log and keeps every decision's `kind`. The log is not per room,
// so a `jev:` signal names a kind any seat's escalation of that shape produces.
func readJev(stateRoot string, evidence *Evidence) string {
	if stateRoot == "" {
		return "no state root, so the Jev decisions were not read"
	}
	path := filepath.Join(stateRoot, jevLogFile)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	readJSONLines(path, func(raw []byte) {
		var line struct {
			Timestamp time.Time `json:"timestamp"`
			Kind      string    `json:"kind"`
			ID        string    `json:"id"`
		}
		if json.Unmarshal(raw, &line) != nil || line.Kind == "" || line.Timestamp.IsZero() {
			return
		}
		evidence.Facts = append(evidence.Facts, Fact{Stream: StreamJEV, Value: line.Kind,
			At: line.Timestamp, Ref: line.ID})
	})
	return ""
}

// jevLogFile is the log clientjev writes.
const jevLogFile = "jev_decisions.log"

// readTools reads each seat's tool-calls file. Every line becomes a TOOL fact whose Value is the target a
// `command:` signal matches; the outcome is NOT kept, because TOOL is counts only and a kept outcome is an
// invitation to demote on it.
func readTools(stateRoot, room string, evidence *Evidence) string {
	if stateRoot == "" {
		return "no state root, so the seats' tool calls were not read"
	}
	paths, _ := filepath.Glob(filepath.Join(stateRoot, "rooms", room, "seats", "*", "tool-calls.jsonl"))
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
	return ""
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
