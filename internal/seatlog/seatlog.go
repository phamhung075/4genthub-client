// Package seatlog holds the READING of a seat's session: where its newest log is, how many tokens that
// log's last record carries, whether its pane exists, and the three compaction limits a reading is
// compared against.
//
// It is a package of its own, named for the data, because TWO verbs need the same reading: the
// compaction supervisor (this phase) and the herdr watch view (phase 4). Putting it here rather than in
// either verb's package is what keeps one parser instead of two that drift.
package seatlog

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// The three limits, read from the environment by ApplyLimits (a BLANK value means the documented
// default; see clientenv.Limit). They are package variables rather than constants because the suite
// sets them, exactly as the Python test monkeypatches watch.COMPACT_LIMIT and friends.
var (
	// CompactLimit is the soft limit: the seat is told once to find its own safe point.
	CompactLimit = 150_000
	// WarnLimit is the urgent limit: compacted now, without waiting for quiet.
	WarnLimit = 250_000
	// HardLimit is the hard limit: the running turn is aborted and compaction forced.
	HardLimit = 300_000
)

// ApplyLimits reads COMPACT_LIMIT_TOKENS, COMPACT_WARN_TOKENS and COMPACT_HARD_TOKENS into the three
// variables above. The verb that supervises calls it, which is where the Python read them (at watch's
// import), so a limit in the operator's environment or env file reaches the loop.
func ApplyLimits() error {
	for _, limit := range []struct {
		name string
		dst  *int
		def  int
	}{
		{"COMPACT_LIMIT_TOKENS", &CompactLimit, 150_000},
		{"COMPACT_WARN_TOKENS", &WarnLimit, 250_000},
		{"COMPACT_HARD_TOKENS", &HardLimit, 300_000},
	} {
		value, err := clientenv.Limit(limit.name, limit.def)
		if err != nil {
			return err
		}
		*limit.dst = value
	}
	return nil
}

// Kilo renders a token count the way every message does: 150k, 1.20M.
func Kilo(tokens int) string {
	if tokens < 1_000_000 {
		return fmt.Sprintf("%.0fk", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(tokens)/1_000_000)
}

// SeatLog is seat_log: (the seat's newest session log, its runtime), the newer of the omp session and
// the agy transcript, with the Claude Code log as the fallback. A seat that switched runtime keeps the
// old runtime's log, so existence alone does not say which one is live. An omp state directory with no
// session in it (the room orchestrator writes one for a Claude Code seat too, holding only its guide)
// does not count as an omp seat. An empty path means the seat has no log this machine can read, which
// the caller skips rather than guessing at.
func SeatLog(stateRoot, room, seat string) (string, string) {
	omp := NewestSession(clientruntime.SeatDir(stateRoot, room, seat))
	agy := AgyTranscript(stateRoot, room, seat)
	switch {
	case agy != "" && (omp == "" || newest([]string{omp, agy}) == agy):
		return agy, "agy"
	case omp != "":
		return omp, "omp"
	}
	path := claudeLog(room, seat)
	if path == "" {
		return "", ""
	}
	return path, "claude"
}

// AgyTranscript is the transcript of the conversation the seat's agy runtime records, or "" when the
// seat has none: agy names its conversation in <seat dir>/agy-conversation and keeps the log under the
// operator's home. Without it an agy seat whose directory still holds an omp-era session file reads as
// silent, which is the false alarm the idle guard raised three times on 2026-10-17.
func AgyTranscript(stateRoot, room, seat string) string {
	id, err := os.ReadFile(filepath.Join(clientruntime.SeatDir(stateRoot, room, seat), "agy-conversation"))
	home, homeErr := os.UserHomeDir()
	if err != nil || homeErr != nil || strings.TrimSpace(string(id)) == "" {
		return ""
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "brain", strings.TrimSpace(string(id)),
		".system_generated", "logs", "transcript.jsonl")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// NewestSession is newest_session: the most recently written *.jsonl under <seat dir>/sessions.
func NewestSession(seatDir string) string {
	matches, err := filepath.Glob(filepath.Join(seatDir, "sessions", "*.jsonl"))
	if err != nil {
		return ""
	}
	return newest(matches)
}

// ClaudeProjectsDir is where a Claude Code runtime keeps its session logs. It is EMPTY unless
// 4GENTEAM_CLAUDE_PROJECTS names a directory: this client drives the omp runtime, and another
// runtime's private directory is not a path it may assume. With no directory configured the Claude
// fallback finds nothing, which the caller treats as "no log".
var ClaudeProjectsDir = os.Getenv("4GENTEAM_CLAUDE_PROJECTS")

var sessionIDPattern = regexp.MustCompile(`--(?:session-id|resume) ([0-9a-f-]{36})`)

// claudeLog is claude_log: the session log of a Claude Code seat, found from the --session-id of its
// process and matched against the configured projects directory.
func claudeLog(room, seat string) string {
	if ClaudeProjectsDir == "" {
		return ""
	}
	out, err := exec.Command("pgrep", "-af", "claude .*--name "+clientruntime.SeatSessionName(room, seat)).Output()
	if err != nil {
		return ""
	}
	var found []string
	for _, match := range sessionIDPattern.FindAllStringSubmatch(string(out), -1) {
		matches, _ := filepath.Glob(filepath.Join(ClaudeProjectsDir, "*", match[1]+".jsonl"))
		found = append(found, matches...)
	}
	return newest(found)
}

func newest(paths []string) string {
	best := ""
	var bestMod int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().UnixNano() > bestMod {
			best, bestMod = path, info.ModTime().UnixNano()
		}
	}
	return best
}

// ContextTokens is context_tokens: the tokens in the context after this record, or false when the
// record carries no usage. A compaction record states its own tokensAfter; an agy transcript record
// carries its counts at the top level (input_tokens + cache_read_tokens + output_tokens); otherwise the
// reading is message.usage.totalTokens when it has one, and the sum of the four per-field counts when it
// does not.
//
// THE AGY BRANCH IS WHAT LETS THE SUPERVISOR SEE AN AGY SEAT AT ALL: the watch view's own parser read these
// top-level fields and this one did not, so `ps` showed an agy seat at 164k while the compaction supervisor
// found no reading in the same transcript and never acted (room aa2-rs-peak, 2026-10-28: limit 75k, the
// supervisor's log held only its start lines while both agy seats grew past 160k).
func ContextTokens(line string) (int, bool) {
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return 0, false
	}
	if record["type"] == "compaction" {
		if after, ok := number(record["tokensAfter"]); ok {
			return after, true
		}
	}
	agySum, agyHas := 0, false
	for _, key := range []string{"input_tokens", "cache_read_tokens", "output_tokens"} {
		if value, ok := number(record[key]); ok {
			agySum += value
			agyHas = true
		}
	}
	if agyHas && agySum > 0 {
		return agySum, true
	}
	message, ok := record["message"].(map[string]any)
	if !ok {
		return 0, false
	}
	usage, ok := message["usage"].(map[string]any)
	if !ok {
		return 0, false
	}
	if total, ok := number(usage["totalTokens"]); ok {
		return total, true
	}
	sum := 0
	for _, key := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens"} {
		value, _ := number(usage[key])
		sum += value
	}
	if sum == 0 {
		return 0, false
	}
	return sum, true
}

func number(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	}
	return 0, false
}

// RoomSeats is room_seats: the seats of a room that have a live tmux session, sorted. The omp state
// directories outlive removed seats, so the tmux session list is what says a seat is really there.
func RoomSeats(root, room string) []string {
	cmd, err := clientenv.TmuxCommandAt(root, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var seats []string
	for _, name := range strings.Fields(string(out)) {
		if sessionRoom, seat, ok := clientruntime.ParseSeatSessionName(name); ok && sessionRoom == room {
			seats = append(seats, seat)
		}
	}
	sort.Strings(seats)
	return seats
}

// compactionMarks are the records a compaction leaves in a seat's log: omp writes a `compaction` record, and
// agy writes a SYSTEM `CHECKPOINT` record whose content opens "# Resuming from a compaction" (measured on
// 23 agy checkpoints, 2026-10-26..28: the reading before each was 232k-297k and the one after it ~17k).
var compactionMarks = []string{`"type":"compaction"`, `"type":"CHECKPOINT"`}

// Compactions counts the compaction records in a log. Unlike the token reading this is over the WHOLE
// file: a compaction that has scrolled out of the tail still happened.
func Compactions(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, mark := range compactionMarks {
		count += strings.Count(string(raw), mark)
	}
	return count
}

// agyTailBytes is how much of an agy transcript the turn reading looks at: only its newest record counts.
const agyTailBytes = 256 * 1024

// AgyTurnEnded says whether an agy seat is at its prompt, from its own transcript: true only when the newest
// record is the model's FINAL answer - a PLANNER_RESPONSE, status DONE, with no tool calls. Every other
// newest record is a turn still running (a tool call waiting on its result, a tool result the model has
// not answered, a user input or system message it has not answered yet).
//
// IT EXISTS BECAUSE THE DAEMON CANNOT SAY: an agy seat has no runner, so its activity word is `unknown`
// for its whole life, and a supervisor that waits for `idle` never compacts it. Measured on the agy
// transcripts of this machine: every final PLANNER_RESPONSE with no tool calls was followed by a
// USER_INPUT (the next turn), never by more of the same turn.
//
// Anything it cannot read answers false: a seat that cannot be proved idle waits, because a /compact typed
// into a working agy pane is queued as a message rather than run.
func AgyTurnEnded(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	offset := info.Size() - agyTailBytes
	if offset < 0 {
		offset = 0
	}
	raw := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(raw, offset); err != nil {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if offset > 0 && len(lines) == 1 {
		return false // one record longer than the tail: its start was cut, so it cannot be read
	}
	var record struct {
		Type      string            `json:"type"`
		Status    string            `json:"status"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(last), &record); err != nil {
		return false
	}
	return record.Type == "PLANNER_RESPONSE" && record.Status == "DONE" && len(record.ToolCalls) == 0
}
