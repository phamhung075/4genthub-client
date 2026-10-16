// `room report`: one read-only measurement of one room over a time window.
//
// The report exists so the five targets below are read off the room's own records instead of an ad-hoc
// script. Four sources feed it, and each is read the way its owner exposes it:
//
//   - the seats' session transcripts (<state-dir>/rooms/<room>/seats/<seat>/sessions/*.jsonl): every
//     assistant message carries its cost and is classified by its FIRST tool call;
//   - the seats' tool-calls.jsonl: how many calls the seat's guard blocked;
//   - the room's queue, asked from the runtime daemon (QueueList) - this client links no SQLite driver and
//     the daemon is the queue store's only owner. Only counts and lengths are used: a body is never printed;
//   - `git log --since` of the repository the seats worked in, where a commit's `Seat:` trailer names the
//     seat that made it (a commit without one is counted as "other").
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
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

const reportUsage = "usage: 4genteam room report ROOM [--since 24h] [--json] [--state-dir DIR] [--socket PATH]"

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

// The targets, as the plan's section 2.7 states them.
const (
	targetDocsOnlyMax  = 15.0
	targetRowsPerDay   = 300.0
	targetHighMax      = 20.0
	targetMeasuringMax = 25.0
	targetCodeEditMin  = 30.0
)

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
	Blocked      int                `json:"guard_blocked_calls"`
	Commits      int                `json:"commits"`
	DocsOnly     int                `json:"docs_only_commits"`
	QueueRows    int                `json:"queue_rows"`
	QueueHigh    int                `json:"queue_high_rows"`
	BodyChars    int                `json:"queue_body_chars"`
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

// readSessions adds one seat's assistant turns at or after cutoff to stats, and returns the working
// directories its session headers named, for locating the repository.
func readSessions(seatDir string, cutoff time.Time, stats *seatStats) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(seatDir, "sessions", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var cwds []string
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
			if err != nil || at.Before(cutoff) {
				return
			}
			cost := line.Message.Usage.Cost.Total
			stats.Turns++
			stats.Cost += cost
			stats.OutputTokens += line.Message.Usage.Output
			stats.ActivityCost[firstToolClass(line.Message.Content)] += cost
		})
		if err != nil {
			return nil, err
		}
	}
	return cwds, nil
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

// commitRecord is one commit: the seat its `Seat:` trailer names ("" when none) and whether every file it
// touched is documentation.
type commitRecord struct {
	Seat     string
	DocsOnly bool
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
// `agenthub--<room>--<seat>`; only a trailer naming this room's prefix is mapped to a seat.
func gitCommits(repo, room string, cutoff time.Time) ([]commitRecord, error) {
	out, err := exec.Command("git", "-C", repo, "log", "--since="+cutoff.Format(time.RFC3339), "--name-only",
		"--format="+recordSep+"%H"+fieldSep+"%s"+fieldSep+"%(trailers:key=Seat,valueonly,separator=%x2c)"+fieldSep+
			"%(trailers:key=Task,valueonly,separator=%x2c)"+fieldSep+"%cI"+fieldSep).Output()
	if err != nil {
		return nil, fmt.Errorf("git log in %s: %w", repo, err)
	}
	return parseGitLog(string(out), room), nil
}

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
		touched, allDocs := false, true
		for _, file := range strings.Split(files, "\n") {
			if file = strings.TrimSpace(file); file != "" {
				touched = true
				rec.Files++
				allDocs = allDocs && docPath(file)
			}
		}
		rec.DocsOnly = touched && allDocs
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

// targetResult is one target's verdict.
type targetResult struct {
	Name   string  `json:"name"`
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

// evaluateTargets checks the room totals against the five targets. A target whose source is missing is
// "n/a" rather than a pass.
func evaluateTargets(room *seatStats, days float64, gitOK, queueOK bool) []targetResult {
	rowsPerDay := float64(room.QueueRows) / days
	results := []targetResult{
		{Name: "docs-only commits %", Value: room.docsOnlyPct(), Limit: fmt.Sprintf("< %.0f", targetDocsOnlyMax)},
		{Name: "queue rows/day", Value: rowsPerDay, Limit: fmt.Sprintf("< %.0f", targetRowsPerDay)},
		{Name: "high-priority rows %", Value: room.highPct(), Limit: fmt.Sprintf("< %.0f", targetHighMax)},
		{Name: "measuring cost %", Value: room.ActivityPct[actMeasuring], Limit: fmt.Sprintf("< %.0f", targetMeasuringMax)},
		{Name: "code-edit cost %", Value: room.ActivityPct[actCodeEdit], Limit: fmt.Sprintf("> %.0f", targetCodeEditMin)},
	}
	pass := []bool{
		room.docsOnlyPct() < targetDocsOnlyMax, rowsPerDay < targetRowsPerDay, room.highPct() < targetHighMax,
		room.ActivityPct[actMeasuring] < targetMeasuringMax, room.ActivityPct[actCodeEdit] > targetCodeEditMin,
	}
	available := []bool{gitOK, queueOK, queueOK, true, true}
	for i := range results {
		results[i].Result = "n/a"
		if available[i] {
			results[i].Result = verdict(pass[i])
		}
	}
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
	var cwds []string
	for _, dir := range seatDirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		stats := seatFor(bySeat, filepath.Base(dir))
		seatCwds, err := readSessions(dir, cutoff, stats)
		if err != nil {
			return nil, fmt.Errorf("read %s sessions: %w", stats.Seat, err)
		}
		cwds = append(cwds, seatCwds...)
		stats.Blocked = readBlocked(dir, cutoff)
	}
	report := &Report{Room: room, Since: sinceLabel(window), Sources: map[string]string{}}

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
	report.Targets = evaluateTargets(report.Total, window.Hours()/24, report.Sources["git"] == "ok", report.Sources["queue"] == "ok")
	closed := targetResult{Name: "tasks closed after commit", Value: float64(len(report.CommittedOpen)), Limit: "0", Result: "n/a"}
	if report.Sources["commit_tasks"] == "ok" {
		closed.Result = verdict(len(report.CommittedOpen) == 0)
	}
	report.Targets = append(report.Targets, closed)
	return report, nil
}

func (r *Report) renderText(out io.Writer) {
	fmt.Fprintf(out, "report: room %s, last %s, repo %s\n", r.Room, r.Since, orDash(r.Repo))
	fmt.Fprintf(out, "sources: git %s; queue %s; tasks %s\n\n", r.Sources["git"], r.Sources["queue"], r.Sources["tasks"])
	fmt.Fprintf(out, "%-10s %9s %6s %7s %6s %9s %7s %6s %8s\n", "SEAT", "USD", "TURNS", "COMMITS", "DOCS%", "QROWS", "HIGH%", "AVGBODY", "BLOCKED")
	for _, s := range append(append([]*seatStats{}, r.Seats...), r.Total) {
		fmt.Fprintf(out, "%-10s %9.4f %6d %7d %6.1f %9d %7.1f %6.0f %8d\n",
			s.Seat, s.Cost, s.Turns, s.Commits, s.docsOnlyPct(), s.QueueRows, s.highPct(), s.avgBodyChars(), s.Blocked)
	}
	fmt.Fprintf(out, "\ncost share by activity (%% of USD)\n%-10s", "SEAT")
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
		fmt.Fprintf(out, "  %-4s %-24s %8.1f  (want %s)\n", t.Result, t.Name, t.Value, t.Limit)
	}
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
		"--state-dir", opts["state-dir"], "--socket", opts["socket"])
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
