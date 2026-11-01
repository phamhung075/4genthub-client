package clientwatch

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The agy adapter: an Antigravity CLI transcript record becomes the omp record the one renderer takes, so an agy
// seat's pane reads like an omp seat's. Styling stays in render.go and view.go; this file only knows agy's shapes:
//
//   - a tool has its own name and argument keys (run_command / CommandLine for bash / command);
//   - a string argument is JSON-encoded a second time ("\"/a/b\""), and may be cut by the log (<truncated N bytes>);
//   - a result carries no tool name or call id, only its place after the call, and wraps the output in an envelope.

// agyCall is a call whose result has not been seen: the id its result is tied to and the omp tool it became.
type agyCall struct{ id, tool string }

// agyPairs ties a seat's results to its calls. A result names neither, and a user input or system message can come
// between a call and its result, so a result takes the oldest open call. A transcript is read more than once (the
// replay filter, the replay, a redraw after a resize), so a step already seen answers from memory: every pass pairs
// the same way. One per seat, for one transcript.
type agyPairs struct {
	open     []agyCall
	answered map[int]agyCall
	lastCall int
}

func newAgyPairs() *agyPairs { return &agyPairs{answered: map[int]agyCall{}, lastCall: -1} }

func (p *agyPairs) called(step int, calls []agyCall) {
	if p == nil || step <= p.lastCall {
		return
	}
	p.lastCall = step
	p.open = append(p.open, calls...)
}

func (p *agyPairs) result(step int) agyCall {
	if p == nil || step < 0 {
		return agyCall{}
	}
	if call, seen := p.answered[step]; seen {
		return call
	}
	var call agyCall
	if len(p.open) > 0 {
		call, p.open = p.open[0], p.open[1:]
	}
	p.answered[step] = call
	return call
}

// agyToOMP maps an agy transcript record onto omp records. The second result is false for a record that is not one.
func agyToOMP(record *orderedJSONObject, pairs *agyPairs) ([]*orderedJSONObject, bool) {
	kind, _ := record.get("type")
	step := -1
	if number := numberValue(record, "step_index"); number != nil {
		step = *number
	}
	switch kind {
	case "PLANNER_RESPONSE":
		parts := []any{}
		for _, field := range []string{"thinking", "text"} {
			source := field
			if field == "text" {
				source = "content"
			}
			if value, ok := record.get(source); ok {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, ordered("type", field, field, text))
				}
			}
		}
		calls := []agyCall{}
		if rawCalls, ok := record.get("tool_calls"); ok {
			list, _ := rawCalls.([]any)
			for _, entry := range list {
				object, ok := entry.(*orderedJSONObject)
				if !ok {
					continue
				}
				rawName, _ := object.get("name")
				name, _ := rawName.(string)
				args, present := object.get("args")
				if !present {
					args, _ = object.get("arguments")
				}
				tool, arguments := agyCallToOMP(name, args)
				call := agyCall{id: fmt.Sprintf("agy-%d-%d", step, len(calls)), tool: tool}
				calls = append(calls, call)
				parts = append(parts, ordered("type", "toolCall", "id", call.id, "name", tool, "arguments", arguments))
			}
		}
		pairs.called(step, calls)
		return []*orderedJSONObject{ordered("message", ordered("role", "assistant", "content", parts))}, true
	case "GENERIC":
		return []*orderedJSONObject{agyResultToOMP(record, pairs.result(step))}, true
	case "USER_INPUT":
		text := ""
		if value, ok := record.get("content"); ok {
			text = pythonText(value)
		}
		return []*orderedJSONObject{wrapContent(nil, "user", []any{ordered("type", "text", "text", text)})}, true
	}
	return nil, false
}

// ordered builds a JSON object from key, value pairs.
func ordered(pairs ...any) *orderedJSONObject {
	object := &orderedJSONObject{}
	for index := 0; index+1 < len(pairs); index += 2 {
		object.keys = append(object.keys, pairs[index].(string))
		object.values = append(object.values, pairs[index+1])
	}
	return object
}

// agyUnescape reads the escapes of a JSON string the log cut before its closing quote.
var agyUnescape = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\t`, "\t", `\r`, "\r", `<`, "<", `>`, ">", `&`, "&")

// agyText decodes the second JSON encoding of a string argument. A number or a flag has none and stays as it is.
func agyText(raw string) string {
	if !strings.HasPrefix(raw, `"`) {
		return raw
	}
	var decoded string
	if json.Unmarshal([]byte(raw), &decoded) == nil {
		return decoded
	}
	return agyUnescape.Replace(strings.TrimSuffix(raw[1:], `"`))
}

// agyArgs is a call's arguments with every string decoded, in the order agy wrote them.
func agyArgs(raw any) *orderedJSONObject {
	decoded := &orderedJSONObject{}
	object, ok := raw.(*orderedJSONObject)
	if !ok {
		if raw != nil {
			decoded.keys = append(decoded.keys, "args")
			decoded.values = append(decoded.values, raw)
		}
	} else {
		decoded.keys = append(decoded.keys, object.keys...)
		decoded.values = append(decoded.values, object.values...)
	}
	for index, value := range decoded.values {
		if text, ok := value.(string); ok {
			decoded.values[index] = agyText(text)
		}
	}
	return decoded
}

func agyArg(args *orderedJSONObject, key string) string {
	value, present := args.get(key)
	if !present {
		return ""
	}
	return pythonText(value)
}

// agyPath is a path as omp writes it: no file:// scheme, and a line range as path:start-end.
func agyPath(args *orderedJSONObject, key string) string {
	path := strings.TrimPrefix(agyArg(args, key), "file://")
	start, end := agyArg(args, "StartLine"), agyArg(args, "EndLine")
	switch {
	case start != "" && end != "":
		return path + ":" + start + "-" + end
	case start != "":
		return path + ":" + start
	case end != "":
		return path + ":1-" + end
	}
	return path
}

func agyRead(key string) func(*orderedJSONObject) (string, *orderedJSONObject) {
	return func(args *orderedJSONObject) (string, *orderedJSONObject) {
		return "read", ordered("path", agyPath(args, key))
	}
}

// agyShortTask is a task id without the conversation in front of it.
func agyShortTask(id string) string { return id[strings.LastIndex(id, "/")+1:] }

// agyTools maps each agy tool found in real transcripts onto the omp tool that does the same work. A tool with no
// omp counterpart keeps its name and says what it does in one summary line. Anything else takes agyGeneric.
var agyTools = map[string]func(*orderedJSONObject) (string, *orderedJSONObject){
	"run_command": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		return "bash", ordered("command", agyArg(args, "CommandLine"))
	},
	"read_file": agyRead("Path"),
	"view_file": agyRead("AbsolutePath"),
	"write_file": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		return "write", ordered("path", agyPath(args, "Path"), "content", agyArg(args, "Content"))
	},
	"edit_file": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		if _, hasTarget := args.get("Target"); !hasTarget {
			return "write", ordered("path", agyPath(args, "Path"), "content", agyArg(args, "Content"))
		}
		return "edit", ordered("path", agyPath(args, "Path"), "old_string", agyArg(args, "Target"), "new_string", agyArg(args, "Replacement"))
	},
	"search_web": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		query := agyArg(args, "query")
		if domain := agyArg(args, "domain"); domain != "" {
			query += "  site:" + domain
		}
		return "web_search", ordered("query", query)
	},
	"invoke_subagent": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		return "task", ordered("i", agyArg(args, "TypeName"), "prompt", agyArg(args, "Prompt"))
	},
	"call_mcp_tool": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		name := "mcp__" + agyArg(args, "ServerName") + "__" + agyArg(args, "ToolName")
		text := agyArg(args, "Arguments")
		if decoded, _, err := decodeOrderedJSON(text); err == nil {
			if object, ok := decoded.(*orderedJSONObject); ok {
				return name, object
			}
		}
		return name, ordered("summary", text)
	},
	"manage_task": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		return "manage_task", ordered("summary", strings.TrimSpace(agyArg(args, "Action")+" "+agyShortTask(agyArg(args, "TaskId"))))
	},
	"schedule": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		return "schedule", ordered("summary", "in "+agyArg(args, "DurationSeconds")+"s: "+agyArg(args, "Prompt"))
	},
	"send_message": func(args *orderedJSONObject) (string, *orderedJSONObject) {
		return "send_message", ordered("i", "to "+agyArg(args, "Recipient"), "prompt", agyArg(args, "Message"))
	},
}

// agyHidden are arguments that are agy's bookkeeping, not what a call does.
var agyHidden = map[string]bool{"toolSummary": true, "ArtifactMetadata": true, "WaitMsBeforeAsync": true}

// agyGeneric is a tool nobody has mapped yet: its name and one "key: value" row per argument, decoded.
func agyGeneric(name string, args *orderedJSONObject) (string, *orderedJSONObject) {
	rows := []string{}
	for index, key := range args.keys {
		if !agyHidden[key] {
			rows = append(rows, key+": "+pythonText(args.values[index]))
		}
	}
	return name, ordered("summary", strings.Join(rows, "\n"))
}

// agyCallToOMP is the omp tool name and arguments of an agy call. agy's toolSummary is what omp calls the intent.
func agyCallToOMP(name string, raw any) (string, *orderedJSONObject) {
	args := agyArgs(raw)
	mapper, known := agyTools[name]
	if !known {
		mapper = func(args *orderedJSONObject) (string, *orderedJSONObject) { return agyGeneric(name, args) }
	}
	tool, arguments := mapper(args)
	if intent := agyArg(args, "toolSummary"); intent != "" {
		arguments.keys = append(arguments.keys, "i")
		arguments.values = append(arguments.values, intent)
	}
	return tool, arguments
}

// agyEnvelopeLines head a result without being its output. The file-reading ones come before numbered lines, and a
// command's output starts only after "Command output:", so output that looks like these is never dropped.
var agyEnvelopeLines = []string{
	"Created At: ", "Completed At: ",
	"The tool's output was truncated due to being too long.", "Here is a preview with the middle of the output truncated:",
	"Showing lines ", "File Path: `", "Total Lines: ", "Total Bytes: ",
	"The following code has been modified to include a line number before every line",
}

const (
	agyBackgroundHead = "Tool is running as a background task with task id: "
	agyTaskHead       = "Task: "
	agyStatusHead     = "Status: "
	agyExitHead       = "Command exited with code "
	agyOutputHead     = "Command output:"
	agyNoOutput       = "No output from the command."
	agyStepError      = "Encountered error in step execution: "
	agyLogHead        = "\nLog output:\n"
	// agyNudge starts the instructions agy appends for its model after a background task's log.
	agyNudge = "YOU MUST TAKE ONE OF THE FOLLOWING"
)

// agyResult is what the envelope of a result holds.
type agyResult struct {
	output string
	exit   int
	wallMs float64
}

// agyLogOutput is the log a task report carries, without the instructions agy appends for its model.
func agyLogOutput(text string) string {
	_, log, found := strings.Cut(text, agyLogHead)
	if !found {
		return ""
	}
	if at := strings.LastIndex(log, agyNudge); at >= 0 {
		log = log[:at]
	}
	return strings.TrimRight(log, " \t\r\n")
}

// agyWallFloor is the shortest call whose time is worth showing: the stamps have whole seconds, so a 1s gap may be a
// call of a few milliseconds that crossed a second. Below it a call reads as quick, as it does on omp.
const agyWallFloor = 2 * time.Second

// agyUnwrap strips the envelope off a result, keeping the output, the exit code and how long the call took.
func agyUnwrap(content string) agyResult {
	result := agyResult{}
	var created, completed time.Time
	rest := content
	for {
		line, tail, more := strings.Cut(rest, "\n")
		trimmed := strings.TrimRight(line, "\r")
		envelope := strings.TrimSpace(trimmed) == ""
		for _, head := range agyEnvelopeLines {
			envelope = envelope || strings.HasPrefix(trimmed, head)
		}
		if !envelope {
			break
		}
		if stamp, ok := strings.CutPrefix(trimmed, "Created At: "); ok {
			created, _ = time.Parse(time.RFC3339, stamp)
		}
		if stamp, ok := strings.CutPrefix(trimmed, "Completed At: "); ok {
			completed, _ = time.Parse(time.RFC3339, stamp)
		}
		if !more {
			rest = ""
			break
		}
		rest = tail
	}
	if took := completed.Sub(created); !created.IsZero() && !completed.IsZero() && took >= agyWallFloor {
		result.wallMs = float64(took.Milliseconds())
	}
	first, tail, _ := strings.Cut(rest, "\n")
	first = strings.TrimRight(first, "\r")
	switch {
	case strings.HasPrefix(first, agyExitHead):
		code, _ := strings.CutSuffix(strings.TrimPrefix(first, agyExitHead), ".")
		result.exit, _ = strconv.Atoi(code)
		tail = strings.TrimLeft(tail, "\r\n")
		if output, found := strings.CutPrefix(tail, agyOutputHead); found {
			result.output = strings.TrimPrefix(strings.TrimPrefix(output, "\r"), "\n")
		} else if strings.TrimSpace(tail) != agyNoOutput {
			result.output = tail
		}
	case strings.HasPrefix(first, agyBackgroundHead):
		id := agyShortTask(strings.TrimPrefix(first, agyBackgroundHead))
		result.output = strings.TrimRight("background "+id+"\n"+agyLogOutput(rest), "\n")
	case strings.HasPrefix(first, agyTaskHead) && strings.HasPrefix(tail, agyStatusHead):
		status, _, _ := strings.Cut(strings.TrimPrefix(tail, agyStatusHead), "\n")
		id := agyShortTask(strings.TrimPrefix(first, agyTaskHead))
		result.output = strings.TrimRight(id+" "+strings.TrimSpace(status)+"\n"+agyLogOutput(rest), "\n")
	default:
		result.output = strings.TrimPrefix(rest, agyStepError)
	}
	return result
}

// agyResultToOMP is a result as omp logs it: the output alone, the tool and call it answers, and the exit code and
// wall time under details, where the renderer reads them. As on omp, a command that exited non-zero is a failure.
func agyResultToOMP(record *orderedJSONObject, call agyCall) *orderedJSONObject {
	content := ""
	if value, ok := record.get("content"); ok {
		content = pythonText(value)
	}
	result := agyUnwrap(content)
	status, _ := record.get("status")
	failed := status == "ERROR" || status == "INVALID" || result.exit != 0
	details := ordered("wallTimeMs", json.Number(strconv.FormatFloat(result.wallMs, 'f', -1, 64)), "exitCode", json.Number(strconv.Itoa(result.exit)))
	inner := ordered("role", "toolResult", "toolName", call.tool, "toolCallId", call.id, "isError", failed,
		"details", details, "content", []any{ordered("text", result.output)})
	return ordered("message", inner)
}
