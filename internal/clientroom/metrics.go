// `room metrics`: an incremental, idempotent extractor that appends one flat JSON line per event to
// <state-dir>/rooms/<room>/metrics/<seat>.jsonl, so a room can be analysed with `jq` without re-reading transcripts.
//
// Sources, each read the way report.go reads it: the seats' transcripts (a `turn` per assistant message,
// classified by report.go's classifier), the seats' tool-calls.jsonl (a `tool` per call), `git log` of the
// repository the seats worked in (a `commit`), and the queue from the daemon (a `queue_row`).
//
// Redaction: a line holds ids, counts, hashes, lengths and classes. Never a command, a file's content, a queue body,
// an environment value or a token; a tool's target is reduced to its verb.
//
// Idempotence: <metrics>/.cursor.json holds, per transcript and tool-calls file, the byte offset already
// consumed (only whole lines), and the commit shas and queue ids already written. A second run appends nothing.
// `--print` writes the new lines to stdout and persists nothing, so it is safe on a live room.
package clientroom

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const metricsSchema = 1

const metricsUsage = `usage: 4genteam room metrics ROOM [--since 24h] [--seat S] [--print] [--state-root DIR] [--socket PATH]

Appends new events to <state-dir>/rooms/ROOM/metrics/<seat>.jsonl (mode 0600), one flat JSON object per line.
Incremental and idempotent: a cursor (metrics/.cursor.json) records what was read, so a second run appends nothing.
--since bounds the first read (events older than the window are skipped and not revisited); --seat limits the run
to one seat; --print writes the new lines to stdout and persists nothing.

Every line: ts (UTC ISO-8601), schema (1), room, seat, session (transcript id, null when unknown), event, plus
  turn       model tokens_in tokens_out cache_read cache_write cost_usd activity first_tool tools wall_ms task
  tool       tool target_class outcome ms blocked_rule bytes_out
  commit     sha files docs_only task
  queue_row  qitem priority tag summary_chars body_chars to
Activity is a turn's FIRST-tool class: code-edit doc-edit measuring-bash test/build queue-write queue-poll read
text-only other. The report scores code-edit by ANY call, so the two are deliberately different readings - the
report prints both (seats/architect/RULING-code-edit-share-target.md).
target_class is a command's verb (never the command line). task is a hub task id only where the turn or the
commit trailer names one, else null. Commands, contents, queue bodies and tokens are never written.

jq, with M=~/.config/4genthub/rooms/ROOM/metrics:
  cost per activity    jq -s '[.[]|select(.event=="turn")]|group_by(.activity)|map({activity:.[0].activity,usd:(map(.cost_usd)|add)})' $M/*.jsonl
  docs-only commits    jq -s '[.[]|select(.event=="commit")]|{commits:length,docs_only:(map(select(.docs_only))|length)}|.share=.docs_only/.commits' $M/*.jsonl
  rows/day by priority jq -s '[.[]|select(.event=="queue_row")]|group_by(.ts[:10]+" "+.priority)|map({day_priority:(.[0].ts[:10]+" "+.[0].priority),rows:length})' $M/*.jsonl
  blocked by rule      jq -s '[.[]|select(.event=="tool" and .blocked_rule!=null)]|group_by(.blocked_rule)|map({rule:.[0].blocked_rule,n:length})' $M/*.jsonl
  cost per task        jq -s '[.[]|select(.event=="turn" and .task!=null)]|group_by(.task)|map({task:.[0].task,usd:(map(.cost_usd)|add)})' $M/*.jsonl
  top expensive turns  jq -s '[.[]|select(.event=="turn")]|sort_by(-.cost_usd)|.[:10]|map({ts,seat,activity,cost_usd})' $M/*.jsonl`

// metricBase is the part every event shares; embedded structs flatten, so a line stays one flat object.
type metricBase struct {
	Ts      string  `json:"ts"`
	Schema  int     `json:"schema"`
	Room    string  `json:"room"`
	Seat    string  `json:"seat"`
	Session *string `json:"session"`
	Event   string  `json:"event"`
}

type turnEvent struct {
	metricBase
	Model      string  `json:"model"`
	TokensIn   int64   `json:"tokens_in"`
	TokensOut  int64   `json:"tokens_out"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	CostUSD    float64 `json:"cost_usd"`
	Activity   string  `json:"activity"`
	FirstTool  string  `json:"first_tool"`
	Tools      int     `json:"tools"`
	WallMS     int64   `json:"wall_ms"`
	Task       *string `json:"task"`
}

type toolEvent struct {
	metricBase
	Tool        string  `json:"tool"`
	TargetClass string  `json:"target_class"`
	Outcome     string  `json:"outcome"`
	MS          *int64  `json:"ms"`
	BlockedRule *string `json:"blocked_rule"`
	BytesOut    *int64  `json:"bytes_out"`
}

type commitEvent struct {
	metricBase
	SHA      string  `json:"sha"`
	Files    int     `json:"files"`
	DocsOnly bool    `json:"docs_only"`
	Task     *string `json:"task"`
}

type queueRowEvent struct {
	metricBase
	Qitem        string `json:"qitem"`
	Priority     string `json:"priority"`
	Tag          string `json:"tag"`
	SummaryChars int    `json:"summary_chars"`
	BodyChars    int    `json:"body_chars"`
	To           string `json:"to"`
}

// metricsCursor is what a run has already consumed.
type metricsCursor struct {
	Offsets map[string]int64 `json:"offsets"` // "<seat>/<file relative to the seat dir>" -> bytes consumed
	Commits map[string]bool  `json:"commits"`
	Queue   map[string]bool  `json:"queue"`
}

func loadMetricsCursor(path string) (*metricsCursor, error) {
	cursor := &metricsCursor{}
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, cursor)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if cursor.Offsets == nil {
		cursor.Offsets = map[string]int64{}
	}
	if cursor.Commits == nil {
		cursor.Commits = map[string]bool{}
	}
	if cursor.Queue == nil {
		cursor.Queue = map[string]bool{}
	}
	return cursor, nil
}

// readNewLines calls fn with every COMPLETE line of path after offset and returns the new offset. A trailing
// partial line (a writer is mid-write) is left for the next run; a file shorter than the offset starts over.
func readNewLines(path string, offset int64, fn func([]byte)) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return offset, nil
		}
		return offset, err
	}
	if int64(len(raw)) < offset {
		offset = 0
	}
	rest := raw[offset:]
	end := strings.LastIndexByte(string(rest), '\n')
	if end < 0 {
		return offset, nil
	}
	for _, line := range strings.Split(string(rest[:end]), "\n") {
		if strings.TrimSpace(line) != "" {
			fn([]byte(line))
		}
	}
	return offset + int64(end) + 1, nil
}

var verbedCommands = map[string]bool{"git": true, "4genteam": true, "go": true, "cargo": true, "npm": true, "docker": true, "tmux": true}
var (
	wordRe = regexp.MustCompile(`^[A-Za-z0-9_.+-]+$`)
	verbRe = regexp.MustCompile(`^[a-z][a-z-]*$`)
)

// targetClass reduces a tool call's target to a class that cannot hold a secret: a bash command to its verb
// (`git commit`, `go`, `grep`), an MCP call to its tool.action, a file tool to doc or code.
func targetClass(tool, target string) string {
	switch {
	case target == "":
		return ""
	case strings.HasPrefix(tool, "mcp__"):
		return target
	case tool == "bash":
		words := strings.Fields(leadingCdRe.ReplaceAllString(strings.TrimSpace(target), ""))
		if len(words) == 0 {
			return ""
		}
		verb := filepath.Base(words[0])
		if !wordRe.MatchString(verb) {
			return "other"
		}
		if verbedCommands[verb] && len(words) > 1 && verbRe.MatchString(words[1]) {
			verb += " " + words[1]
		}
		return verb
	case docPath(target):
		return "doc"
	}
	return "code"
}

// toolLine is one tool-calls.jsonl line.
type toolLine struct {
	At          string  `json:"at"`
	Session     string  `json:"session"`
	Tool        string  `json:"tool"`
	Target      string  `json:"target"`
	TargetClass *string `json:"target_class"`
	Outcome     string  `json:"outcome"`
	MS          *int64  `json:"ms"`
	Rule        string  `json:"rule"`
	BlockedRule string  `json:"blocked_rule"`
	BytesOut    *int64  `json:"bytes_out"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// metricsRun is one extraction: what it appends, per seat, as encoded lines.
type metricsRun struct {
	room    string
	roomDir string
	cutoff  time.Time
	cursor  *metricsCursor
	lines   map[string][][]byte
}

func (r *metricsRun) emit(seat string, event any) {
	encoded, _ := json.Marshal(event)
	r.lines[seat] = append(r.lines[seat], encoded)
}

func (r *metricsRun) base(ts time.Time, seat, session, event string) metricBase {
	return metricBase{Ts: ts.UTC().Format(time.RFC3339Nano), Schema: metricsSchema, Room: r.room, Seat: seat, Session: strPtr(session), Event: event}
}

// readTurns emits one turn per assistant message of a transcript at or after the cutoff.
func (r *metricsRun) readTurns(seat, key, path string) error {
	session := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if i := strings.LastIndex(session, "_"); i >= 0 {
		session = session[i+1:]
	}
	next, err := readNewLines(path, r.cursor.Offsets[key], func(raw []byte) {
		var line struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role     string          `json:"role"`
				Model    string          `json:"model"`
				Content  json.RawMessage `json:"content"`
				Duration float64         `json:"duration"`
				Usage    struct {
					Input, Output, CacheRead, CacheWrite int64
					Cost                                 struct {
						Total float64 `json:"total"`
					} `json:"cost"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &line) != nil || line.Type != "message" || line.Message.Role != "assistant" {
			return
		}
		at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
		if err != nil || at.Before(r.cutoff) {
			return
		}
		usage := line.Message.Usage
		turn := turnEvent{metricBase: r.base(at, seat, session, "turn"), Model: line.Message.Model,
			TokensIn: usage.Input, TokensOut: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite,
			CostUSD: usage.Cost.Total, Activity: firstToolClass(line.Message.Content), WallMS: int64(line.Message.Duration)}
		var parts []contentPart
		_ = json.Unmarshal(line.Message.Content, &parts)
		for _, p := range parts {
			if p.Type != "toolCall" {
				continue
			}
			if turn.Tools == 0 {
				turn.FirstTool = p.Name
			}
			turn.Tools++
			if id, _ := p.Arguments["task_id"].(string); turn.Task == nil && taskIDPattern.MatchString(strings.ToLower(id)) {
				id = strings.ToLower(id)
				turn.Task = &id
			}
		}
		r.emit(seat, turn)
	})
	r.cursor.Offsets[key] = next
	return err
}

// readTools emits one tool per tool-calls.jsonl line at or after the cutoff.
func (r *metricsRun) readTools(seat, key, path string) error {
	next, err := readNewLines(path, r.cursor.Offsets[key], func(raw []byte) {
		var line toolLine
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		at, err := time.Parse(time.RFC3339Nano, line.At)
		if err != nil || at.Before(r.cutoff) {
			return
		}
		class := ""
		if line.TargetClass != nil {
			class = *line.TargetClass
		} else {
			class = targetClass(line.Tool, line.Target)
		}
		rule := line.BlockedRule
		if rule == "" && line.Outcome == "blocked" {
			rule = line.Rule
		}
		r.emit(seat, toolEvent{metricBase: r.base(at, seat, line.Session, "tool"), Tool: line.Tool, TargetClass: class,
			Outcome: line.Outcome, MS: line.MS, BlockedRule: strPtr(rule), BytesOut: line.BytesOut})
	})
	r.cursor.Offsets[key] = next
	return err
}

func (r *metricsRun) addCommits(commits []commitRecord, only string) {
	for _, c := range commits {
		seat := c.Seat
		if seat == "" {
			seat = "other"
		}
		if r.cursor.Commits[c.SHA] || c.Time.Before(r.cutoff) || (only != "" && seat != only) {
			continue
		}
		r.cursor.Commits[c.SHA] = true
		r.emit(seat, commitEvent{metricBase: r.base(c.Time, seat, "", "commit"), SHA: c.SHA, Files: c.Files, DocsOnly: c.DocsOnly, Task: strPtr(c.TaskID)})
	}
}

// addQueueRows emits a queue_row per row created at or after the cutoff; the body is measured, never kept.
func (r *metricsRun) addQueueRows(rows []clientruntime.QueueRow, only string) {
	prefix := "agenthub--" + r.room + "--"
	for _, row := range rows {
		created, err := time.Parse(time.RFC3339Nano, row.TsCreated)
		seat := "other"
		if name, ok := strings.CutPrefix(row.SourceSession, prefix); ok {
			seat = name
		}
		if err != nil || created.Before(r.cutoff) || r.cursor.Queue[row.QitemID] || (only != "" && seat != only) {
			continue
		}
		r.cursor.Queue[row.QitemID] = true
		to := strings.TrimPrefix(row.DestinationSession, prefix)
		r.emit(seat, queueRowEvent{metricBase: r.base(created, seat, "", "queue_row"), Qitem: row.QitemID, Priority: row.Priority,
			Tag: strings.Join(row.Tags, ","), SummaryChars: utf8.RuneCountInString(row.Summary), BodyChars: utf8.RuneCountInString(row.Body), To: to})
	}
}

// extractMetrics reads every source once and returns the run holding the new lines and the advanced cursor.
// A source that cannot be read goes in warnings; the others are still written.
func extractMetrics(room, roomDir, only string, cutoff time.Time, cursor *metricsCursor,
	queueRows func() ([]clientruntime.QueueRow, error)) (*metricsRun, []string, error) {
	run := &metricsRun{room: room, roomDir: roomDir, cutoff: cutoff, cursor: cursor, lines: map[string][][]byte{}}
	seatDirs, err := filepath.Glob(filepath.Join(roomDir, "seats", "*"))
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	var cwds []string
	for _, dir := range seatDirs {
		seat := filepath.Base(dir)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() || (only != "" && seat != only) {
			continue
		}
		transcripts, _ := filepath.Glob(filepath.Join(dir, "sessions", "*.jsonl"))
		for _, path := range transcripts {
			if err := run.readTurns(seat, seat+"/"+strings.TrimPrefix(path, dir+"/"), path); err != nil {
				return nil, nil, fmt.Errorf("read %s: %w", path, err)
			}
			_ = forEachLine(path, func(raw []byte) {
				var head struct{ Type, Cwd string }
				if json.Unmarshal(raw, &head) == nil && head.Type == "session" && head.Cwd != "" {
					cwds = append(cwds, head.Cwd)
				}
			})
		}
		if err := run.readTools(seat, seat+"/tool-calls.jsonl", filepath.Join(dir, "tool-calls.jsonl")); err != nil {
			return nil, nil, fmt.Errorf("read %s tool calls: %w", seat, err)
		}
	}
	if repo := mostCommon(cwds); repo == "" {
		warnings = append(warnings, "commits skipped: no session names a working directory")
	} else if commits, err := gitCommits(repo, room, cutoff); err != nil {
		warnings = append(warnings, "commits skipped: "+err.Error())
	} else {
		run.addCommits(commits, only)
	}
	if rows, err := queueRows(); err != nil {
		warnings = append(warnings, "queue rows skipped: "+err.Error())
	} else {
		run.addQueueRows(rows, only)
	}
	return run, warnings, nil
}

// persist appends the run's lines to the per-seat files (0600) and then saves the cursor, so a crash in between
// can repeat lines but never lose them.
func (r *metricsRun) persist(metricsDir string) error {
	if err := os.MkdirAll(metricsDir, 0o700); err != nil {
		return err
	}
	for seat, lines := range r.lines {
		file, err := os.OpenFile(filepath.Join(metricsDir, seat+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = file.Write(joinLines(lines))
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	encoded, _ := json.Marshal(r.cursor)
	cursorPath := filepath.Join(metricsDir, ".cursor.json")
	tmp := cursorPath + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, cursorPath)
}

func joinLines(lines [][]byte) []byte {
	var out []byte
	for _, line := range lines {
		out = append(append(out, line...), '\n')
	}
	return out
}

func (r *metricsRun) total() int {
	n := 0
	for _, lines := range r.lines {
		n += len(lines)
	}
	return n
}

func runMetrics(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, metricsUsage)
		return clientcmd.ExitOK
	}
	rootFlag, args, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam room metrics: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitRoomRefused)
	}
	opts, rest, err := clientruntime.SplitFlags(args, queueFlagSpecs(
		clientruntime.FlagSpec{Name: "since", Value: true}, clientruntime.FlagSpec{Name: "seat", Value: true}, clientruntime.FlagSpec{Name: "print"}))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room metrics: %v. %s\n", err, metricsUsage)
		return clientcmd.ExitRoomRefused
	}
	since := "24h"
	if v, ok := opts["since"]; ok {
		since = v
	}
	window, err := parseSince(since)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room metrics: %v. %s\n", err, metricsUsage)
		return clientcmd.ExitRoomRefused
	}
	pairArgs := append(append(make([]string, 0, len(rest)+4), rest...), "--state-root", rootFlag, "--socket", opts["socket"])
	inv, code, ok := parseInvocation("metrics", pairArgs, 1, stdout, stderr)
	if !ok {
		return code
	}
	roomDir := filepath.Join(inv.stateDir, "rooms", inv.room)
	if info, err := os.Stat(roomDir); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "4genteam room metrics: no room directory at %s\n", roomDir)
		return clientcmd.ExitRoomRefused
	}
	metricsDir := filepath.Join(roomDir, "metrics")
	cursor, err := loadMetricsCursor(filepath.Join(metricsDir, ".cursor.json"))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room metrics: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	run, warnings, err := extractMetrics(inv.room, roomDir, opts["seat"], time.Now().UTC().Add(-window), cursor,
		func() ([]clientruntime.QueueRow, error) {
			return inv.client.QueueList(inv.room, clientruntime.QueueListFilter{})
		})
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room metrics: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "4genteam room metrics: %s\n", warning)
	}
	if _, printOnly := opts["print"]; printOnly {
		seats := make([]string, 0, len(run.lines))
		for seat := range run.lines {
			seats = append(seats, seat)
		}
		sort.Strings(seats)
		for _, seat := range seats {
			_, _ = stdout.Write(joinLines(run.lines[seat]))
		}
		return clientcmd.ExitOK
	}
	if err := run.persist(metricsDir); err != nil {
		fmt.Fprintf(stderr, "4genteam room metrics: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	fmt.Fprintf(stdout, "metrics: %d new lines in %s\n", run.total(), metricsDir)
	return clientcmd.ExitOK
}
