package seatlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextTokensReadsTotalTokens(t *testing.T) {
	got, ok := ContextTokens(`{"message":{"usage":{"totalTokens":12345}}}`)
	if !ok || got != 12345 {
		t.Errorf("got %d, %v, want 12345", got, ok)
	}
}

func TestContextTokensSumsTheFourCountsWhenThereIsNoTotal(t *testing.T) {
	line := `{"message":{"usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":300,"output_tokens":7}}}`
	got, ok := ContextTokens(line)
	if !ok || got != 607 {
		t.Errorf("got %d, %v, want 607", got, ok)
	}
}

func TestContextTokensReadsACompactionRecordsTokensAfter(t *testing.T) {
	got, ok := ContextTokens(`{"type":"compaction","tokensAfter":42000}`)
	if !ok || got != 42000 {
		t.Errorf("got %d, %v, want 42000", got, ok)
	}
}

func TestContextTokensIsFalseWithoutAUsage(t *testing.T) {
	for _, line := range []string{`{"message":{"role":"assistant"}}`, `not json`, `{}`} {
		if _, ok := ContextTokens(line); ok {
			t.Errorf("%q was read as a token count", line)
		}
	}
}

func TestNewestSessionPicksTheMostRecentlyWrittenLog(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(sessions, "old.jsonl")
	newer := filepath.Join(sessions, "new.jsonl")
	for _, path := range []string{old, newer} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if got := NewestSession(dir); got != newer {
		t.Errorf("newest session = %q, want %q", got, newer)
	}
}

func TestSeatLogPrefersAnOmpStateDirectoryAndFallsBackToClaude(t *testing.T) {
	stateRoot := t.TempDir()
	seatDir := filepath.Join(stateRoot, "rooms", "my-rig", "seats", "lead")
	if err := os.MkdirAll(filepath.Join(seatDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(seatDir, "sessions", "s.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if path, runtime := SeatLog(stateRoot, "my-rig", "lead"); path != log || runtime != "omp" {
		t.Errorf("SeatLog = (%q, %q), want the omp session", path, runtime)
	}
	// No state directory and no configured Claude projects directory: no log, and the runtime says so.
	if path, runtime := SeatLog(stateRoot, "my-rig", "absent"); path != "" || runtime != "claude" {
		t.Errorf("SeatLog = (%q, %q), want no log and the claude fallback", path, runtime)
	}
}

func TestKiloRendersThousandsAndMillions(t *testing.T) {
	if got := Kilo(150_000); got != "150k" {
		t.Errorf("Kilo(150000) = %q", got)
	}
	if got := Kilo(1_200_000); got != "1.20M" {
		t.Errorf("Kilo(1200000) = %q", got)
	}
}
