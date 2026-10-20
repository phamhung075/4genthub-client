package clientwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// PERF-06 (ai_docs/core-architecture/nfr-measurements.md): a line appended to a seat's session log
// must reach the pane's writer within two polls of the feed's loop. The claim is delivery - zero
// line-buffering delay on the feed's stdout path - so the check is a test, not a benchmark: a
// bufio writer in front of out would hold the line until its 4 KB buffer fills or the process ends,
// and this fails. The writer records (arrival, bytes) per Write, so the delay and the shape of the
// delivery - one rendered line in a Write of its own - are both observable.

type timedWrite struct {
	at   time.Time
	text string
}

type timingWriter struct {
	mu     sync.Mutex
	writes []timedWrite
}

func (w *timingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes = append(w.writes, timedWrite{at: time.Now(), text: string(p)})
	w.mu.Unlock()
	return len(p), nil
}

// firstContaining returns the first recorded Write whose bytes carry the needle.
func (w *timingWriter) firstContaining(needle string) (timedWrite, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, write := range w.writes {
		if strings.Contains(write.text, needle) {
			return write, true
		}
	}
	return timedWrite{}, false
}

func TestFeedWritesEachLineWithinOnePoll(t *testing.T) {
	fake := newFakeExec(t)
	fake.seatsFromTmux("r", []string{"s"})

	root := t.TempDir()
	sessions := filepath.Join(root, "rooms", "r", "seats", "s", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(sessions, "session.jsonl")
	seed := `{"message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"SEED-REPLAY"}]}}`
	if err := os.WriteFile(logPath, []byte(seed+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := &timingWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runFeed(ctx, shared{room: "r", stateRoot: root, back: 2, width: 120, lines: 3, detail: true}, out)
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("feed returned %v", err)
		}
	}()

	// The startup replay prints the seed before the loop records its offsets; those offsets are taken
	// just after the last replay write. Wait for the seed, then give the loop the moment it needs to
	// settle, so the appended line is tailed by the loop and not consumed by replay bookkeeping.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := out.firstContaining("SEED-REPLAY"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the startup replay never reached the writer")
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)

	marker := fmt.Sprintf("PERF06-MARKER-%d", time.Now().UnixNano())
	line := fmt.Sprintf(`{"message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"%s"}]}}`, marker)
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	appended := time.Now()
	if _, err := file.WriteString(line + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	// Wait past the bound so a late write is measured as late rather than reported as missing.
	bound := 2 * feedPollInterval
	limit := appended.Add(4 * bound)
	var got timedWrite
	ok := false
	for !ok && time.Now().Before(limit) {
		got, ok = out.firstContaining(marker)
		if !ok {
			time.Sleep(2 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatalf("no write carried %q within %s: the line is buffered or the tail missed it", marker, 4*bound)
	}
	if delay := got.at.Sub(appended); delay > bound {
		t.Errorf("the line reached the writer after %s, want <= 2 x feedPollInterval = %s", delay, bound)
	}
	if newlines := strings.Count(got.text, "\n"); newlines != 1 {
		t.Errorf("the line's Write carried %d newline(s), want exactly the one line, uncoalesced: %q", newlines, got.text)
	}
	if strings.Contains(got.text, "SEED-REPLAY") {
		t.Errorf("the marker arrived coalesced with the replay: %q", got.text)
	}
}
