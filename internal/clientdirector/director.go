// Package clientdirector routes an escalation that would reach the owner to the room's director instead,
// when the room has one.
//
// A room declares a director by holding <roomDir>/director/, the directory the director keeps its ready list
// in (the same declaration the git guard reads: internal/clientguard/director.go). In such a room the founder
// approves steps and the director decides everything else (owner, 2026-10-30): a Jev escalation whose next
// tier is the owner is not raised on the dashboard, it is appended to <roomDir>/director/ready.md as one
// QUESTION line, and only the director raises an owner ask, for what it cannot decide itself.
package clientdirector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
)

// LanesRootEnv is the room's `wt/` the runtime hands every seat; its parent is the room's directory.
const LanesRootEnv = "AGENTHUB_LANES_ROOT"

// lineCap bounds one QUESTION line: ready.md is read line by line, and a line is one item.
const lineCap = 700

// RoomDir is the room's directory: the parent of the lane root the runtime handed this seat when it names
// the same room, else <stateRoot>/rooms/<room>. "" when neither can be named.
func RoomDir(stateRoot, room string, getenv func(string) string) string {
	if getenv != nil {
		if lanes := strings.TrimSpace(getenv(LanesRootEnv)); lanes != "" {
			dir := filepath.Dir(filepath.Clean(lanes))
			if room == "" || filepath.Base(dir) == room {
				return dir
			}
		}
	}
	if stateRoot == "" || room == "" {
		return ""
	}
	return filepath.Join(stateRoot, "rooms", room)
}

// ReadyPath is the director's ready list of a room directory.
func ReadyPath(roomDir string) string { return filepath.Join(roomDir, "director", "ready.md") }

// Has answers whether the room directory declares a director. A directory that cannot be read is no
// declaration.
func Has(roomDir string) bool {
	if roomDir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(roomDir, "director"))
	return err == nil && info.IsDir()
}

// QuestionLine is the ready.md line of one escalation, in the list's own format:
//
//   - <stamp> | QUESTION | <asker> (Jev escalation) | <summary>; options: <id: label, ...>; asker's pick: <id or none>
//
// Several questions are written as `<qid> = <options>` parts and `<qid>=<pick>` picks. Newlines and the
// field separator are flattened so the line stays one item, and the line is clipped to lineCap.
func QuestionLine(stamp, asker, summary string, questions []clientdecisions.Question) string {
	var options, picks []string
	for _, q := range questions {
		labels := make([]string, 0, len(q.Options))
		for _, o := range q.Options {
			labels = append(labels, o.ID+": "+o.Label)
		}
		pick := q.Recommended()
		if pick == "" {
			pick = "none"
		}
		if len(questions) == 1 {
			options = append(options, strings.Join(labels, ", "))
			picks = append(picks, pick)
			continue
		}
		options = append(options, q.ID+" = "+strings.Join(labels, ", "))
		picks = append(picks, q.ID+"="+pick)
	}
	if len(picks) == 0 {
		picks = []string{"none"}
	}
	body := fmt.Sprintf("%s; options: %s; asker's pick: %s", flat(summary), flat(strings.Join(options, "; ")), flat(strings.Join(picks, ", ")))
	line := fmt.Sprintf("- %s | QUESTION | %s (Jev escalation) | %s", flat(stamp), flat(asker), body)
	return clip(line, lineCap)
}

// AppendQuestion appends the line to the room's ready.md (created if missing, inside an existing director
// directory only) and returns the file it wrote.
func AppendQuestion(roomDir, line string) (string, error) {
	if !Has(roomDir) {
		return "", fmt.Errorf("room %s has no director office", roomDir)
	}
	path := ReadyPath(roomDir)
	prefix := ""
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return path, err
	}
	if _, err := file.WriteString(prefix + line + "\n"); err != nil {
		_ = file.Close()
		return path, err
	}
	return path, file.Close()
}

// flat makes text one field: whitespace runs (newlines included) become one space and "|" becomes "/".
func flat(text string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(text), " "), "|", "/")
}

// clip cuts text to at most max runes, ending in "..." when cut.
func clip(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max-3]) + "..."
}
