package clientwatch

import (
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/seatloop"
)

// seatEventsTail is how much of a seat's log one reading takes. The guard's window is minutes, and the file may be
// gigabytes.
const seatEventsTail = 2_000_000

// SeatEvents reads the tail of a seat's session log into the runtime-neutral events the loop guard counts, through
// the SAME adapters the feed renders: agyToOMP for an agy transcript and claudeToOMP for everything else, so a
// runtime the feed can show is a runtime the guard can read. A log that cannot be read answers no events: a guard
// that could not look does not act.
func SeatEvents(path string) []seatloop.Event {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	offset := info.Size() - seatEventsTail
	if offset < 0 {
		offset = 0
	}
	raw := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(raw, offset); err != nil {
		return nil
	}
	lines := splitLines(strings.ToValidUTF8(string(raw), ""))
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line was cut by the offset
	}
	pairs := newAgyPairs()
	var events []seatloop.Event
	for _, line := range lines {
		events = append(events, lineEvents(line, pairs, info.ModTime())...)
	}
	return events
}

// lineEvents is one log line's events. A record that carries no time is stamped when the log was last written.
func lineEvents(line string, pairs *agyPairs, written time.Time) []seatloop.Event {
	decoded, _, err := decodeOrderedJSON(line)
	if err != nil {
		return nil
	}
	record, ok := decoded.(*orderedJSONObject)
	if !ok {
		return nil
	}
	at := written
	for _, key := range []string{"timestamp", "created_at"} {
		if value, present := record.get(key); present {
			if text, ok := value.(string); ok {
				if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
					at = parsed
					break
				}
			}
		}
	}
	converted, isAgy := agyToOMP(record, pairs)
	if !isAgy {
		converted = claudeToOMP(record)
	}
	var events []seatloop.Event
	for _, omp := range converted {
		events = append(events, ompRecordEvents(omp, at)...)
	}
	return events
}

// ompRecordEvents reads one omp-shaped record, the shape both adapters write.
func ompRecordEvents(record *orderedJSONObject, at time.Time) []seatloop.Event {
	rawMessage, _ := record.get("message")
	message, ok := rawMessage.(*orderedJSONObject)
	if !ok {
		return nil
	}
	role, _ := message.get("role")
	rawContent, _ := message.get("content")
	content, _ := rawContent.([]any)
	switch role {
	case "toolResult":
		id, _ := message.get("toolCallId")
		idText, _ := id.(string)
		failed, _ := message.get("isError")
		failedFlag, _ := failed.(bool)
		pieces := []string{}
		for _, entry := range content {
			if part, ok := entry.(*orderedJSONObject); ok {
				if text, present := part.get("text"); present {
					pieces = append(pieces, pythonText(text))
				}
			}
		}
		return []seatloop.Event{{Kind: seatloop.Result, ID: idText, Output: strings.Join(pieces, " "), Failed: failedFlag, At: at}}
	case "user":
		return []seatloop.Event{{Kind: seatloop.Input, At: at}}
	case "assistant":
		var events []seatloop.Event
		for _, entry := range content {
			part, ok := entry.(*orderedJSONObject)
			if !ok {
				continue
			}
			if kind, _ := part.get("type"); kind != "toolCall" {
				continue
			}
			name, _ := part.get("name")
			id, _ := part.get("id")
			idText, _ := id.(string)
			arguments, _ := part.get("arguments")
			args, label := callIdentity(arguments)
			events = append(events, seatloop.Event{Kind: seatloop.Call, Tool: pythonText(name), Args: args, Label: label, ID: idText, At: at})
		}
		return events
	}
	return nil
}

// callIdentity is what makes two calls the same call, and the call as a person reads it. The intent ("i") is left
// out of both: it is the model's own sentence about the call, and it changes while the call does not.
func callIdentity(raw any) (string, string) {
	object, ok := raw.(*orderedJSONObject)
	if !ok {
		text := pythonText(raw)
		return text, text
	}
	identity, label := []string{}, []string{}
	for index, key := range object.keys {
		if key == "i" {
			continue
		}
		value := pythonText(object.values[index])
		identity = append(identity, key+"="+value)
		label = append(label, value)
	}
	return strings.Join(identity, "\n"), strings.Join(strings.Fields(strings.Join(label, " ")), " ")
}
