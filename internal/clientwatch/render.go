package clientwatch

import (
	"encoding/json"
	"fmt"
	"math"
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

// toolColors maps a tool name to its colour: reads blue, writes yellow, shell green, MCP magenta,
// anything else white.
var toolColors = map[string]int{
	"read": 117, "grep": 117, "find": 117, "ls": 117, "search": 117,
	"write": 221, "edit": 221, "ast_edit": 221,
	"bash": 120, "eval": 120,
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
// Each line is cut at width characters and at most lines lines are shown; what is hidden is counted,
// never silently dropped.
func block(head, style, text string, width, lines int) string {
	trimmed := strings.TrimRightFunc(text, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' })
	rows := splitLines(trimmed)
	if len(rows) == 0 {
		rows = []string{""}
	}
	out := []string{head + style + cutRunes(rows[0], width) + reset}
	for _, row := range rows[1:min(lines, len(rows))] {
		out = append(out, "    "+style+cutRunes(row, width)+reset)
	}
	if len(rows) > lines {
		out = append(out, "    "+fg(stampColor)+fmt.Sprintf("… +%d more lines", len(rows)-lines)+reset)
	}
	return strings.Join(out, "\n")
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
func callBody(args any) (string, string) {
	object, ok := args.(*orderedJSONObject)
	if !ok {
		return "", pythonText(args)
	}
	for _, key := range []string{"command", "path", "intent", "prompt", "code"} {
		value, present := object.get(key)
		if !present {
			continue
		}
		others := []string{}
		for _, candidate := range object.keys {
			if candidate != key {
				others = append(others, candidate)
			}
		}
		extra := ""
		if len(others) > 0 {
			extra = " (+" + strings.Join(others, ", ") + ")"
		}
		return extra, key + "=" + pythonText(value)
	}
	return "", indentPython(object)
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
	out := []string{}
	for _, converted := range claudeToOMP(record) {
		out = append(out, ompEvents(converted, width, detail, lines)...)
	}
	return out
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
		head120 := cutRunes(text, 120)
		switch {
		case strings.HasPrefix(text, "Tool ") && strings.Contains(head120, "is blocked by tool policy"):
			return []string{block("\033[1;38;5;231;48;5;160m ✗ BLOCKED "+reset, fg(210), body, width, lines)}
		default:
			isError, _ := msg.get("isError")
			if flag, _ := isError.(bool); flag {
				return []string{block(bold+fg(203)+"✗ "+reset, fg(203), body, width, lines)}
			}
			return []string{block(fg(resultColor)+"← "+reset, fg(resultColor), body, width, lines)}
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
		colour := defaultToolColor
		if strings.HasPrefix(name, "mcp__") {
			colour = mcpColor
		} else if mapped, known := toolColors[name]; known {
			colour = mapped
		}
		arguments, _ := part.get("arguments")
		extra, body := callBody(arguments)
		out = append(out, block(fmt.Sprintf("%s%s→ %s%s%s ", bold, fg(colour), name, extra, reset), fg(255), body, width, lines))
	}
	return out
}

// Header is the seat name and its token bar as a line of the feed, repeated when the reading moves.
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

	// now and out are the clock and the sink, so a test can drive the repeat rule without waiting.
	now func() time.Time
	out func(string)
}

const headerRepeat = 20 * time.Second

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
	if title != h.lastText && waited >= headerRepeat {
		h.out(title)
		h.lastText = title
		h.lastAt = h.now()
	}
}
