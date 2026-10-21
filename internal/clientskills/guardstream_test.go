package clientskills

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The guard stream has two producers that spell the same rule differently: the commit wrapper writes
// `docs_only` into its spool, the seat guard writes `docs-only` (and ten other hyphenated kinds) into each
// seat's tool log. Both cases below fail on the reviewer's S2 finding if either half regresses: a `guard:`
// signal a person writes from the guard's own vocabulary must fire.

// TestTheGuardStreamFoldsTheSpoolRule: the spool's `docs_only` matches a declared `docs-only`, and the ref a
// reader follows is the spool line's own id.
func TestTheGuardStreamFoldsTheSpoolRule(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	gitDir := git(t, repo, "rev-parse", "--absolute-git-dir")
	writeFile(t, filepath.Join(gitDir, "parked-diffs", "refusals.jsonl"),
		`{"id":"rf-9","ts":"2026-10-21T10:05:00Z","rule":"docs_only","docs":3,"code":0}`+"\n")

	evidence := LoadEvidence(t.TempDir(), "room", repo)
	since := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	found := evidence.Occurrences(Signal{Kind: SignalGuard, Value: "docs-only"}, since)
	if len(found) != 1 {
		t.Fatalf("occurrences = %d, want 1: the spool's docs_only must match the declared docs-only (%v)",
			len(found), found)
	}
	if found[0].Ref != "rf-9" {
		t.Fatalf("ref = %q, want the spool line's id rf-9", found[0].Ref)
	}
}

// TestTheGuardStreamReadsTheSeatsBlockedToolLines: the seat guard's own log is the producer that actually has
// volume (757 blocked lines measured 2026-10-21), and only the BLOCKED lines are refusals.
func TestTheGuardStreamReadsTheSeatsBlockedToolLines(t *testing.T) {
	stateRoot := t.TempDir()
	writeFile(t, filepath.Join(stateRoot, "rooms", "room", "seats", "a", "tool-calls.jsonl"),
		`{"at":"2026-10-21T10:06:00Z","seat":"a","tool":"bash","outcome":"blocked","rule":"protected-paths"}`+"\n"+
			`{"at":"2026-10-21T10:07:00Z","seat":"a","tool":"bash","outcome":"ok","rule":""}`+"\n")

	evidence := LoadEvidence(stateRoot, "room", "")
	if evidence.Unread(StreamGuard) {
		t.Fatalf("a stream with one readable producer must not be marked unread: %v", evidence.Skips)
	}
	since := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	found := evidence.Occurrences(Signal{Kind: SignalGuard, Value: "protected-paths"}, since)
	if len(found) != 1 {
		t.Fatalf("occurrences = %d, want 1 (the blocked line, not the ok one): %v", len(found), found)
	}
}

// TestAJevOccurrenceCarriesARefAReaderCanFollow: the log writes no id, so the ref is the line's own timestamp
// and tier. A `jev:` ref with nothing after it is a demotion nobody can look up (the S2 verdict's minor).
func TestAJevOccurrenceCarriesARefAReaderCanFollow(t *testing.T) {
	stateRoot := t.TempDir()
	writeFile(t, filepath.Join(stateRoot, jevLogFile),
		`{"timestamp":"2026-10-21T10:04:00Z","kind":"escalation","tier":"seat","outcome":"escalated-to-lead"}`+"\n")

	evidence := LoadEvidence(stateRoot, "room", "")
	since := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	found := evidence.Occurrences(Signal{Kind: SignalJEV, Value: "escalation"}, since)
	if len(found) != 1 {
		t.Fatalf("occurrences = %d, want 1: %v", len(found), found)
	}
	if want := "jev:2026-10-21T10:04:00Z seat"; found[0].Ref != want {
		t.Fatalf("ref = %q, want %q", found[0].Ref, want)
	}
}

// TestTheGuardStreamIsUnreadOnlyWhenNoProducerExists: both producers are append-on-event files, so an absent
// file is measured silence. The stream is unread only when there is nothing to read at all, because a stream
// nobody opened must never promote a skill.
func TestTheGuardStreamIsUnreadOnlyWhenNoProducerExists(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	empty := LoadEvidence(t.TempDir(), "room", repo)
	if !empty.Unread(StreamGuard) {
		t.Fatalf("no spool and no tool log must read as unread, got %v", empty.Skips)
	}

	stateRoot := t.TempDir()
	writeFile(t, filepath.Join(stateRoot, "rooms", "room", "seats", "a", "tool-calls.jsonl"), "")
	present := LoadEvidence(stateRoot, "room", repo)
	if present.Unread(StreamGuard) {
		t.Fatalf("an empty tool log is measured silence, not an unread stream: %v", present.Skips)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "rooms", "room", "seats", "a", "tool-calls.jsonl")); err != nil {
		t.Fatalf("fixture: %v", err)
	}
}
