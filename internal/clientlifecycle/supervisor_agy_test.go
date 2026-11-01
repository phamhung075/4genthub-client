package clientlifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

// agyLogFile writes an agy transcript whose newest record carries `tokens` at the top level, the way agy
// 1.4.9 writes it, ending on a final answer (ended) or on a pending tool call (working).
func agyLogFile(t *testing.T, tokens int, ended bool, ageSeconds float64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	last := fmt.Sprintf(`{"type":"PLANNER_RESPONSE","status":"DONE","input_tokens":1000,"cache_read_tokens":%d,"output_tokens":0}`, tokens-1000)
	if !ended {
		last = fmt.Sprintf(`{"type":"PLANNER_RESPONSE","status":"DONE","input_tokens":1000,"cache_read_tokens":%d,"output_tokens":0,"tool_calls":[{"name":"run_command"}]}`, tokens-1000)
	}
	if err := os.WriteFile(path, []byte(`{"type":"USER_INPUT","status":"DONE"}`+"\n"+last+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Duration(ageSeconds * float64(time.Second)))
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

// THE INCIDENT: room aa2-rs-peak, 2026-10-28, two agy seats at 164k against a 75k limit and a supervisor log
// holding only its start lines. The pass found no reading in the agy transcript, so it never acted.
func TestAnAgySeatPastTheLimitAtItsPromptIsCompactedWithoutANotice(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	path := agyLogFile(t, 200_000, true, 600)
	supervisor := supervisorFor(path, "agy", false, &sent, &interrupted)
	supervisor.Idle = func(string, string) bool { return seatlog.AgyTurnEnded(path) }
	supervisor.Step()
	if strings.Join(sent, ",") != "/compact RAW" {
		t.Errorf("sent %v, want only the raw /compact: a notice pasted into an idle agy pane starts a model turn", sent)
	}
}

func TestAWorkingAgySeatPastTheLimitIsToldAndNotCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	path := agyLogFile(t, 200_000, false, 600)
	supervisor := supervisorFor(path, "agy", false, &sent, &interrupted)
	supervisor.Idle = func(string, string) bool { return seatlog.AgyTurnEnded(path) }
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte" {
		t.Errorf("sent %v, want the notice alone: /compact must never reach an agy seat mid-turn", got)
	}
}

func TestAnAgyCheckpointWitnessesTheCompaction(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	path := agyLogFile(t, 200_000, true, 600)
	supervisor := supervisorFor(path, "agy", false, &sent, &interrupted)
	var logged []string
	supervisor.Log = func(text string) { logged = append(logged, text) }
	supervisor.Step()
	// agy's own record of a compaction, with a reading that has NOT dropped yet: only the count can witness it.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"type":"CHECKPOINT","status":"DONE","source":"SYSTEM","content":"# Resuming from a compaction"}` + "\n")
	_ = file.Close()
	supervisor.Step()
	if !strings.Contains(strings.Join(logged, "\n"), "/compact WITNESSED") {
		t.Errorf("the CHECKPOINT did not witness the compaction; log:\n%s", strings.Join(logged, "\n"))
	}
}

// The task-249 seat at a limit far past hard: never at its prompt by the transcript's word, so the pass interrupts
// it, and a log quiet since then is what lets /compact through. Before this it was interrupted every minute forever.
func TestAnAgySeatPastHardThatNeverReadsIdleIsInterruptedThenCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	path := agyLogFile(t, 340_000, false, 600)
	supervisor := supervisorFor(path, "agy", true, &sent, &interrupted)
	now := float64(time.Now().Unix())
	supervisor.Now = func() float64 { return now }
	supervisor.Step()
	if len(interrupted) != 1 || containsCompact(sent) {
		t.Fatalf("first pass: interrupted %v, sent %v", interrupted, sent)
	}
	now += float64(supervisor.Quiet) + 1
	supervisor.Step()
	if !containsCompact(sent) {
		t.Fatalf("after the interrupt and a quiet log, sent %v", sent)
	}
	count := len(sent)
	for i := 0; i < 5; i++ {
		now += 10
		supervisor.Step()
	}
	if len(sent) != count {
		t.Fatalf("an unwitnessed /compact was typed again inside the window: %v", sent[count:])
	}
}

// Past the warn limit and below hard: the warning first, one witness window to reach a safe point, then the same.
func TestAnAgySeatPastWarnThatNeverReadsIdleGetsItsGraceThenIsCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	path := agyLogFile(t, 260_000, false, 600)
	supervisor := supervisorFor(path, "agy", true, &sent, &interrupted)
	now := float64(time.Now().Unix())
	supervisor.Now = func() float64 { return now }
	supervisor.Step()
	now += witnessWindow - 10
	supervisor.Step()
	if len(interrupted) != 0 || containsCompact(sent) {
		t.Fatalf("inside the grace: interrupted %v, sent %v", interrupted, sent)
	}
	now += 11
	supervisor.Step()
	if len(interrupted) != 1 || containsCompact(sent) {
		t.Fatalf("past the grace: interrupted %v, sent %v", interrupted, sent)
	}
	now += float64(supervisor.Quiet) + 1
	supervisor.Step()
	if !containsCompact(sent) {
		t.Fatalf("after the interrupt and a quiet log, sent %v", sent)
	}
}

func containsCompact(sent []string) bool {
	for _, text := range sent {
		if strings.HasPrefix(text, "/compact") {
			return true
		}
	}
	return false
}
