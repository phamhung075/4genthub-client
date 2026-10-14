package clientwatch

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The view: what a seat's log is turned into on screen. render.go turns one record into text; this file
// decides how much of it a reader needs - which kinds to keep, how a result is folded, how an edit reads.

const (
	removedColour = 203
	addedColour   = 120

	// resultPreviewLines is how much of a successful result stays on screen unless --expand is given.
	shellPreviewLines = 3
	otherPreviewLines = 6
	diffContext       = 1
	diffCellLimit     = 4_000_000
	trackedCallLimit  = 512
	briefRunes        = 70
)

// viewKinds are the names --only takes: the tool kinds, plus the four things that are not tool calls.
var viewKinds = []string{"read", "search", "write", "shell", "web", "delegate", "ask", "mcp", "other", "think", "say", "in", "hook"}

// renderOptions is everything the rendering of one record depends on.
type renderOptions struct {
	width, lines int
	detail       bool
	// expand keeps every result in full; without it a result is folded to what a reader verifies with.
	expand bool
	// only, when set, keeps just these kinds.
	only map[string]bool
	// errorsOnly keeps failures, refusals and the end of a session.
	errorsOnly bool
	// calls ties a result to its call. Nil when the caller renders without state.
	calls *callTracker
}

func (o renderOptions) keep(kind string) bool { return o.only == nil || o.only[kind] }

// withoutState is the same options with no call tracking, for a pass that only asks whether a record shows anything.
func (o renderOptions) withoutState() renderOptions {
	o.calls = nil
	return o
}

// callInfo is what a result needs of its call: the kind of work and the line that says what it was about.
type callInfo struct {
	kind  string
	brief string
}

// callTracker remembers the calls whose results have not arrived.
type callTracker struct{ open map[string]callInfo }

func newCallTracker() *callTracker { return &callTracker{open: map[string]callInfo{}} }

func (t *callTracker) note(id, kind, body string) {
	if len(t.open) >= trackedCallLimit {
		t.open = map[string]callInfo{}
	}
	t.open[id] = callInfo{kind: kind, brief: briefOf(body)}
}

func (t *callTracker) take(id string) (callInfo, bool) {
	info, ok := t.open[id]
	delete(t.open, id)
	return info, ok
}

// briefOf is the first line of a call's main argument, cut short.
func briefOf(body string) string {
	for _, row := range splitLines(strings.TrimSpace(body)) {
		if row = strings.TrimSpace(row); row != "" {
			return cutRunes(row, briefRunes)
		}
	}
	return ""
}

func toolKind(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		return mcpStyle.kind
	}
	if style, known := toolStyles[name]; known {
		return style.kind
	}
	return otherStyle.kind
}

// duration is how long a call ran, from the wall time the runtime recorded; quick calls say nothing.
func duration(ms float64) string {
	switch {
	case ms >= 1000:
		return fmt.Sprintf("%.1fs", ms/1000)
	case ms >= 200:
		return fmt.Sprintf("%.0fms", ms)
	}
	return ""
}

func floatField(object *orderedJSONObject, key string) (float64, bool) {
	raw, present := object.get(key)
	if !present {
		return 0, false
	}
	if number, ok := raw.(interface{ Float64() (float64, error) }); ok {
		value, err := number.Float64()
		return value, err == nil
	}
	return 0, false
}

// resultLines renders a tool's result: headed by the tool and what the call was about, and folded so that
// a read says how long the file was instead of printing it. A failure always prints in full.
func (o renderOptions) resultLines(msg *orderedJSONObject, tool, body string, failed bool) []string {
	kind := toolKind(tool)
	var info callInfo
	if id, _ := msg.get("toolCallId"); o.calls != nil {
		if callID, ok := id.(string); ok {
			info, _ = o.calls.take(callID)
		}
	}
	if !failed && (o.errorsOnly || !o.keep(kind)) {
		return nil
	}
	label := tool
	if info.brief != "" {
		label += "  " + info.brief
	}
	var wall float64
	var exit string
	if details, ok := msg.get("details"); ok {
		if object, ok := details.(*orderedJSONObject); ok {
			wall, _ = floatField(object, "wallTimeMs")
			if code, present := floatField(object, "exitCode"); present && code != 0 {
				exit = fmt.Sprintf("exit %.0f", code)
			}
		}
	}
	tail := strings.TrimSpace(strings.Join([]string{duration(wall), exit}, " "))
	if tail != "" {
		label += "  " + fg(stampColor) + tail
	}
	if failed {
		return []string{block(bold+fg(203)+"✗ "+label+reset+" ", fg(203), body, o.width, o.lines)}
	}
	if o.expand {
		return []string{block(fg(resultColor)+"← "+label+reset+" ", fg(resultColor), body, o.width, o.lines)}
	}
	rows := len(splitLines(strings.TrimSpace(body)))
	switch kind {
	case "write":
		return nil
	case "read", "search":
		return []string{fmt.Sprintf("%s← %s%s  %s· %d lines%s", fg(resultColor), label, reset, fg(stampColor), rows, reset)}
	case "shell":
		return []string{block(fg(resultColor)+"← "+label+reset+" ", fg(resultColor), body, o.width, shellPreviewLines)}
	}
	return []string{block(fg(resultColor)+"← "+label+reset+" ", fg(resultColor), body, o.width, otherPreviewLines)}
}

// diffRow is one line of an edit as it reads: '-' removed, '+' added, ' ' kept for context, '~' a gap.
type diffRow struct {
	sign byte
	text string
}

// lineDiff compares the old and new text line by line, so an edit that changes one line shows one line
// changed and not the whole block twice. It keeps diffContext unchanged lines around each change.
func lineDiff(removed, added string) (rows []diffRow, plus, minus int) {
	a := splitLines(strings.TrimRight(removed, "\n"))
	b := splitLines(strings.TrimRight(added, "\n"))
	var all []diffRow
	if len(a)*len(b) > diffCellLimit {
		for _, line := range a {
			all = append(all, diffRow{'-', line})
		}
		for _, line := range b {
			all = append(all, diffRow{'+', line})
		}
	} else {
		table := make([][]int, len(a)+1)
		for i := range table {
			table[i] = make([]int, len(b)+1)
		}
		for i := len(a) - 1; i >= 0; i-- {
			for j := len(b) - 1; j >= 0; j-- {
				if a[i] == b[j] {
					table[i][j] = table[i+1][j+1] + 1
				} else {
					table[i][j] = max(table[i+1][j], table[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(a) && j < len(b) {
			switch {
			case a[i] == b[j]:
				all = append(all, diffRow{' ', a[i]})
				i++
				j++
			case table[i+1][j] >= table[i][j+1]:
				all = append(all, diffRow{'-', a[i]})
				i++
			default:
				all = append(all, diffRow{'+', b[j]})
				j++
			}
		}
		for ; i < len(a); i++ {
			all = append(all, diffRow{'-', a[i]})
		}
		for ; j < len(b); j++ {
			all = append(all, diffRow{'+', b[j]})
		}
	}
	near := make([]bool, len(all))
	for index, row := range all {
		if row.sign == ' ' {
			continue
		}
		if row.sign == '+' {
			plus++
		} else {
			minus++
		}
		for k := max(0, index-diffContext); k <= min(len(all)-1, index+diffContext); k++ {
			near[k] = true
		}
	}
	gap := false
	for index, row := range all {
		if near[index] {
			rows = append(rows, row)
			gap = false
		} else if !gap {
			rows = append(rows, diffRow{sign: '~'})
			gap = true
		}
	}
	return rows, plus, minus
}

// diffRows draws the rows: removed in red, added in green, context dim. The line limit counts all of them.
func diffRows(rows []diffRow, width, lines int) string {
	var out []string
	for _, row := range rows {
		switch row.sign {
		case '~':
			out = append(out, "    "+fg(stampColor)+"⋯"+reset)
		case ' ':
			for _, part := range wrapRunes(row.text, width) {
				out = append(out, "    "+fg(stampColor)+"  "+part+reset)
			}
		default:
			colour := removedColour
			if row.sign == '+' {
				colour = addedColour
			}
			for _, part := range wrapRunes(row.text, width) {
				out = append(out, "    "+bold+fg(colour)+string(row.sign)+reset+" "+fg(colour)+part+reset)
			}
		}
	}
	if lines > 0 && len(out) > lines {
		more := len(out) - lines
		out = append(out[:lines], "    "+fg(stampColor)+fmt.Sprintf("… +%d more lines", more)+reset)
	}
	return strings.Join(out, "\n")
}

// terminalColumns is the width of the terminal the feed prints to, or 0 when it is not one.
func terminalColumns() int {
	command := exec.Command("stty", "size")
	command.Stdin = os.Stdout
	output, err := command.Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return 0
	}
	columns, _ := strconv.Atoi(fields[1])
	return columns
}

const (
	prefixColumns = 22
	minLineWidth  = 40
	defaultWidth  = 200
)

// lineWidth is how many characters of a line to keep: the --width given, else what the pane holds.
func lineWidth(requested int) int {
	if requested > 0 {
		return requested
	}
	if columns := terminalColumns(); columns > 0 {
		return max(minLineWidth, columns-prefixColumns)
	}
	return defaultWidth
}

// seatView is what the feed remembers of one seat between events, so the prefix and the repeats can be quiet.
type seatView struct {
	minute    string
	bucket    int
	bucketSet bool
	last      string
	repeated  int
	kind      string // kind of the last tool call shown, so a change of kind is marked
}

// stamp is the clock of a line: shown when the minute changes and blank under it.
func (v *seatView) stamp(at time.Time) string {
	minute := at.Format("15:04")
	if minute == v.minute {
		return strings.Repeat(" ", 5)
	}
	v.minute = minute
	return fg(stampColor) + minute + reset
}

// meter is the percent tag: shown when the context moved a tenth, blank under it.
func (v *seatView) meter(tag string, pct int) string {
	if tag == "" {
		return ""
	}
	bucket := pct / 10
	if v.bucketSet && bucket == v.bucket {
		return strings.Repeat(" ", visibleLen(tag))
	}
	v.bucket, v.bucketSet = bucket, true
	return tag
}

func visibleLen(text string) int { return len([]rune(ansiSequence.ReplaceAllString(text, ""))) }

// callStyles are the styles a tool call can wear, found again by the glyph that heads its first line.
var callStyles = []toolStyle{readStyle, searchStyle, writeStyle, shellStyle, webStyle, delegateStyle, askStyle, mcpStyle, otherStyle}

// callKind is the style of an event that is a tool call, judged by its first glyph; ok is false for a
// result, a hook, a thought or a message.
func callKind(text string) (toolStyle, bool) {
	head := strings.TrimSpace(ansiSequence.ReplaceAllString(strings.SplitN(text, "\n", 2)[0], ""))
	for _, style := range callStyles {
		if strings.HasPrefix(head, style.glyph+" ") {
			return style, true
		}
	}
	return toolStyle{}, false
}

// kindRule is the divider printed when the work changes kind: the kind's glyph and name in its own colour
// on a rule, so a run of reads, then an edit, then a shell command is seen at a glance.
func kindRule(style toolStyle, width int) string {
	label := " " + style.glyph + " " + style.kind + " "
	fill := min(width, 60) - len([]rune(label)) - 2
	if fill < 2 {
		fill = 2
	}
	return fg(style.colour) + "──" + label + strings.Repeat("─", fill) + reset
}
