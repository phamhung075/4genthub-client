package clientwatch

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Terminal rendering. Every colour is a light 256-colour tone: readable on a dark background, which is
// what a seat's terminal is.

const (
	reset = "\033[0m"
	bold  = "\033[1m"

	seatColorCount   = 10
	mcpColor         = 213
	defaultToolColor = 255
	resultColor      = 249
	stampColor       = 245
	barCells         = 16
)

// seatColors cycle per seat, in the Python's order.
var seatColors = []int{203, 114, 221, 75, 213, 87, 215, 183, 120, 229}

// toolStyle is how a tool call looks: a glyph and a colour per kind of work, so a feed is read by shape.
type toolStyle struct {
	glyph  string
	colour int
}

var (
	readStyle     = toolStyle{"◉", 117}
	searchStyle   = toolStyle{"⌕", 81}
	writeStyle    = toolStyle{"✎", 221}
	shellStyle    = toolStyle{"❯", 120}
	webStyle      = toolStyle{"⌁", 75}
	delegateStyle = toolStyle{"⇉", 183}
	askStyle      = toolStyle{"?", 215}
	mcpStyle      = toolStyle{"⬡", mcpColor}
	otherStyle    = toolStyle{"→", defaultToolColor}
)

// toolStyles maps a tool name to its style: reads blue, searches cyan, writes yellow, shell green,
// web and delegation their own, MCP magenta, anything else white.
var toolStyles = map[string]toolStyle{
	"read": readStyle, "ls": readStyle,
	"grep": searchStyle, "find": searchStyle, "glob": searchStyle, "search": searchStyle, "ast_grep": searchStyle,
	"write": writeStyle, "edit": writeStyle, "ast_edit": writeStyle,
	"bash": shellStyle, "eval": shellStyle,
	"web_search": webStyle, "fetch": webStyle, "browser": webStyle,
	"task": delegateStyle, "todo_write": delegateStyle,
	"ask": askStyle,
}

func fg(code int) string { return fmt.Sprintf("\033[38;5;%dm", code) }

// kilo is Python's f"{n/1000:.0f}k" / f"{n/1e6:.2f}M".
func kilo(n int) string {
	if n < 1_000_000 {
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
}

// tokenBar is a bar of the context filled so far, its percent, and tokens used of the compaction point.
func tokenBar(tokens *int, limit int) string {
	if tokens == nil {
		return fg(stampColor) + "ctx …" + reset
	}
	pct := float64(*tokens) / float64(limit)
	if pct >= 1 {
		return fmt.Sprintf("%s\033[1;38;5;231;48;5;160m LIMIT REACHED %s %s%s%s/%s%s %scompacts when the job ends%s",
			bold, reset, bold, fg(203), kilo(*tokens), kilo(limit), reset, fg(resultColor), reset)
	}
	cells := int(math.Min(barCells, math.RoundToEven(pct*barCells)))
	colour := 120
	if pct >= 0.8 {
		colour = 203
	} else if pct >= 0.6 {
		colour = 221
	}
	bar := strings.Repeat("█", cells) + strings.Repeat("░", barCells-cells)
	return fmt.Sprintf("%s%s%s %s%s%.0f%%%s %s%s/%s to compact%s",
		fg(colour), bar, reset, bold, fg(colour), pct*100, reset, fg(resultColor), kilo(*tokens), kilo(limit), reset)
}

// barTag is the short percent that prefixes every event line, so the meter is never scrolled out of
// view. Empty until a reading is known.
func barTag(tokens *int, limit int) string {
	if tokens == nil {
		return ""
	}
	pct := float64(*tokens) / float64(limit)
	colour := 120
	if pct >= 0.8 {
		colour = 203
	} else if pct >= 0.6 {
		colour = 221
	}
	return fmt.Sprintf("%s%s%4.0f%%%s %s%5s%s ", bold, fg(colour), pct*100, reset, fg(resultColor), kilo(*tokens), reset)
}

// splitLines is Python's str.splitlines for the shapes a log produces: a trailing separator does not
// add a line, and a "\r\n" is one break.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

// indentCutJSON indents JSON that was cut off mid-value, which no parser accepts: it breaks at every
// structural comma, brace and bracket that is outside a string.
func indentCutJSON(text string) string {
	var out strings.Builder
	depth := 0
	inString := false
	escaped := false
	pending := []rune{}
	flush := func() {
		for _, r := range pending {
			out.WriteRune(r)
		}
		pending = pending[:0]
	}
	writeIndent := func(level int) {
		out.WriteString("\n")
		out.WriteString(strings.Repeat("  ", level))
	}
	for _, ch := range text {
		switch {
		case inString:
			pending = append(pending, ch)
			if ch == '\\' && !escaped {
				escaped = true
				continue
			}
			if ch == '"' && !escaped {
				inString = false
			}
			escaped = false
		case ch == '"':
			pending = append(pending, ch)
			inString = true
		case ch == '{' || ch == '[':
			pending = append(pending, ch)
			flush()
			depth++
			writeIndent(depth)
		case ch == ',':
			pending = append(pending, ch)
			flush()
			writeIndent(depth)
		case ch == '}' || ch == ']':
			depth--
			if depth < 0 {
				depth = 0
			}
			flush()
			writeIndent(depth)
			out.WriteRune(ch)
		default:
			pending = append(pending, ch)
		}
	}
	flush()
	return out.String()
}

// pretty shows a result that starts with JSON indented, whatever follows it (a wall-time line, a
// truncation notice) as it is; JSON cut off by the log is indented as far as it goes.
func pretty(text string) string {
	stripped := strings.TrimSpace(text)
	if stripped == "" || (stripped[0] != '{' && stripped[0] != '[') {
		return text
	}
	value, end, err := decodeOrderedJSON(stripped)
	if err != nil {
		head, tail, found := strings.Cut(stripped, "\n\n")
		if !found {
			return indentCutJSON(stripped)
		}
		return indentCutJSON(head) + "\n\n" + tail
	}
	return indentPython(value) + stripped[end:]
}

// block renders head then text as real lines: the first beside the head, the rest indented.
//
// Lines are wrapped at width characters and indented; at most lines lines are shown.
func block(head, style, text string, width, lines int) string {
	trimmed := strings.TrimRightFunc(text, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' })
	rawRows := splitLines(trimmed)
	if len(rawRows) == 0 {
		rawRows = []string{""}
	}
	var rows []string
	for _, r := range rawRows {
		rows = append(rows, wrapRunes(r, width)...)
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	out := []string{head + style + rows[0] + reset}
	if lines > 0 {
		for _, row := range rows[1:min(lines, len(rows))] {
			out = append(out, "    "+style+row+reset)
		}
		if len(rows) > lines {
			out = append(out, "    "+fg(stampColor)+fmt.Sprintf("… +%d more lines", len(rows)-lines)+reset)
		}
	} else {
		for _, row := range rows[1:] {
			out = append(out, "    "+style+row+reset)
		}
	}
	return strings.Join(out, "\n")
}

// wrapRunes splits text into slices of at most width runes. If width <= 0, no wrapping is applied.
func wrapRunes(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= width {
		return []string{text}
	}
	var out []string
	for len(runes) > width {
		// Look for a whitespace within [width/2, width] to break cleanly at word boundaries
		splitAt := width
		for i := width; i >= width/2 && i > 0; i-- {
			if runes[i-1] == ' ' || runes[i-1] == '\t' {
				splitAt = i
				break
			}
		}
		chunk := strings.TrimRight(string(runes[:splitAt]), " \t")
		out = append(out, chunk)
		runes = runes[splitAt:]
		for len(runes) > 0 && (runes[0] == ' ' || runes[0] == '\t') {
			runes = runes[1:]
		}
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// cutRunes is Python's text[:width]: it counts characters, not bytes.
func cutRunes(text string, width int) string {
	if width < 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:width])
}

// callBody is (the names of the arguments not shown, the main argument as text).
func callBody(args any) string {
	object, ok := args.(*orderedJSONObject)
	if !ok {
		return pythonText(args)
	}
	for _, key := range []string{"command", "CommandLine", "path", "AbsolutePath", "TargetFile", "target_file", "intent", "prompt", "code"} {
		if value, present := object.get(key); present {
			return pythonText(value)
		}
	}
	rest := &orderedJSONObject{}
	for index, key := range object.keys {
		if key != "i" {
			rest.keys = append(rest.keys, key)
			rest.values = append(rest.values, object.values[index])
		}
	}
	return indentPython(rest)
}

// callIntent is the purpose a seat states in the "i" argument of its call.
func callIntent(args any) string {
	if object, ok := args.(*orderedJSONObject); ok {
		if value, present := object.get("i"); present {
			if text, ok := value.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

// mcpDevice is the tool named by the device path a seat reads or writes to reach an MCP tool,
// xd://mcp__<server>_<tool>: a write sends the tool its arguments, a read returns its schema.
func mcpDevice(name string, args any) (string, bool) {
	object, ok := args.(*orderedJSONObject)
	if !ok || (name != "write" && name != "read") {
		return "", false
	}
	path, _ := object.get("path")
	text, _ := path.(string)
	device, found := strings.CutPrefix(text, "xd://mcp__")
	return device, found
}

// pythonText is Python's str() for the values that reach a header: a string is itself, everything else
// is rendered as JSON.
func pythonText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return compactPython(value)
}

// detailLines renders the agent's reasoning, what it says, and what it is told.
func detailLines(role string, part *orderedJSONObject, width, lines int) []string {
	kind, _ := part.get("type")
	kindText, _ := kind.(string)
	switch {
	case role == "assistant" && kindText == "thinking":
		if text, ok := part.get("thinking"); ok {
			if trimmed := strings.TrimSpace(pythonText(text)); trimmed != "" {
				return []string{block(bold+fg(183)+"~ think "+reset, fg(183)+"\033[3m", trimmed, width, lines)}
			}
		}
	case role == "assistant" && kindText == "text":
		if text, ok := part.get("text"); ok {
			trimmed := strings.TrimSpace(pythonText(text))
			if len([]rune(trimmed)) > 1 {
				return []string{block(bold+fg(231)+"▸ say "+reset, fg(255), trimmed, width, lines)}
			}
		}
	case role == "user" && kindText == "text":
		if text, ok := part.get("text"); ok {
			if trimmed := strings.TrimSpace(pythonText(text)); trimmed != "" {
				return []string{block(bold+fg(87)+"◂ in "+reset, fg(123), trimmed, width, lines)}
			}
		}
	}
	return nil
}

// hookColor is the colour of what the runtime injects into a seat on its own, so a reader tells it from
// the seat's own calls.
const hookColor = 215

var systemTags = strings.NewReplacer("<system-reminder>", "", "</system-reminder>", "", "<system-notice>", "", "</system-notice>", "")

// hookMaxLines caps an injected notice: they repeat, and the seat's own work is what the feed is for.
const hookMaxLines = 3

// hookEvents renders what the runtime injects into a seat itself: a reminder or notice, a rule it injected
// and the end of a session. The second result is false for a record that is none of these.
func hookEvents(record *orderedJSONObject, width, lines int) ([]string, bool) {
	kind, _ := record.get("type")
	badge := func(label string) string {
		return "\033[1;38;5;16;48;5;" + fmt.Sprint(hookColor) + "m " + label + " " + reset + " "
	}
	switch kind {
	case "custom_message":
		customType, _ := record.get("customType")
		label, _ := customType.(string)
		content, _ := record.get("content")
		text := strings.TrimSpace(systemTags.Replace(pythonText(content)))
		return []string{block(badge("HOOK "+label), fg(hookColor), text, width, min(lines, hookMaxLines))}, true
	case "ttsr_injection":
		names := []string{}
		if rules, ok := record.get("injectedRules"); ok {
			if list, ok := rules.([]any); ok {
				for _, rule := range list {
					names = append(names, pythonText(rule))
				}
			}
		}
		return []string{block(badge("RULE"), fg(hookColor), strings.Join(names, ", "), width, lines)}, true
	case "custom":
		if customType, _ := record.get("customType"); customType != "session_exit" {
			return nil, false
		}
		reason := ""
		if data, ok := record.get("data"); ok {
			if object, ok := data.(*orderedJSONObject); ok {
				if value, present := object.get("reason"); present {
					reason = pythonText(value)
				}
			}
		}
		return []string{block(badge("EXIT"), fg(hookColor), reason, width, lines)}, true
	}
	return nil, false
}

// events renders one session-log line. A line that is not JSON, or that carries no event, renders
// nothing - the caller tells the two apart by the empty result.
func events(line string, width int, detail bool, lines int) []string {
	decoded, _, err := decodeOrderedJSON(line)
	if err != nil {
		return nil
	}
	record, ok := decoded.(*orderedJSONObject)
	if !ok {
		return nil
	}
	if kind, _ := record.get("type"); kind == "compaction" {
		before := numberValue(record, "tokensBefore")
		after := numberValue(record, "tokensAfter")
		if before != nil && after != nil {
			return []string{fmt.Sprintf("\033[1;38;5;16;48;5;120m COMPACTED %s %s%s%s -> %s%s",
				reset, bold, fg(120), kilo(*before), kilo(*after), reset)}
		}
		return []string{}
	}
	if rendered, isHook := hookEvents(record, width, lines); isHook {
		return rendered
	}
	if converted, isAgy := agyToOMP(record); isAgy {
		out := []string{}
		for _, c := range converted {
			out = append(out, ompEvents(c, width, detail, lines)...)
		}
		return out
	}
	out := []string{}
	for _, converted := range claudeToOMP(record) {
		out = append(out, ompEvents(converted, width, detail, lines)...)
	}
	return out
}

// agyToOMP maps an Antigravity CLI (agy) transcript record onto omp records.
func agyToOMP(record *orderedJSONObject) ([]*orderedJSONObject, bool) {
	kindRaw, present := record.get("type")
	if !present {
		return nil, false
	}
	kind, ok := kindRaw.(string)
	if !ok {
		return nil, false
	}

	switch kind {
	case "PLANNER_RESPONSE":
		parts := []any{}
		if thinking, ok := record.get("thinking"); ok {
			if thStr, ok := thinking.(string); ok && strings.TrimSpace(thStr) != "" {
				p := &orderedJSONObject{}
				p.keys = append(p.keys, "type", "thinking")
				p.values = append(p.values, "thinking", thStr)
				parts = append(parts, p)
			}
		}
		if content, ok := record.get("content"); ok {
			if cStr, ok := content.(string); ok && strings.TrimSpace(cStr) != "" {
				p := &orderedJSONObject{}
				p.keys = append(p.keys, "type", "text")
				p.values = append(p.values, "text", cStr)
				parts = append(parts, p)
			}
		}
		if rawCalls, ok := record.get("tool_calls"); ok {
			if calls, ok := rawCalls.([]any); ok {
				for _, call := range calls {
					callObj, ok := call.(*orderedJSONObject)
					if !ok {
						continue
					}
					name, _ := callObj.get("name")
					args, _ := callObj.get("args")
					if args == nil {
						args, _ = callObj.get("arguments")
					}
					p := &orderedJSONObject{}
					p.keys = append(p.keys, "type", "name", "arguments")
					p.values = append(p.values, "toolCall", name, args)
					parts = append(parts, p)
				}
			}
		}
		inner := &orderedJSONObject{}
		inner.keys = append(inner.keys, "role", "content")
		inner.values = append(inner.values, "assistant", parts)
		wrapper := &orderedJSONObject{}
		wrapper.keys = append(wrapper.keys, "message")
		wrapper.values = append(wrapper.values, inner)
		return []*orderedJSONObject{wrapper}, true

	case "GENERIC":
		text := ""
		if c, ok := record.get("content"); ok {
			text = pythonText(c)
		}
		isErr := false
		if st, ok := record.get("status"); ok {
			if sStr, ok := st.(string); ok && sStr == "ERROR" {
				isErr = true
			}
		}
		return []*orderedJSONObject{toolResultRecord(isErr, text)}, true

	case "USER_INPUT":
		text := ""
		if c, ok := record.get("content"); ok {
			text = pythonText(c)
		}
		part := &orderedJSONObject{}
		part.keys = append(part.keys, "type", "text")
		part.values = append(part.values, "text", text)
		return []*orderedJSONObject{wrapContent(nil, "user", []any{part})}, true
	}

	return nil, false
}

func numberValue(record *orderedJSONObject, key string) *int {
	raw, present := record.get(key)
	if !present {
		return nil
	}
	number, ok := raw.(json.Number)
	if !ok {
		return nil
	}
	value, err := number.Int64()
	if err != nil {
		return nil
	}
	asInt := int(value)
	return &asInt
}

// claudeToOMP maps a Claude Code log record onto omp records: tool_use becomes toolCall, and each
// tool_result becomes its own toolResult.
func claudeToOMP(record *orderedJSONObject) []*orderedJSONObject {
	message, _ := record.get("message")
	msg, ok := message.(*orderedJSONObject)
	if !ok {
		return []*orderedJSONObject{record}
	}
	rawContent, _ := msg.get("content")
	content, ok := rawContent.([]any)
	if !ok {
		return []*orderedJSONObject{record}
	}
	role, _ := msg.get("role")
	roleText, _ := role.(string)
	if roleText == "assistant" {
		parts := []any{}
		for _, entry := range content {
			part, ok := entry.(*orderedJSONObject)
			if !ok {
				parts = append(parts, entry)
				continue
			}
			if kind, _ := part.get("type"); kind == "tool_use" {
				name, _ := part.get("name")
				arguments, _ := part.get("input")
				converted := &orderedJSONObject{}
				converted.keys = append(converted.keys, "type", "name", "arguments")
				converted.values = append(converted.values, "toolCall", name, arguments)
				parts = append(parts, converted)
				continue
			}
			parts = append(parts, part)
		}
		inner := &orderedJSONObject{}
		inner.keys = append(inner.keys, "role", "content")
		inner.values = append(inner.values, "assistant", parts)
		wrapper := &orderedJSONObject{}
		wrapper.keys = append(wrapper.keys, "message")
		wrapper.values = append(wrapper.values, inner)
		return []*orderedJSONObject{wrapper}
	}
	out := []*orderedJSONObject{}
	for _, entry := range content {
		part, ok := entry.(*orderedJSONObject)
		if !ok {
			out = append(out, wrapContent(msg, roleText, []any{entry}))
			continue
		}
		if kind, _ := part.get("type"); kind != "tool_result" {
			out = append(out, wrapContent(msg, roleText, []any{part}))
			continue
		}
		body, _ := part.get("content")
		text := ""
		if items, ok := body.([]any); ok {
			pieces := []string{}
			for _, item := range items {
				if object, ok := item.(*orderedJSONObject); ok {
					if value, present := object.get("text"); present {
						pieces = append(pieces, pythonText(value))
					}
				}
			}
			text = strings.Join(pieces, " ")
		} else {
			text = pythonText(body)
		}
		isError, _ := part.get("is_error")
		flag, _ := isError.(bool)
		out = append(out, toolResultRecord(flag, text))
	}
	return out
}

func wrapContent(msg *orderedJSONObject, role string, content []any) *orderedJSONObject {
	inner := &orderedJSONObject{}
	inner.keys = append(inner.keys, "role", "content")
	inner.values = append(inner.values, role, content)
	for _, key := range []string{"toolName", "isError"} {
		if msg == nil {
			break
		}
		if value, present := msg.get(key); present {
			inner.keys = append(inner.keys, key)
			inner.values = append(inner.values, value)
		}
	}
	wrapper := &orderedJSONObject{}
	wrapper.keys = append(wrapper.keys, "message")
	wrapper.values = append(wrapper.values, inner)
	return wrapper
}

func toolResultRecord(isError bool, text string) *orderedJSONObject {
	part := &orderedJSONObject{}
	part.keys = append(part.keys, "text")
	part.values = append(part.values, text)
	inner := &orderedJSONObject{}
	inner.keys = append(inner.keys, "role", "isError", "content")
	inner.values = append(inner.values, "toolResult", isError, []any{part})
	wrapper := &orderedJSONObject{}
	wrapper.keys = append(wrapper.keys, "message")
	wrapper.values = append(wrapper.values, inner)
	return wrapper
}

// ompEvents renders one omp-shaped record: a tool result (blocked, error, or plain), and the detail
// lines that go with a tool call.
func ompEvents(record *orderedJSONObject, width int, detail bool, lines int) []string {
	message, _ := record.get("message")
	msg, ok := message.(*orderedJSONObject)
	if !ok {
		return nil
	}
	role, _ := msg.get("role")
	roleText, _ := role.(string)
	rawContent, _ := msg.get("content")
	if roleText == "toolResult" {
		pieces := []string{}
		if items, ok := rawContent.([]any); ok {
			for _, item := range items {
				if object, ok := item.(*orderedJSONObject); ok {
					if value, present := object.get("text"); present {
						pieces = append(pieces, pythonText(value))
					}
				}
			}
		}
		text := strings.Join(pieces, " ")
		body := pretty(text)
		tool := ""
		if value, present := msg.get("toolName"); present {
			if name, ok := value.(string); ok && name != "" {
				tool = name + " "
			}
		}
		head120 := cutRunes(text, 120)
		switch {
		case strings.HasPrefix(text, "Tool ") && strings.Contains(head120, "is blocked by tool policy"):
			return []string{block("\033[1;38;5;231;48;5;160m ✗ BLOCKED "+reset, fg(210), body, width, lines)}
		default:
			isError, _ := msg.get("isError")
			if flag, _ := isError.(bool); flag {
				return []string{block(bold+fg(203)+"✗ "+tool+reset, fg(203), body, width, lines)}
			}
			return []string{block(fg(resultColor)+"← "+tool+reset, fg(resultColor), body, width, lines)}
		}
	}
	items, ok := rawContent.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, entry := range items {
		part, ok := entry.(*orderedJSONObject)
		if !ok {
			continue
		}
		if detail {
			out = append(out, detailLines(roleText, part, width, lines)...)
		}
		if kind, _ := part.get("type"); kind != "toolCall" {
			continue
		}
		name := "?"
		if value, present := part.get("name"); present {
			if text, ok := value.(string); ok && text != "" {
				name = text
			}
		}
		style := otherStyle
		if strings.HasPrefix(name, "mcp__") {
			style = mcpStyle
		} else if mapped, known := toolStyles[name]; known {
			style = mapped
		}
		arguments, _ := part.get("arguments")
		label, body := name, callBody(arguments)
		if device, isDevice := mcpDevice(name, arguments); isDevice {
			label, style = "mcp "+device, mcpStyle
			body = ""
			if name == "write" {
				if object, ok := arguments.(*orderedJSONObject); ok {
					if content, present := object.get("content"); present {
						body = pythonText(content)
					}
				}
			}
		}
		head := fmt.Sprintf("%s%s%s %s%s", bold, fg(style.colour), style.glyph, label, reset)
		intent := callIntent(arguments)
		switch {
		case intent == "":
			out = append(out, block(head+" ", fg(255), body, width, lines))
		case body == "":
			out = append(out, head+"  "+fg(250)+"\033[3m"+intent+reset)
		default:
			out = append(out, head+"  "+fg(250)+"\033[3m"+intent+reset+"\n"+block("    ", fg(255), body, width, lines))
		}
	}
	return out
}

// Header is the seat name, model and token bar: a line of the feed, repeated when the reading moves, or
// the title of the pane the feed runs in.
//
// A pinned row needs a terminal scroll region, and lines scrolled inside a region never reach the
// scrollback, so the pane could not be scrolled. A plain line keeps the scrollback.
type Header struct {
	name   string
	colour int
	limit  int
	model  string
	tokens *int

	lastText string
	lastAt   time.Time

	// pane, when set, takes the title instead of the feed: the grid pane's own title carries it.
	pane func(title string)

	// now and out are the clock and the sink, so a test can drive the repeat rule without waiting.
	now func() time.Time
	out func(string)
}

const (
	headerRepeat = 20 * time.Second
	paneRepeat   = 5 * time.Second
)

var ansiSequence = regexp.MustCompile("\033\\[[0-9;]*m")

func newHeader(name string, colour, limit int, model string, out func(string)) *Header {
	return &Header{name: name, colour: colour, limit: limit, model: model, now: time.Now, out: out}
}

func (h *Header) setTokens(tokens *int) { h.tokens = tokens }

func (h *Header) tag() string { return barTag(h.tokens, h.limit) }

func (h *Header) title() string {
	who := h.name
	if h.model != "" {
		who = h.name + " | " + h.model
	}
	return bold + fg(h.colour) + "== " + who + " ==" + reset + "  " + tokenBar(h.tokens, h.limit)
}

// draw prints the bar line when the reading moved and the repeat interval has passed.
func (h *Header) draw() {
	title := h.title()
	waited := h.now().Sub(h.lastAt)
	if h.pane != nil {
		if plain := ansiSequence.ReplaceAllString(title, ""); plain != h.lastText && waited >= paneRepeat {
			h.pane(plain)
			h.lastText = plain
			h.lastAt = h.now()
		}
		return
	}
	if title != h.lastText && waited >= headerRepeat {
		h.out(title)
		h.lastText = title
		h.lastAt = h.now()
	}
}
