package clientdirector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
)

func TestRoomDirPrefersTheLaneRootOfTheSameRoom(t *testing.T) {
	env := map[string]string{LanesRootEnv: "/rooms/aa2-rs/wt"}
	getenv := func(k string) string { return env[k] }
	if got := RoomDir("/state", "aa2-rs", getenv); got != "/rooms/aa2-rs" {
		t.Errorf("same room = %q", got)
	}
	if got := RoomDir("/state", "other", getenv); got != "/state/rooms/other" {
		t.Errorf("other room = %q", got)
	}
	if got := RoomDir("", "", nil); got != "" {
		t.Errorf("nothing named = %q", got)
	}
}

func TestOnlyAnOfficeDirectoryDeclaresADirector(t *testing.T) {
	room := t.TempDir()
	if Has(room) {
		t.Fatal("no office yet")
	}
	if _, err := AppendQuestion(room, "- x"); err == nil {
		t.Error("appended to a room without an office")
	}
	_ = os.WriteFile(filepath.Join(room, "director"), nil, 0o600)
	if Has(room) {
		t.Error("a file named director is not an office")
	}
}

func TestQuestionLineIsOneClippedLine(t *testing.T) {
	qs := []clientdecisions.Question{
		{ID: "a", Question: "?", Options: []clientdecisions.Option{{ID: "x", Label: "X"}, {ID: "y", Label: "Y", Recommended: true}}},
		{ID: "b", Question: "?", Options: []clientdecisions.Option{{ID: "p", Label: "P"}, {ID: "q", Label: "Q"}}},
	}
	line := QuestionLine("2026-10-30 07:00 CET", "idle-guard", "two\nlines | here", qs)
	want := "- 2026-10-30 07:00 CET | QUESTION | idle-guard (Jev escalation) | two lines / here; options: a = x: X, y: Y; b = p: P, q: Q; asker's pick: a=y, b=none"
	if line != want {
		t.Errorf("line =\n%s\nwant\n%s", line, want)
	}
	long := QuestionLine("s", "lead", strings.Repeat("w ", 600), qs)
	if n := len([]rune(long)); n != lineCap || !strings.HasSuffix(long, "...") || strings.Contains(long, "\n") {
		t.Errorf("clipped line: %d runes", n)
	}
}
