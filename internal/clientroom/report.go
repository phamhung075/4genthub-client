// `room report`: one read-only measurement of one room over a time window.
//
// The report exists so the targets below are read off the room's own records instead of an ad-hoc script.
// Five sources feed it, and each is read the way its owner exposes it:
//
//   - the seats' session transcripts (<state-dir>/rooms/<room>/seats/<seat>/sessions/*.jsonl): every
//     assistant message carries its cost and is classified by its FIRST tool call, and as code-edit when ANY
//     of its calls edits code - both readings are kept (see `turnClasses`);
//   - the seats' tool-calls.jsonl: how many calls the seat's guard blocked;
//   - the room's queue, asked from the runtime daemon (QueueList) - this client links no SQLite driver and
//     the daemon is the queue store's only owner. Only counts and lengths are used: a body is never printed;
//   - `git log --since` of the repository the seats worked in, where a commit's `Seat:` trailer names the
//     seat that made it (a commit without one is counted as "other");
//   - the room's spec.json: which seats are `developer` seats, i.e. which of them the code-edit target holds
//     (seats/architect/RULING-code-edit-share-target.md).
//
// A source that cannot be read is printed as `unavailable (<reason>)`; it is never a zero. Nothing here
// writes anything.
package clientroom

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

const reportUsage = `usage: 4genteam room report ROOM [--since 24h] [--json] [--state-root DIR] [--socket PATH]

One read-only measurement of one room over one window; it writes nothing. The numbers come from the room's own
records rather than an ad-hoc script, so the targets below are read the same way twice, and a source that
cannot be read prints "unavailable (<reason>)" instead of a zero.

Five sources: the seats' session transcripts (every assistant turn's cost, classified by the FIRST tool call,
and by ANY call for the code-edit class) and tool-calls.jsonl (the guard-blocked count); the room's queue, asked
from the runtime daemon - counts and lengths only, never a body; the room's spec.json, which says which seats
are developer seats and so which carry the code-edit target; and git log --since of the repository the seats'
session headers name most often, where a commit's Seat: trailer names the seat that made it, Task: names a hub
task, and a commit whose every file is documentation counts as docs-only.

The window is --since: 24h by default, and 90m or 7d are read the same way. It is LIVE - two runs minutes
apart answer differently - and --json prints the same answer as one object.

Per seat and for the room: USD cost, turns, commits, docs-only share, queue rows, high-priority share, the
guard-blocked count, and the cost share across the nine activity classes room metrics records.

The targets, each PASS, FAIL or n/a: docs-only commits % < 15, queue rows/day < 300, high-priority rows % < 20,
measuring-bash cost % < 25 - all four on the room; then code-edit cost % against the floor, for each EDITING
seat (the room spec's developer seats), each on its own line so one seat cannot hide behind the other. The
floor RATCHETS: 25, +5 for every complete UTC day on which every editing seat passes, never down, cap 40, so
the line reads "> 25, cap 40". The step is replayed from the transcripts under the same any-call reading the
floor gates; nothing is written. The room's own code-edit share is printed as a DIAGNOSTIC with no PASS/FAIL:
three of five seats may not edit code and hold more than half the cost, so a room-wide floor measures the role
split, not the effort (seats/architect/RULING-code-edit-share-target.md).

Paired with the floor, one DIAGNOSTIC line per EDITING seat: the edit yield - the commits the gate PASSED whose
Seat: trailer names that seat, over that seat's code-edit USD. A commit counts when its Task: names a task a
queue row tagged gate, closed by the reviewer, names in its summary or body; a task the gate passed for another
seat credits nobody, and a commit that only moves a submodule pin never counts. The line prints its inputs and
the commits the join dropped, by reason; its own floor waits for a ruling after five daily windows.

A sixth check this verb adds is not one of the five: tasks closed after a commit that cites them, wanted 0.
Below them: the tasks open more than 24h, and the commits that carry no Task: trailer.`

// The activity classes a turn falls into.
const (
	actCodeEdit  = "code-edit"
	actDocEdit   = "doc-edit"
	actMeasuring = "measuring-bash"
	actTestBuild = "test/build"
	actQueueWr   = "queue-write"
	actQueuePoll = "queue-poll"
	actRead      = "read"
	actTextOnly  = "text-only"
	actOther     = "other"
)

var activityOrder = []string{actCodeEdit, actDocEdit, actMeasuring, actTestBuild, actQueueWr, actQueuePoll, actRead, actTextOnly, actOther}

// The targets, as the plan's section 2.7 states them - except code-edit, which
// seats/architect/RULING-code-edit-share-target.md moved from a room-wide floor to a per-editing-seat floor.
// The 30% room target cannot be met here by construction: the seats that may not edit code hold 55.1% of the
// room's cost, so the room share caps at 44.9% even if both developers spent every dollar editing.
const (
	targetDocsOnlyMax  = 15.0
	targetRowsPerDay   = 300.0
	targetHighMax      = 20.0
	targetMeasuringMax = 25.0
)

// The code-edit floor ratchets (seats/architect/RULING-code-edit-share-target.md, Addendum 3): it starts at
// 25, gains a step for every complete UTC day on which EVERY editing seat passes, never falls, and stops at
// the ceiling 40 - which was a flat target once, and a 2.1x floor prints a permanent FAIL. A failing seat
// holds the floor: the step needs all of them.
const (
	codeEditFloorBase = 25.0
	codeEditFloorStep = 5.0
	codeEditCeil      = 40.0
	// codeEditFloorDays is how many complete days the floor replays. The step is derived from existing records
	// - the transcripts already read for the window - rather than stored, because the report writes nothing.
	codeEditFloorDays = 7
)

// yieldBaselineDays is how many daily windows the edit-yield ratio runs as a diagnostic before a ruling sets
// its floor; until then it is printed and never gated.
const yieldBaselineDays = 5

// staleTaskAge is how long a todo or in_progress task may stay open before it is listed.
const staleTaskAge = 24 * time.Hour

var (
	queueWriteRe = regexp.MustCompile(`room queue (create|update|claim|close)\b`)
	queuePollRe  = regexp.MustCompile(`room queue (list|show)\b`)
	testBuildRe  = regexp.MustCompile(`\b(go (test|build|vet)|cargo (test|build|check|clippy)|npm (run|test)|npx |tsc\b|pytest|build\.sh|make\b|vite build)`)
	measuringRe  = regexp.MustCompile(`^(grep|rg|egrep|find|wc|sha\d*sum|shasum|git (log|rev-parse|rev-list|diff|show|status|ls-tree|ls-files|blame)|ls|head|tail|cat|stat|du|diff|cmp|awk|sed|sort|uniq)\b`)
	// leadingCdRe strips what a seat puts in front of the command that does the work: cd, export, VAR=value
	// and an echo banner, each ended by && or ;.
	leadingCdRe = regexp.MustCompile(`^((cd|export|echo)\s+("[^"]*"|'[^']*'|[^;&]*?)\s*(&&|;)\s*|[A-Z_][A-Z0-9_]*=\S*\s+)+`)
)

// docPath is true for the paths the plan counts as documentation.
func docPath(path string) bool {
	return strings.HasSuffix(path, ".md") || strings.Contains(path, "CHANGELOG") ||
		strings.Contains(path, "NEXT-GEN") || strings.Contains(path, "ai_docs/")
}

// classifyBash puts one shell command in an activity class. Queue verbs are matched anywhere in the command,
// because a seat chains them (`cd X && 4genteam room queue create ...`); the rest by the first command word.
func classifyBash(command string) string {
	switch {
	case queueWriteRe.MatchString(command):
		return actQueueWr
	case queuePollRe.MatchString(command):
		return actQueuePoll
	}
	first := leadingCdRe.ReplaceAllString(strings.TrimSpace(command), "")
	switch {
	case testBuildRe.MatchString(first):
		return actTestBuild
	case measuringRe.MatchString(first):
		return actMeasuring
	}
	return actOther
}

// classifyToolCall is one tool call's class, by tool name and the arguments that decide it.
func classifyToolCall(name string, args map[string]any) string {
	switch name {
	case "bash":
		command, _ := args["command"].(string)
		return classifyBash(command)
	case "edit", "write":
		path, _ := args["path"].(string)
		if docPath(path) {
			return actDocEdit
		}
		return actCodeEdit
	case "read":
		return actRead
	case "grep", "glob", "find":
		return actMeasuring
	}
	return actOther
}

// seatStats is everything measured for one seat (or, summed, for the room).
type seatStats struct {
	Seat         string             `json:"seat"`
	Turns        int                `json:"turns"`
	Cost         float64            `json:"cost_usd"`
	OutputTokens int64              `json:"output_tokens"`
	ActivityCost map[string]float64 `json:"cost_by_activity_usd"`
	ActivityPct  map[string]float64 `json:"cost_by_activity_pct"`
	// CodeEditFirstCallPct is the SAME cost share under the OLD definition - a turn scored by its first tool
	// call only - kept beside the new one so the two readings stay comparable for a window before the old
	// target is retired (seats/architect/RULING-code-edit-share-target.md). Room metrics keeps persisting the
	// first-call reading per turn, so this field and the log agree.
	CodeEditFirstCallPct float64 `json:"code_edit_first_call_pct"`
	Blocked              int     `json:"guard_blocked_calls"`
	Commits              int     `json:"commits"`
	DocsOnly             int     `json:"docs_only_commits"`
	QueueRows            int     `json:"queue_rows"`
	QueueHigh            int     `json:"queue_high_rows"`
	BodyChars            int     `json:"queue_body_chars"`

	codeEditFirstCallCost float64
}

func newSeatStats(seat string) *seatStats {
	return &seatStats{Seat: seat, ActivityCost: map[string]float64{}, ActivityPct: map[string]float64{}}
}

func (s *seatStats) add(o *seatStats) {
	s.Turns += o.Turns
	s.Cost += o.Cost
	s.OutputTokens += o.OutputTokens
	for k, v := range o.ActivityCost {
		s.ActivityCost[k] += v
	}
	s.codeEditFirstCallCost += o.codeEditFirstCallCost
	s.Blocked += o.Blocked
	s.Commits += o.Commits
	s.DocsOnly += o.DocsOnly
	s.QueueRows += o.QueueRows
	s.QueueHigh += o.QueueHigh
	s.BodyChars += o.BodyChars
}

func (s *seatStats) finish() {
	for k, v := range s.ActivityCost {
		if s.Cost > 0 {
			s.ActivityPct[k] = 100 * v / s.Cost
		}
	}
	s.CodeEditFirstCallPct = 0
	if s.Cost > 0 {
		s.CodeEditFirstCallPct = 100 * s.codeEditFirstCallCost / s.Cost
	}
}

func (s *seatStats) docsOnlyPct() float64  { return pct(s.DocsOnly, s.Commits) }
func (s *seatStats) highPct() float64      { return pct(s.QueueHigh, s.QueueRows) }
func (s *seatStats) avgBodyChars() float64 { return ratio(s.BodyChars, s.QueueRows) }

func pct(part, whole int) float64 { return 100 * ratio(part, whole) }
func ratio(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// sessionLine is the slice of a transcript line the report reads.
type sessionLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			Output int64 `json:"output"`
			Cost   struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}

type contentPart struct {
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// firstToolClass is the class of a message's first tool call, or text-only when it made none.
func firstToolClass(raw json.RawMessage) string {
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return actTextOnly
	}
	for _, p := range parts {
		if p.Type == "toolCall" {
			return classifyToolCall(p.Name, p.Arguments)
		}
	}
	return actTextOnly
}

// turnClasses is one assistant turn's TWO readings, because the two answer different questions and the
// metric moved from one to the other (seats/architect/RULING-code-edit-share-target.md).
//
// The first return is the first tool call's class - the reading the report and room metrics used, and the one
// `metrics.go` still persists per turn. It under-counts editing by construction: every edit is preceded by a
// read, so a turn that read two files and then edited code was scored `read`.
//
// The second return is the class the report now uses: `code-edit` when ANY call in the turn edits code, else
// the first call's class. Only the code-edit class follows the any-call rule - a turn that reads and then
// writes a CHANGELOG is still scored by its first call, because the ruling's defect is the code-edit share and
// widening every class at once would move numbers nobody asked to move.
func turnClasses(raw json.RawMessage) (first, primary string) {
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return actTextOnly, actTextOnly
	}
	edits := false
	for _, p := range parts {
		if p.Type != "toolCall" {
			continue
		}
		class := classifyToolCall(p.Name, p.Arguments)
		if first == "" {
			first = class
		}
		if class == actCodeEdit {
			edits = true
		}
	}
	if first == "" {
		return actTextOnly, actTextOnly
	}
	if edits {
		return first, actCodeEdit
	}
	return first, first
}

// forEachLine calls fn with every non-blank line of a JSONL file; a line that is not JSON is the caller's to skip.
func forEachLine(path string, fn func([]byte)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// daySplit is one UTC day's code-edit cost against the seat's whole cost: the material the floor's ratchet
// replays. Both come from the same any-call reading the floor gates, so a day cannot pass under one
// classifier and fail under another.
type daySplit struct {
	CodeEdit float64
	Total    float64
}

// seatDays is one seat's days, keyed by UTC date.
type seatDays map[string]daySplit

// readSessions adds one seat's assistant turns at or after cutoff to stats, and returns the working
// directories its session headers named, for locating the repository, plus the same turns split by UTC day -
// read regardless of cutoff, because the floor replays recent days and the window may be shorter.
func readSessions(seatDir string, cutoff time.Time, stats *seatStats) ([]string, seatDays, error) {
	files, err := filepath.Glob(filepath.Join(seatDir, "sessions", "*.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	var cwds []string
	days := seatDays{}
	for _, file := range files {
		err := forEachLine(file, func(raw []byte) {
			var line sessionLine
			if json.Unmarshal(raw, &line) != nil {
				return
			}
			if line.Type == "session" && line.Cwd != "" {
				cwds = append(cwds, line.Cwd)
			}
			if line.Type != "message" || line.Message.Role != "assistant" {
				return
			}
			at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
			if err != nil {
				return
			}
			cost := line.Message.Usage.Cost.Total
			first, primary := turnClasses(line.Message.Content)
			key := at.UTC().Format("2006-01-02")
			split := days[key]
			split.Total += cost
			if primary == actCodeEdit {
				split.CodeEdit += cost
			}
			days[key] = split
			if at.Before(cutoff) {
				return
			}
			stats.Turns++
			stats.Cost += cost
			stats.OutputTokens += line.Message.Usage.Output
			stats.ActivityCost[primary] += cost
			if first == actCodeEdit {
				stats.codeEditFirstCallCost += cost
			}
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return cwds, days, nil
}

// readBlocked counts the guard-blocked calls a seat's tool-calls.jsonl recorded at or after cutoff.
func readBlocked(seatDir string, cutoff time.Time) int {
	var blocked int
	_ = forEachLine(filepath.Join(seatDir, "tool-calls.jsonl"), func(raw []byte) {
		var call struct {
			At      string `json:"at"`
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(raw, &call) != nil || call.Outcome != "blocked" {
			return
		}
		if at, err := time.Parse(time.RFC3339Nano, call.At); err == nil && !at.Before(cutoff) {
			blocked++
		}
	})
	return blocked
}

// commitRecord is one commit: the seat its `Seat:` trailer names ("" when none), whether every file it
// touched is documentation, and whether every file it touched is a submodule gitlink - a pin-only commit,
// which landed nothing of the seat's own work and never counts as a gate-passed commit.
type commitRecord struct {
	Seat     string
	DocsOnly bool
	PinOnly  bool
	SHA      string
	Title    string
	TaskID   string // the hub task its `Task:` trailer names, "" when none
	Time     time.Time
	Files    int
}

var taskIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	recordSep = "\x1e"
	fieldSep  = "\x1f"
)

// gitCommits reads `git log --since` of repo and returns one record per commit. Seat trailers have the form
// `agenthub--<room>--<seat>`; only a trailer naming this room's prefix is mapped to a seat. --raw names each
// touched file's mode, which is how a pin-only commit (mode 160000) is told from a real one.
func gitCommits(repo, room string, cutoff time.Time) ([]commitRecord, error) {
	out, err := exec.Command("git", "-C", repo, "log", "--since="+cutoff.Format(time.RFC3339), "--raw",
		"--format="+recordSep+"%H"+fieldSep+"%s"+fieldSep+"%(trailers:key=Seat,valueonly,separator=%x2c)"+fieldSep+
			"%(trailers:key=Task,valueonly,separator=%x2c)"+fieldSep+"%cI"+fieldSep).Output()
	if err != nil {
		return nil, fmt.Errorf("git log in %s: %w", repo, err)
	}
	return parseGitLog(string(out), room), nil
}

// submoduleMode is the file mode git prints for a submodule pointer: the commit touched the gitlink, not code.
const submoduleMode = "160000"

func parseGitLog(out, room string) []commitRecord {
	prefix := "agenthub--" + room + "--"
	var commits []commitRecord
	for _, record := range strings.Split(out, recordSep)[1:] {
		fields := strings.SplitN(record, fieldSep, 6)
		for len(fields) < 6 {
			fields = append(fields, "")
		}
		rec := commitRecord{SHA: fields[0], Title: fields[1]}
		files := fields[5]
		rec.Time, _ = time.Parse(time.RFC3339, fields[4])
		if seat, ok := strings.CutPrefix(strings.TrimSpace(fields[2]), prefix); ok {
			rec.Seat = seat
		}
		for _, id := range strings.Split(fields[3], ",") {
			if id = strings.ToLower(strings.TrimSpace(id)); taskIDPattern.MatchString(id) {
				rec.TaskID = id
				break
			}
		}
		touched, allDocs, allPins := false, true, true
		for _, line := range strings.Split(files, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			meta, path, ok := strings.Cut(line, "\t")
			if !ok {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(meta, ":"))
			mode := ""
			if len(fields) > 1 {
				mode = fields[1]
			}
			touched = true
			rec.Files++
			allDocs = allDocs && docPath(path)
			allPins = allPins && mode == submoduleMode
		}
		rec.DocsOnly = touched && allDocs
		rec.PinOnly = touched && allPins
		commits = append(commits, rec)
	}
	return commits
}

// mostCommon returns the value that occurs most often (ties broken by name), or "".
func mostCommon(values []string) string {
	counts := map[string]int{}
	for _, v := range values {
		counts[v]++
	}
	best, bestN := "", 0
	for v, n := range counts {
		if n > bestN || (n == bestN && v < best) {
			best, bestN = v, n
		}
	}
	return best
}

// applyQueue adds the rows created at or after cutoff to the seat that sourced them (a source that is not a
// seat of this room goes to "other"). Only counts and the body's length are taken; the body is not kept.
func applyQueue(rows []clientruntime.QueueRow, room string, cutoff time.Time, bySeat map[string]*seatStats) {
	prefix := "agenthub--" + room + "--"
	for _, row := range rows {
		created, err := time.Parse(time.RFC3339Nano, row.TsCreated)
		if err != nil || created.Before(cutoff) {
			continue
		}
		seat := "other"
		if name, ok := strings.CutPrefix(row.SourceSession, prefix); ok {
			seat = name
		}
		stats := seatFor(bySeat, seat)
		stats.QueueRows++
		stats.BodyChars += utf8.RuneCountInString(row.Body)
		if row.Priority == "high" {
			stats.QueueHigh++
		}
	}
}

func seatFor(bySeat map[string]*seatStats, seat string) *seatStats {
	if bySeat[seat] == nil {
		bySeat[seat] = newSeatStats(seat)
	}
	return bySeat[seat]
}

// staleTask is a hub task still open more than staleTaskAge after it was created. The age counts from
// created_at: the hub's lifecycle sweeper rewrites updated_at when it escalates a task, so updated_at cannot
// show that nobody worked on it.
type staleTask struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// findStaleTasks lists the todo and in_progress tasks created more than staleTaskAge ago, oldest first.
// list returns the hub's list answer for one status (ids only, the list is minimal); get returns one task's answer.
func findStaleTasks(list func(status string) (map[string]any, error), get func(id string) (map[string]any, error), now time.Time) ([]staleTask, error) {
	var stale []staleTask
	for _, status := range []string{"todo", "in_progress"} {
		body, err := list(status)
		if err != nil {
			return nil, err
		}
		tasks, _ := body["tasks"].([]any)
		for _, entry := range tasks {
			listed, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			id, _ := listed["id"].(string)
			answer, err := get(id)
			if err != nil {
				return nil, err
			}
			task, _ := answer["task"].(map[string]any)
			created, _ := task["created_at"].(string)
			at, err := time.Parse(time.RFC3339Nano, created)
			if err != nil {
				return nil, fmt.Errorf("task %s has no readable created_at", id)
			}
			if now.Sub(at) <= staleTaskAge {
				continue
			}
			title, _ := task["title"].(string)
			stale = append(stale, staleTask{ID: id, Title: title, Status: status, CreatedAt: created})
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].CreatedAt < stale[j].CreatedAt })
	return stale, nil
}

// hubTasks reads the stale tasks through the client's authenticated hub transport. Its URL and credential
// come from the process environment through clientsync.RequireEnv; this file never reads or prints them.
func hubTasks(ctx context.Context, now time.Time) ([]staleTask, error) {
	hubGet, err := hubGetter(ctx)
	if err != nil {
		return nil, err
	}
	return findStaleTasks(func(status string) (map[string]any, error) {
		return hubGet("/api/v2/tasks/?limit=500&task_status=" + status)
	}, func(id string) (map[string]any, error) {
		return hubGet("/api/v2/tasks/" + id)
	}, now)
}

// hubGetter returns a GET against the hub through the client's authenticated transport.
func hubGetter(ctx context.Context) (func(path string) (map[string]any, error), error) {
	baseURL, err := clientsync.RequireEnv("AGENTHUB_URL")
	if err != nil {
		return nil, err
	}
	token, err := clientsync.RequireEnv("AGENTHUB_TOKEN")
	if err != nil {
		return nil, err
	}
	return func(path string) (map[string]any, error) {
		return clientsync.RequestJSON(ctx, "GET", baseURL, token, path, nil)
	}, nil
}

// hubTaskStatus reads one task's status from the hub.
func hubTaskStatus(ctx context.Context, id string) (string, error) {
	hubGet, err := hubGetter(ctx)
	if err != nil {
		return "", err
	}
	answer, err := hubGet("/api/v2/tasks/" + id)
	if err != nil {
		return "", err
	}
	task, _ := answer["task"].(map[string]any)
	status, _ := task["status"].(string)
	if status == "" {
		return "", fmt.Errorf("task %s has no readable status", id)
	}
	return status, nil
}

// closedStatuses end a task's work; any other status is still open.
var closedStatuses = map[string]bool{"done": true, "cancelled": true, "archived": true}

// committedOpenTask is a hub task a commit in the window names in its `Task:` trailer that is not closed.
type committedOpenTask struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Commits int    `json:"commits"`
	LastSHA string `json:"last_sha"`
	Title   string `json:"last_title"`
}

// findCommittedOpen looks up each distinct task id the commits name (newest commit first, as git log
// prints them) and lists those still open, plus the number of commits with no Task trailer.
func findCommittedOpen(commits []commitRecord, status func(id string) (string, error)) ([]committedOpenTask, int, error) {
	var order []string
	byID := map[string]*committedOpenTask{}
	noTrailer := 0
	for _, c := range commits {
		if c.TaskID == "" {
			noTrailer++
			continue
		}
		entry, seen := byID[c.TaskID]
		if !seen {
			entry = &committedOpenTask{ID: c.TaskID, LastSHA: c.SHA, Title: c.Title}
			byID[c.TaskID] = entry
			order = append(order, c.TaskID)
		}
		entry.Commits++
	}
	var open []committedOpenTask
	for _, id := range order {
		state, err := status(id)
		if err != nil {
			return nil, 0, err
		}
		if closedStatuses[state] {
			continue
		}
		byID[id].Status = state
		open = append(open, *byID[id])
	}
	return open, noTrailer, nil
}

// targetResult is one target's verdict. Seat names the scope when the target belongs to one seat rather than
// the room; the verdict vocabulary gains `diag`, meaning "printed, not gated".
type targetResult struct {
	Name   string  `json:"name"`
	Seat   string  `json:"seat,omitempty"`
	Value  float64 `json:"value"`
	Limit  string  `json:"limit"`
	Result string  `json:"result"`
}

func verdict(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

// verdictDiag marks a line the report prints and never gates.
const verdictDiag = "diag"

// roomSpec is the slice of the room's spec.json the report reads: which seats edit code, and which seat is
// the gate.
type roomSpec struct {
	Editing  []string
	Reviewer string
}

// readRoomSpec names the EDITING seats - the room spec's `developer` seats - and the reviewer whose PASS is
// what makes a gate row a pass.
//
// The code-edit target is per SEAT, not per room, because the seats that may not edit code (lead, reviewer,
// architect, named by their own seat_type: lead, reviewer, architect) carry most of a gated room's cost and
// would make a room-wide floor a measure of the role split (seats/architect/RULING-code-edit-share-target.md).
// The room's own spec is the only place that says which seats edit, so it is read here; a spec that cannot be
// read yields no editing seats and no reviewer, and the report says so.
func readRoomSpec(roomDir string) (roomSpec, error) {
	raw, err := os.ReadFile(filepath.Join(roomDir, "spec.json"))
	if err != nil {
		return roomSpec{}, err
	}
	var spec clientruntime.RoomSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return roomSpec{}, err
	}
	var out roomSpec
	for _, seat := range spec.Seats {
		switch seat.SeatType {
		case "developer":
			out.Editing = append(out.Editing, seat.SeatKey)
		case "reviewer":
			if out.Reviewer == "" {
				out.Reviewer = seat.SeatKey
			}
		}
	}
	return out, nil
}

// gateTokenRe matches what a queue row uses to name work: a task's 8-character id prefix or a commit sha,
// 7 to 40 hex characters.
var gateTokenRe = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

// passedTasks is the gate-passed set: a task's 8-character id -> the seats the gate passed it for.
//
// A pass is a queue row tagged `gate`, in state done, whose done transition was made by the room's reviewer -
// the reviewer's PASS is what a pass is - and whose summary or body names the task by its 8-character id or a
// commit by its sha. The seat credited is the row's SOURCE seat: the seat that filed the gate is the seat
// whose work was gated. Bodies are matched, never printed.
//
// Joining through a named sha is not decoration: gate rows speak in tips ("client 9c9f4f8", "tip 858abf2") and
// only sometimes in task ids, and the named commit's Task: trailer names the task. Without that path the live
// numerator is zero for work whose gate row is perfectly legible.
func passedTasks(rows []clientruntime.QueueRow, room, reviewer string, commits []commitRecord) map[string]map[string]bool {
	prefix := "agenthub--" + room + "--"
	passed := map[string]map[string]bool{}
	if reviewer == "" {
		return passed
	}
	for _, row := range rows {
		if row.State != "done" || !hasTag(row.Tags, "gate") {
			continue
		}
		closedByReviewer := false
		for _, tr := range row.Transitions {
			if tr.State == "done" && tr.ActorSession == prefix+reviewer {
				closedByReviewer = true
				break
			}
		}
		if !closedByReviewer {
			continue
		}
		seat, ok := strings.CutPrefix(row.SourceSession, prefix)
		if !ok || seat == "" {
			continue
		}
		text := strings.ToLower(row.Summary + "\n" + row.Body)
		for _, token := range gateTokenRe.FindAllString(text, -1) {
			for _, c := range commits {
				if c.TaskID == "" {
					continue
				}
				if !strings.HasPrefix(strings.ToLower(c.SHA), token) && !strings.HasPrefix(c.TaskID, token) {
					continue
				}
				id := c.TaskID[:8]
				if passed[id] == nil {
					passed[id] = map[string]bool{}
				}
				passed[id][seat] = true
			}
		}
	}
	return passed
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// editYield is one editing seat's ruled ratio: the commits the gate passed for that seat, over its code-edit
// USD. It is a DIAGNOSTIC until five daily windows exist and a ruling sets its floor.
type editYield struct {
	Seat        string  `json:"seat"`
	Commits     int     `json:"gate_passed_commits"`
	CodeEditUSD float64 `json:"code_edit_usd"`
	PerUSD      float64 `json:"commits_per_usd"`
	Result      string  `json:"result"`
}

// yieldDrops counts every commit the ratio's join left out, by reason, so a numerator of zero is explained
// rather than mysterious.
type yieldDrops struct {
	NoTask    int `json:"no_task_trailer"`
	NotPassed int `json:"task_not_gate_passed"`
	OtherSeat int `json:"gate_passed_for_another_seat"`
	PinOnly   int `json:"pin_only"`
}

// editYields counts the gate-passed commits per editing seat. A commit counts when its Task: names a task the
// gate passed FOR THAT SEAT and its Seat: trailer names the same seat; a commit that only moves a submodule
// pin never counts. Every other commit lands in one drop bucket.
func editYields(commits []commitRecord, passed map[string]map[string]bool, editing []*seatStats) ([]editYield, yieldDrops) {
	var drops yieldDrops
	counts := map[string]int{}
	for _, c := range commits {
		if c.TaskID == "" {
			drops.NoTask++
			continue
		}
		seats := passed[c.TaskID[:8]]
		if len(seats) == 0 {
			drops.NotPassed++
			continue
		}
		if c.Seat == "" || !seats[c.Seat] {
			drops.OtherSeat++
			continue
		}
		if c.PinOnly {
			drops.PinOnly++
			continue
		}
		counts[c.Seat]++
	}
	yields := make([]editYield, 0, len(editing))
	for _, seat := range editing {
		y := editYield{Seat: seat.Seat, Commits: counts[seat.Seat], CodeEditUSD: seat.ActivityCost[actCodeEdit], Result: verdictDiag}
		if y.CodeEditUSD > 0 {
			y.PerUSD = float64(y.Commits) / y.CodeEditUSD
		}
		yields = append(yields, y)
	}
	return yields, drops
}

// codeEditFloor is the ratcheted floor and the replay that produced it, printed so the number is auditable.
type codeEditFloor struct {
	Base    float64 `json:"base"`
	Step    float64 `json:"step"`
	Cap     float64 `json:"cap"`
	Floor   float64 `json:"floor"`
	Days    int     `json:"days_replayed"`
	Stepped int     `json:"days_stepped"`
}

// ratchetedFloor replays the last complete UTC days from the seats' own turns: each day on which every
// editing seat has turns and its code-edit share clears the floor then in force raises the floor by one step,
// up to the cap. No day lowers it, and a day on which one editing seat fails holds it. Today is not replayed:
// it is not complete, and a partial day would hold the floor for a reason that is not the seats'.
//
// The reading is the any-call one the floor itself gates, not the first-call class room metrics persists: a
// ratchet moved by a different classifier would step for reasons the target does not measure.
func ratchetedFloor(daysBySeat map[string]seatDays, editing []string, now time.Time) codeEditFloor {
	floor := codeEditFloor{Base: codeEditFloorBase, Step: codeEditFloorStep, Cap: codeEditCeil, Floor: codeEditFloorBase}
	if len(editing) == 0 {
		return floor
	}
	for back := codeEditFloorDays; back >= 1; back-- {
		day := now.UTC().AddDate(0, 0, -back).Format("2006-01-02")
		floor.Days++
		all := true
		for _, seat := range editing {
			split := daysBySeat[seat][day]
			if split.Total <= 0 || 100*split.CodeEdit/split.Total <= floor.Floor {
				all = false
				break
			}
		}
		if all {
			floor.Stepped++
			floor.Floor += floor.Step
			if floor.Floor > floor.Cap {
				floor.Floor = floor.Cap
			}
		}
	}
	return floor
}

// evaluateTargets checks the room totals against the four room targets, then the code-edit floor per editing
// seat against the ratcheted floor, then the paired edit yield per editing seat as a diagnostic, and prints
// the room's own code-edit share as a diagnostic too. A target whose source is missing is "n/a" rather than a
// pass.
func evaluateTargets(room *seatStats, days float64, gitOK, queueOK bool, editing []*seatStats, floor codeEditFloor, yields []editYield) []targetResult {
	rowsPerDay := float64(room.QueueRows) / days
	results := []targetResult{
		{Name: "docs-only commits %", Value: room.docsOnlyPct(), Limit: fmt.Sprintf("< %.0f", targetDocsOnlyMax)},
		{Name: "queue rows/day", Value: rowsPerDay, Limit: fmt.Sprintf("< %.0f", targetRowsPerDay)},
		{Name: "high-priority rows %", Value: room.highPct(), Limit: fmt.Sprintf("< %.0f", targetHighMax)},
		{Name: "measuring cost %", Value: room.ActivityPct[actMeasuring], Limit: fmt.Sprintf("< %.0f", targetMeasuringMax)},
	}
	pass := []bool{
		room.docsOnlyPct() < targetDocsOnlyMax, rowsPerDay < targetRowsPerDay,
		room.highPct() < targetHighMax, room.ActivityPct[actMeasuring] < targetMeasuringMax,
	}
	available := []bool{gitOK, queueOK, queueOK, true}
	for i := range results {
		results[i].Result = "n/a"
		if available[i] {
			results[i].Result = verdict(pass[i])
		}
	}
	for _, seat := range editing {
		line := targetResult{
			Name:  "code-edit cost %",
			Seat:  seat.Seat,
			Value: seat.ActivityPct[actCodeEdit],
			Limit: fmt.Sprintf("> %.0f, cap %.0f", floor.Floor, floor.Cap),
		}
		if seat.Turns == 0 {
			line.Result = "n/a"
		} else {
			line.Result = verdict(seat.ActivityPct[actCodeEdit] > floor.Floor)
		}
		results = append(results, line)
	}
	for _, y := range yields {
		line := targetResult{
			Name:   "edit yield (commits/USD)",
			Seat:   y.Seat,
			Value:  y.PerUSD,
			Limit:  fmt.Sprintf("diagnostic for %d daily windows", yieldBaselineDays),
			Result: y.Result,
		}
		if !gitOK || !queueOK {
			line.Result = "n/a"
		}
		results = append(results, line)
	}
	results = append(results, targetResult{
		Name:   "code-edit cost %",
		Seat:   "room",
		Value:  room.ActivityPct[actCodeEdit],
		Limit:  "diagnostic, not a target",
		Result: verdictDiag,
	})
	return results
}

// Report is the whole answer, in the shape --json prints.
type Report struct {
	Room    string            `json:"room"`
	Since   string            `json:"since"`
	Repo    string            `json:"repo"`
	Seats   []*seatStats      `json:"seats"`
	Total   *seatStats        `json:"total"`
	Targets []targetResult    `json:"targets"`
	Sources map[string]string `json:"sources"`
	Stale   []staleTask       `json:"stale_tasks"`

	Floor      codeEditFloor `json:"code_edit_floor"`
	EditYield  []editYield   `json:"edit_yield"`
	YieldDrops yieldDrops    `json:"edit_yield_drops"`

	CommittedOpen []committedOpenTask `json:"committed_but_open"`
	NoTrailer     int                 `json:"commits_without_task_trailer"`
}

// sinceLabel prints a window the way it is typed: 24h, 90m, 7d.
func sinceLabel(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return strings.NewReplacer("m0s", "m", "h0m", "h").Replace(d.String())
}

// parseSince reads a window such as 24h, 90m or 7d.
func parseSince(raw string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--since %q is not a positive duration", raw)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since %q is not a positive duration such as 24h or 7d", raw)
	}
	return d, nil
}

// buildReport measures the room. queueRows and its error come from the daemon; fetchTasks from the hub.
func buildReport(room, roomDir string, window time.Duration, now time.Time,
	queueRows func() ([]clientruntime.QueueRow, error), tasks func() ([]staleTask, error),
	taskStatus func(id string) (string, error)) (*Report, error) {
	cutoff := now.Add(-window)
	seatDirs, err := filepath.Glob(filepath.Join(roomDir, "seats", "*"))
	if err != nil {
		return nil, err
	}
	bySeat := map[string]*seatStats{}
	daysBySeat := map[string]seatDays{}
	var cwds []string
	for _, dir := range seatDirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		stats := seatFor(bySeat, filepath.Base(dir))
		seatCwds, days, err := readSessions(dir, cutoff, stats)
		if err != nil {
			return nil, fmt.Errorf("read %s sessions: %w", stats.Seat, err)
		}
		cwds = append(cwds, seatCwds...)
		daysBySeat[stats.Seat] = days
		stats.Blocked = readBlocked(dir, cutoff)
	}
	report := &Report{Room: room, Since: sinceLabel(window), Sources: map[string]string{}}

	spec, specErr := readRoomSpec(roomDir)
	if specErr != nil {
		report.Sources["spec"] = "unavailable (" + specErr.Error() + ")"
	} else {
		report.Sources["spec"] = "ok"
	}

	report.Repo = mostCommon(cwds)
	commits, err := gitCommits(report.Repo, room, cutoff)
	switch {
	case report.Repo == "":
		report.Sources["git"] = "unavailable (no session names a working directory)"
	case err != nil:
		report.Sources["git"] = "unavailable (" + err.Error() + ")"
	default:
		report.Sources["git"] = "ok"
		for _, c := range commits {
			seat := c.Seat
			if seat == "" {
				seat = "other"
			}
			stats := seatFor(bySeat, seat)
			stats.Commits++
			if c.DocsOnly {
				stats.DocsOnly++
			}
		}
		report.CommittedOpen, report.NoTrailer, err = findCommittedOpen(commits, taskStatus)
		if err != nil {
			report.Sources["commit_tasks"] = "unavailable (" + err.Error() + ")"
		} else {
			report.Sources["commit_tasks"] = "ok"
		}
	}
	if report.Sources["commit_tasks"] == "" {
		report.Sources["commit_tasks"] = "unavailable (" + report.Sources["git"] + ")"
	}

	rows, err := queueRows()
	if err != nil {
		report.Sources["queue"] = "unavailable (" + err.Error() + ")"
	} else {
		report.Sources["queue"] = "ok"
		applyQueue(rows, room, cutoff, bySeat)
	}

	stale, err := tasks()
	if err != nil {
		report.Sources["tasks"] = "unavailable (" + err.Error() + ")"
	} else {
		report.Sources["tasks"] = "ok"
		report.Stale = stale
	}

	report.Total = newSeatStats("ROOM")
	names := make([]string, 0, len(bySeat))
	for name := range bySeat {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		bySeat[name].finish()
		report.Total.add(bySeat[name])
		report.Seats = append(report.Seats, bySeat[name])
	}
	report.Total.finish()
	editingStats := make([]*seatStats, 0, len(spec.Editing))
	for _, seat := range spec.Editing {
		stats, seen := bySeat[seat]
		if !seen {
			stats = newSeatStats(seat)
		}
		editingStats = append(editingStats, stats)
	}
	report.Floor = ratchetedFloor(daysBySeat, spec.Editing, now)
	report.EditYield, report.YieldDrops = editYields(commits, passedTasks(rows, room, spec.Reviewer, commits), editingStats)
	report.Targets = evaluateTargets(report.Total, window.Hours()/24, report.Sources["git"] == "ok",
		report.Sources["queue"] == "ok", editingStats, report.Floor, report.EditYield)
	closed := targetResult{Name: "tasks closed after commit", Value: float64(len(report.CommittedOpen)), Limit: "0", Result: "n/a"}
	if report.Sources["commit_tasks"] == "ok" {
		closed.Result = verdict(len(report.CommittedOpen) == 0)
	}
	report.Targets = append(report.Targets, closed)
	return report, nil
}

func (r *Report) renderText(out io.Writer) {
	fmt.Fprintf(out, "report: room %s, last %s, repo %s\n", r.Room, r.Since, orDash(r.Repo))
	fmt.Fprintf(out, "sources: git %s; queue %s; tasks %s; spec %s\n\n", r.Sources["git"], r.Sources["queue"], r.Sources["tasks"], r.Sources["spec"])
	fmt.Fprintf(out, "%-10s %9s %6s %7s %6s %9s %7s %6s %8s\n", "SEAT", "USD", "TURNS", "COMMITS", "DOCS%", "QROWS", "HIGH%", "AVGBODY", "BLOCKED")
	for _, s := range append(append([]*seatStats{}, r.Seats...), r.Total) {
		fmt.Fprintf(out, "%-10s %9.4f %6d %7d %6.1f %9d %7.1f %6.0f %8d\n",
			s.Seat, s.Cost, s.Turns, s.Commits, s.docsOnlyPct(), s.QueueRows, s.highPct(), s.avgBodyChars(), s.Blocked)
	}
	fmt.Fprintf(out, "\ncost share by activity (%% of USD; a turn counts as code-edit when ANY of its calls edits code)\n%-10s", "SEAT")
	for _, a := range activityOrder {
		fmt.Fprintf(out, " %13s", a)
	}
	fmt.Fprintln(out)
	for _, s := range append(append([]*seatStats{}, r.Seats...), r.Total) {
		fmt.Fprintf(out, "%-10s", s.Seat)
		for _, a := range activityOrder {
			fmt.Fprintf(out, " %13.1f", s.ActivityPct[a])
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "\ntargets")
	for _, t := range r.Targets {
		label := t.Name
		if t.Seat != "" {
			label += " [" + t.Seat + "]"
		}
		fmt.Fprintf(out, "  %-4s %-35s %8.1f  (want %s)\n", t.Result, label, t.Value, t.Limit)
	}
	fmt.Fprintf(out, "\ncode-edit floor: %.0f (+%.0f per complete UTC day every editing seat passes, never down, cap %.0f); replayed %d days, stepped %d\n",
		r.Floor.Floor, r.Floor.Step, r.Floor.Cap, r.Floor.Days, r.Floor.Stepped)
	switch {
	case r.Sources["git"] != "ok" || r.Sources["queue"] != "ok":
		fmt.Fprintf(out, "edit yield: unavailable (git %s; queue %s)\n", r.Sources["git"], r.Sources["queue"])
	case len(r.EditYield) == 0:
		fmt.Fprintln(out, "edit yield: no editing seat in the spec")
	default:
		fmt.Fprintln(out, "edit yield (gate-passed commits per code-edit USD; diagnostic)")
		for _, y := range r.EditYield {
			fmt.Fprintf(out, "  %-10s %3d commits / %9.4f USD = %7.4f per USD\n", y.Seat, y.Commits, y.CodeEditUSD, y.PerUSD)
		}
		fmt.Fprintf(out, "  dropped: no Task: %d, task not gate-passed %d, gate passed for another seat %d, pin-only %d\n",
			r.YieldDrops.NoTask, r.YieldDrops.NotPassed, r.YieldDrops.OtherSeat, r.YieldDrops.PinOnly)
	}
	fmt.Fprintf(out, "\ncode-edit by FIRST tool call (the reading room metrics persists, for comparison): %.1f%%\n", r.Total.CodeEditFirstCallPct)
	fmt.Fprintln(out)
	switch {
	case r.Sources["tasks"] != "ok":
		fmt.Fprintf(out, "tasks: %s\n", r.Sources["tasks"])
	case len(r.Stale) == 0:
		fmt.Fprintln(out, "stale tasks: none")
	default:
		fmt.Fprintf(out, "stale tasks (todo or in_progress, open for over 24h): %d\n", len(r.Stale))
		for _, t := range r.Stale {
			fmt.Fprintf(out, "  %s %-11s %s  %s\n", t.ID, t.Status, t.CreatedAt, t.Title)
		}
	}
	fmt.Fprintln(out)
	if r.Sources["commit_tasks"] != "ok" {
		fmt.Fprintf(out, "committed but still open: %s\n", r.Sources["commit_tasks"])
		return
	}
	fmt.Fprintf(out, "committed but still open: %d\n", len(r.CommittedOpen))
	for _, t := range r.CommittedOpen {
		fmt.Fprintf(out, "  %s %-11s %3d commits  %.10s  %s\n", t.ID, t.Status, t.Commits, t.LastSHA, t.Title)
	}
	fmt.Fprintf(out, "commits without Task trailer: %d\n", r.NoTrailer)
}

func runReport(args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		fmt.Fprintln(stdout, reportUsage)
		return clientcmd.ExitOK
	}
	rootFlag, args, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam room report: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitRoomRefused)
	}
	opts, rest, err := clientruntime.SplitFlags(args, queueFlagSpecs(
		clientruntime.FlagSpec{Name: "since", Value: true}, clientruntime.FlagSpec{Name: "json"}))
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room report: %v. %s\n", err, reportUsage)
		return clientcmd.ExitRoomRefused
	}
	since := "24h"
	if v, ok := opts["since"]; ok {
		since = v
	}
	window, err := parseSince(since)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room report: %v. %s\n", err, reportUsage)
		return clientcmd.ExitRoomRefused
	}
	pairArgs := append(append(make([]string, 0, len(rest)+4), rest...),
		"--state-root", rootFlag, "--socket", opts["socket"])
	inv, code, ok := parseInvocation("report", pairArgs, 1, stdout, stderr)
	if !ok {
		return code
	}
	roomDir := filepath.Join(inv.stateDir, "rooms", inv.room)
	if info, err := os.Stat(roomDir); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "4genteam room report: no room directory at %s\n", roomDir)
		return clientcmd.ExitRoomRefused
	}
	now := time.Now().UTC()
	report, err := buildReport(inv.room, roomDir, window, now,
		func() ([]clientruntime.QueueRow, error) {
			return inv.client.QueueList(inv.room, clientruntime.QueueListFilter{})
		},
		func() ([]staleTask, error) { return hubTasks(context.Background(), now) },
		func(id string) (string, error) { return hubTaskStatus(context.Background(), id) })
	if err != nil {
		fmt.Fprintf(stderr, "4genteam room report: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	if _, asJSON := opts["json"]; asJSON {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
		return clientcmd.ExitOK
	}
	report.renderText(stdout)
	return clientcmd.ExitOK
}
