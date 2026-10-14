package clientwatch

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func plain(text string) string { return ansiSequence.ReplaceAllString(text, "") }

func TestLineDiffCountsAddedAndRemovedLines(t *testing.T) {
	_, added, removed := lineDiff("a\nb\nc", "a\nB\nc\nd")
	if added != 2 || removed != 1 {
		t.Errorf("+%d -%d, want +2 -1", added, removed)
	}
}

func TestAResultIsFoldedUnlessExpanded(t *testing.T) {
	rows := []string{}
	for n := range 12 {
		rows = append(rows, fmt.Sprintf("row %d", n))
	}
	line := toolResult(strings.Join(rows, "\n"))
	folded := plain(strings.Join(eventsWith(line, renderOptions{width: 200, lines: 60}), "\n"))
	if strings.Contains(folded, "row 11") {
		t.Errorf("a long result was not folded: %q", folded)
	}
	full := plain(strings.Join(eventsWith(line, renderOptions{width: 200, lines: 60, expand: true}), "\n"))
	if !strings.Contains(full, "row 11") {
		t.Errorf("--expand dropped rows: %q", full)
	}
}

func TestOnlyFilterDropsOtherKinds(t *testing.T) {
	line := toolResult("hello")
	if got := eventsWith(line, renderOptions{width: 200, lines: 60, only: map[string]bool{"write": true}}); len(got) != 0 {
		t.Errorf("a result passed --only write: %q", got)
	}
}

func TestSeatViewStampShowsTheMinuteOnlyWhenItChanges(t *testing.T) {
	view := &seatView{}
	first := time.Date(2026, 10, 14, 9, 5, 10, 0, time.Local)
	if got := plain(view.stamp(first)); !strings.Contains(got, "09:05") {
		t.Errorf("first stamp %q", got)
	}
	if got := plain(view.stamp(first.Add(20 * time.Second))); strings.Contains(got, "09:05") {
		t.Errorf("same minute repeated: %q", got)
	}
	if got := plain(view.stamp(first.Add(2 * time.Minute))); !strings.Contains(got, "09:07") {
		t.Errorf("new minute missing: %q", got)
	}
}

func TestHeaderStatusIsWorkingThenQuiet(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local)
	h := &Header{now: func() time.Time { return now }}
	if h.status() != "" {
		t.Error("a seat with no event has a status")
	}
	h.touch(now.Add(-5 * time.Second))
	if !strings.Contains(plain(h.status()), "working") {
		t.Errorf("status %q", plain(h.status()))
	}
	h.lastEvent = now.Add(-7 * time.Minute)
	if !strings.Contains(plain(h.status()), "quiet 7m") {
		t.Errorf("status %q", plain(h.status()))
	}
}

func TestOnlyRefusesAnUnknownKind(t *testing.T) {
	if err := (shared{only: "write,bogus"}).checkKinds(); err == nil {
		t.Error("an unknown kind was accepted")
	}
	if err := (shared{only: "write,shell"}).checkKinds(); err != nil {
		t.Error(err)
	}
}

func TestAChangeOfToolKindGetsALabelledRule(t *testing.T) {
	read, ok := callKind(fg(117) + "◉ read" + reset + " path")
	if !ok || read.kind != "read" {
		t.Fatalf("read call not recognised: %v %v", read, ok)
	}
	if _, ok := callKind(fg(249) + "← read" + reset); ok {
		t.Error("a result was taken for a call")
	}
	rule := plain(kindRule(writeStyle, 80))
	if !strings.Contains(rule, "✎ write") || !strings.HasPrefix(rule, "──") {
		t.Errorf("rule = %q", rule)
	}
}
