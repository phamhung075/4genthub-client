package seatlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The record shape an agy 1.4.9 transcript writes (room aa2-rs-peak, 2026-10-28): the counts sit at the top
// level. The supervisor read none of them, so it never saw an agy seat past its limit.
func TestContextTokensReadsAnAgyRecordsTopLevelCounts(t *testing.T) {
	line := `{"step_index":447,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","input_tokens":2629,"cache_read_tokens":168616,"output_tokens":656}`
	got, ok := ContextTokens(line)
	if !ok || got != 171901 {
		t.Errorf("got %d, %v, want 171901", got, ok)
	}
	if _, ok := ContextTokens(`{"type":"GENERIC","status":"DONE"}`); ok {
		t.Error("an agy record with no counts was read as a token count")
	}
}

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompactionsCountsAnAgyCheckpointAndAnOmpCompaction(t *testing.T) {
	path := writeLog(t,
		`{"type":"compaction","tokensAfter":1}`,
		`{"step_index":472,"source":"SYSTEM","type":"CHECKPOINT","status":"DONE","content":"# Resuming from a compaction"}`,
		`{"type":"PLANNER_RESPONSE","status":"DONE"}`)
	if got := Compactions(path); got != 2 {
		t.Errorf("Compactions = %d, want 2", got)
	}
	if got := Compactions(filepath.Join(t.TempDir(), "absent")); got != 0 {
		t.Errorf("an absent log counted %d compactions", got)
	}
}

func TestAgyTurnEndedOnlyOnAFinalAnswer(t *testing.T) {
	final := `{"type":"PLANNER_RESPONSE","status":"DONE","input_tokens":1,"cache_read_tokens":2,"output_tokens":3}`
	cases := []struct {
		name  string
		last  string
		ended bool
	}{
		{"final answer", final, true},
		{"tool call pending", `{"type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"run_command","args":{}}]}`, false},
		{"tool result unanswered", `{"type":"GENERIC","status":"DONE"}`, false},
		{"tool still running", `{"type":"GENERIC","status":"RUNNING"}`, false},
		{"user input unanswered", `{"type":"USER_INPUT","status":"DONE"}`, false},
		{"system message unanswered", `{"type":"SYSTEM_MESSAGE","status":"DONE"}`, false},
		{"unreadable", `not json`, false},
	}
	for _, c := range cases {
		path := writeLog(t, `{"type":"USER_INPUT","status":"DONE"}`, final, c.last)
		if got := AgyTurnEnded(path); got != c.ended {
			t.Errorf("%s: AgyTurnEnded = %v, want %v", c.name, got, c.ended)
		}
	}
	if AgyTurnEnded(filepath.Join(t.TempDir(), "absent")) {
		t.Error("an absent transcript read as a seat at its prompt")
	}
}
