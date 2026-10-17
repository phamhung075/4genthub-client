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
	// No state directory and no configured Claude projects directory: no log, so no runtime is named.
	if path, runtime := SeatLog(stateRoot, "my-rig", "absent"); path != "" || runtime != "" {
		t.Errorf("SeatLog = (%q, %q), want no log", path, runtime)
	}
}

// TestSeatLogReadsAnAgySeatsTranscriptBesideAnOmpEraLog pins the third false alarm of 2026-10-17: the
// reviewer's seat directory still holds the session file of the omp runtime it no longer runs, so a
// resolver that prefers any omp jsonl reads tens of thousands of seconds of silence while the agy
// transcript was written minutes earlier. The newer of the two logs is the live one; only a seat with
// neither falls through to the Claude Code log.
func TestSeatLogReadsAnAgySeatsTranscriptBesideAnOmpEraLog(t *testing.T) {
	stateRoot := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	seatDir := filepath.Join(stateRoot, "rooms", "my-rig", "seats", "reviewer")
	sessions := filepath.Join(seatDir, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(sessions, "2026-10-16T22-47.jsonl")
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-14 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	const conversation = "agy-conversation-1"
	if err := os.WriteFile(filepath.Join(seatDir, "agy-conversation"), []byte(conversation+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(home, ".gemini", "antigravity-cli", "brain", conversation,
		".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if path, runtime := SeatLog(stateRoot, "my-rig", "reviewer"); path != transcript || runtime != "agy" {
		t.Errorf("SeatLog = (%q, %q), want the agy transcript", path, runtime)
	}

	// An omp log written after the transcript still wins: the rule is the newer log, not the runtime.
	newer := filepath.Join(sessions, "2026-10-17T08-37.jsonl")
	if err := os.WriteFile(newer, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if path, runtime := SeatLog(stateRoot, "my-rig", "reviewer"); path != newer || runtime != "omp" {
		t.Errorf("SeatLog = (%q, %q), want the newer omp log", path, runtime)
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
