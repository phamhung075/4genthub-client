package clientwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// Where a seat's session log lives. An omp seat keeps one under the state root, at
// clientruntime.SeatSessionsDir. An agy seat keeps its transcript in agy's own home, under the conversation id
// the runtime records in the seat directory's `agy-conversation` file.

var modelInLog = regexp.MustCompile(`"model":"([^"]+)"`)

// newestSession is the most recently written *.jsonl in a seat's sessions directory. The directory also
// holds one artifact subdirectory per session, named like its log without the extension; the pattern
// skips them.
func newestSession(sessionsDir string) string {
	matches, err := filepath.Glob(filepath.Join(sessionsDir, "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return newestByModTime(matches)
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

// agyTranscript is the transcript of the conversation the seat's agy runs, or "" when the seat has none.
func agyTranscript(root, room, seat string) string {
	id, err := os.ReadFile(filepath.Join(clientruntime.SeatDir(root, room, seat), "agy-conversation"))
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

// seatLog is (the newest session log, the runtime) of a seat in a native room. A seat that switched runtime
// keeps the old runtime's log, so the newer of the omp log and the agy transcript is the live one. A seat with
// no log yields an empty path and no runtime.
func seatLog(root, room, seat string) (string, string) {
	omp := newestSession(clientruntime.SeatSessionsDir(root, room, seat))
	agy := agyTranscript(root, room, seat)
	switch {
	case agy == "":
		if omp == "" {
			return "", ""
		}
		return omp, "omp"
	case omp == "" || newestByModTime([]string{omp, agy}) == agy:
		return agy, "agy"
	}
	return omp, "omp"
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
	// Check agy / top-level token fields (input_tokens + cache_read_tokens + output_tokens)
	sumAgy := 0
	hasAgy := false
	for _, key := range []string{"input_tokens", "cache_read_tokens", "output_tokens"} {
		if part := numberValueOrFloat(record, key); part != nil {
			sumAgy += *part
			hasAgy = true
		}
	}
	if hasAgy && sumAgy > 0 {
		return &sumAgy
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
	if len(found) > 0 {
		return found[len(found)-1][1]
	}
	return ""
}

// seatModelOf is the model a seat runs: the one that answered last in its log, else the one the room's spec.json
// declares for it. An agy transcript names no model, so for an agy seat the spec is the only record.
func seatModelOf(root, room, seat, logPath string) string {
	if model := seatModel(logPath); model != "" {
		return model
	}
	raw, err := os.ReadFile(filepath.Join(root, "rooms", room, "spec.json"))
	if err != nil {
		return ""
	}
	var spec struct {
		Seats []struct {
			SeatKey string `json:"seat_key"`
			Model   string `json:"model"`
		} `json:"seats"`
	}
	if json.Unmarshal(raw, &spec) != nil {
		return ""
	}
	for _, entry := range spec.Seats {
		if entry.SeatKey == seat {
			return entry.Model
		}
	}
	return ""
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
