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

// SeatLog is seat_log: (the seat's newest session log, its runtime), where the omp state directory is
// tried first and the Claude Code log is the fallback. An omp state directory with no session in it (the rig orchestrator
// writes one for a Claude Code seat too, holding only its guide) does not count as an omp seat. An empty path means the seat has no log this
// machine can read, which the caller skips rather than guessing at.
func SeatLog(stateRoot, rig, seat string) (string, string) {
	seatDir := filepath.Join(stateRoot, rig+"-"+seat+"@"+rig)
	if info, err := os.Stat(seatDir); err == nil && info.IsDir() {
		if session := NewestSession(seatDir); session != "" {
			return session, "omp"
		}
	}
	return claudeLog(rig, seat), "claude"
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
func claudeLog(rig, seat string) string {
	if ClaudeProjectsDir == "" {
		return ""
	}
	out, err := exec.Command("pgrep", "-af", "claude .*--name "+rig+"-"+seat+"@"+rig).Output()
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
// record carries no usage. A compaction record states its own tokensAfter; otherwise the reading is
// message.usage.totalTokens when it has one, and the sum of the four per-field counts when it does not.
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

// RigSeats is rig_seats: the seats of a rig that have a live tmux session, sorted. The omp state
// directories outlive removed seats, so the tmux session list is what says a seat is really there.
func RigSeats(rig string) []string {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}
	var seats []string
	for _, name := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, rig+"-") && strings.HasSuffix(name, "@"+rig) {
			seats = append(seats, strings.TrimSuffix(strings.TrimPrefix(name, rig+"-"), "@"+rig))
		}
	}
	sort.Strings(seats)
	return seats
}
