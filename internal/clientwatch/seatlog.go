package clientwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// Where a seat's session log lives. An omp seat keeps one under the state root; a Claude Code seat
// keeps one under ~/.claude/projects, found from its process. A runtime the client has no reader for
// (codex, agy) has neither, so its pane keeps its header and shows no events - which is what the doc
// says a reader sees.

var (
	claudeSessionID = regexp.MustCompile(`--(?:session-id|resume) ([0-9a-f-]{36})`)
	modelInLog      = regexp.MustCompile(`"model":"([^"]+)"`)
)

func claudeProjects() string {
	home, err := clientenv.RealHome()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// newestSession is the most recently written sessions/*.jsonl under one seat's state directory.
func newestSession(seatDir string) string {
	matches, err := filepath.Glob(filepath.Join(seatDir, "sessions", "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return newestByModTime(matches)
}

// claudeLog is the session log of a Claude Code seat, found from the --session-id of its process.
func claudeLog(ctx context.Context, rig, seat string) string {
	out, err := execCommand(ctx, "pgrep", "-af", fmt.Sprintf("claude .*--name %s-%s@%s", rig, seat, rig))
	if err != nil {
		return ""
	}
	roots := claudeProjects()
	if roots == "" {
		return ""
	}
	found := []string{}
	for _, match := range claudeSessionID.FindAllStringSubmatch(out, -1) {
		pattern := filepath.Join(roots, "*", match[1]+".jsonl")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		found = append(found, matches...)
	}
	if len(found) == 0 {
		return ""
	}
	return newestByModTime(found)
}

func newestByModTime(paths []string) string {
	sort.Slice(paths, func(i, j int) bool {
		left, leftErr := os.Stat(paths[i])
		right, rightErr := os.Stat(paths[j])
		if leftErr != nil || rightErr != nil {
			return paths[i] < paths[j]
		}
		if left.ModTime().Equal(right.ModTime()) {
			return paths[i] < paths[j]
		}
		return left.ModTime().Before(right.ModTime())
	})
	return paths[len(paths)-1]
}

// seatLog is (the newest session log, the runtime) of a seat: omp state first, else the Claude Code
// log. A seat with neither yields an empty path and no runtime.
func seatLog(ctx context.Context, root, rig, seat string) (string, string) {
	seatDir := filepath.Join(root, fmt.Sprintf("%s-%s@%s", rig, seat, rig))
	if info, err := os.Stat(seatDir); err == nil && info.IsDir() {
		return newestSession(seatDir), "omp"
	}
	return claudeLog(ctx, rig, seat), "claude"
}

// contextTokens is the tokens in the context after this record's reply, or nil when the record carries
// no usage.
func contextTokens(line string) *int {
	value, _, err := decodeOrderedJSON(line)
	if err != nil {
		return nil
	}
	record, ok := value.(*orderedJSONObject)
	if !ok {
		return nil
	}
	if kind, _ := record.get("type"); kind == "compaction" {
		if after := numberValueOrFloat(record, "tokensAfter"); after != nil {
			return after
		}
	}
	message, _ := record.get("message")
	msg, ok := message.(*orderedJSONObject)
	if !ok {
		return nil
	}
	rawUsage, _ := msg.get("usage")
	usage, ok := rawUsage.(*orderedJSONObject)
	if !ok {
		return nil
	}
	if total := numberValueOrFloat(usage, "totalTokens"); total != nil {
		return total
	}
	sum := 0
	for _, key := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens"} {
		if part := numberValueOrFloat(usage, key); part != nil {
			sum += *part
		}
	}
	if sum == 0 {
		return nil
	}
	return &sum
}

// numberValueOrFloat reads a JSON number as an int, tolerating a float form.
func numberValueOrFloat(record *orderedJSONObject, key string) *int {
	raw, present := record.get(key)
	if !present {
		return nil
	}
	number, ok := raw.(json.Number)
	if !ok {
		return nil
	}
	if value, err := number.Int64(); err == nil {
		asInt := int(value)
		return &asInt
	}
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil {
		return nil
	}
	asInt := int(value)
	return &asInt
}

// seatModel is the model that answered last, read from the log's tail (message.model in both runtimes).
func seatModel(logPath string) string {
	if logPath == "" {
		return ""
	}
	handle, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer func() { _ = handle.Close() }()
	info, err := handle.Stat()
	if err != nil {
		return ""
	}
	offset := int64(0)
	if info.Size() > 300_000 {
		offset = info.Size() - 300_000
	}
	if _, err := handle.Seek(offset, 0); err != nil {
		return ""
	}
	raw := make([]byte, info.Size()-offset)
	read, err := handle.Read(raw)
	if err != nil && read == 0 {
		return ""
	}
	found := modelInLog.FindAllStringSubmatch(string(raw[:read]), -1)
	if len(found) == 0 {
		return ""
	}
	return found[len(found)-1][1]
}

// readLines is the whole log as lines, for the initial replay.
func readLines(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return splitLines(string(raw))
}

// logSize is a log's current size, the offset the tailing loop resumes from.
func logSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// readFrom is the bytes written since offset.
func readFrom(path string, offset int64) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if offset > int64(len(raw)) {
		return ""
	}
	return strings.ToValidUTF8(string(raw[offset:]), "\uFFFD")
}
