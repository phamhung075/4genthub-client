package clientlifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

// withDocumentedLimits pins the three limits and restores them, so the suite does not move with the
// operator's env file - the Python suite's the_documented_limits fixture does the same thing.
func withDocumentedLimits(t *testing.T) {
	t.Helper()
	compact, warn, hard := seatlog.CompactLimit, seatlog.WarnLimit, seatlog.HardLimit
	t.Cleanup(func() { seatlog.CompactLimit, seatlog.WarnLimit, seatlog.HardLimit = compact, warn, hard })
	seatlog.CompactLimit, seatlog.WarnLimit, seatlog.HardLimit = 150_000, 250_000, 300_000
}

// seatLogFile writes the one-record session log the passes read, with a chosen age.
func seatLogFile(t *testing.T, tokens int, ageSeconds float64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	record, err := json.Marshal(map[string]any{
		"message": map[string]any{"usage": map[string]any{"totalTokens": tokens}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(record, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Duration(ageSeconds * float64(time.Second)))
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

// supervisorFor is one pass over one seat, with every action recorded instead of taken.
func supervisorFor(path, runtime string, working bool, sent *[]string, interrupted *[]string) *Supervisor {
	supervisor := &Supervisor{Rig: "r", Quiet: 180, State: map[string]*seatState{}}
	supervisor.Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	supervisor.Seats = func(string) []string { return []string{"lead"} }
	supervisor.SeatLog = func(string, string) (string, string) { return path, runtime }
	supervisor.Idle = func(string, string) bool { return !working }
	supervisor.Say = func(_, _, text string, raw bool) {
		if raw {
			*sent = append(*sent, "/compact RAW")
			return
		}
		*sent = append(*sent, text)
	}
	supervisor.RPCCompact = func(string, string) string { *sent = append(*sent, "RPC"); return "sent" }
	supervisor.Abort = func(string, string) { *sent = append(*sent, "ABORT") }
	supervisor.Interrupt = func(_, seat string) { *interrupted = append(*interrupted, seat) }
	supervisor.Log = func(string) {}
	return supervisor
}

func firstFive(sent []string) []string {
	out := make([]string, 0, len(sent))
	for _, text := range sent {
		if len(text) > 5 {
			text = text[:5]
		}
		out = append(out, text)
	}
	return out
}

func TestASeatUnderTheLimitIsLeftAlone(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, seatlog.CompactLimit-1, 999), "omp", false, &sent, &interrupted)
	supervisor.Step()
	if len(sent) != 0 {
		t.Errorf("a seat under the limit was acted on: %v", sent)
	}
}

func TestASeatPastTheLimitStillWorkingIsToldOnceAndNotCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	path := seatLogFile(t, 200_000, 5)
	supervisor := supervisorFor(path, "omp", true, &sent, &interrupted)
	supervisor.Step()
	if len(sent) != 1 || !strings.Contains(sent[0], "Context limit") || !strings.Contains(sent[0], "Do not type /compact") {
		t.Fatalf("first pass sent %v, want the one notice", sent)
	}
	sent = nil
	supervisor.Step()
	if len(sent) != 0 {
		t.Errorf("the notice was repeated: %v", sent)
	}
}

func TestAQuietSeatPastTheLimitIsCompactedAfterTheNotice(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 200_000, 600), "omp", false, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,/comp" {
		t.Errorf("sent %v, want the notice then the raw /compact", got)
	}
}

func TestASeatThatDroppedBelowTheLimitIsToldAgainNextTime(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 200_000, 5), "omp", false, &sent, &interrupted)
	supervisor.Step()
	supervisor.SeatLog = func(string, string) (string, string) {
		return seatLogFile(t, 40_000, 5), "omp"
	}
	supervisor.Step()
	if supervisor.State["lead"].told {
		t.Error("a seat that dropped below the limit is still marked as told")
	}
}

func TestAnOmpSeatPastTheWarnLimitIsCompactedNowWithoutAnAbort(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "omp", true, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,VERY ,RPC" {
		t.Errorf("sent %v, want notice, warning then the RPC compact", got)
	}
}

func TestAnOmpSeatAtTheHardLimitIsAbortedThenCompacted(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 320_000, 5), "omp", true, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,VERY ,ABORT,RPC" {
		t.Errorf("sent %v, want the abort before the RPC compact", got)
	}
}

func TestAnIdleSeatOfAnotherRuntimePastTheWarnLimitIsSentCompactWithoutWaitingForQuiet(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "claude-code", false, &sent, &interrupted)
	supervisor.Step()
	if got := firstFive(sent); strings.Join(got, ",") != "Conte,VERY ,/comp" {
		t.Errorf("sent %v, want notice, warning then /compact", got)
	}
}

func TestAWorkingSeatOfAnotherRuntimePastTheWarnLimitWaitsThenIsInterruptedAtTheHardLimit(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "claude-code", true, &sent, &interrupted)
	supervisor.Step()
	if strings.Contains(strings.Join(sent, "|"), "/compact RAW") || len(interrupted) != 0 {
		t.Fatalf("a working seat past the warn limit was acted on early: sent %v, interrupted %v", sent, interrupted)
	}
	hard := seatLogFile(t, 320_000, 5)
	supervisor.SeatLog = func(string, string) (string, string) { return hard, "claude-code" }
	supervisor.Step()
	supervisor.Step()
	if len(interrupted) != 1 || interrupted[0] != "lead" {
		t.Errorf("interrupted = %v, want exactly one interrupt of lead (one per gap)", interrupted)
	}
}

func TestASeatPastTheWarnLimitIsWarnedOnce(t *testing.T) {
	withDocumentedLimits(t)
	var sent, interrupted []string
	supervisor := supervisorFor(seatLogFile(t, 260_000, 5), "omp", false, &sent, &interrupted)
	supervisor.Step()
	if !strings.Contains(strings.Join(sent, "|"), "VERY IMPORTANT") {
		t.Fatalf("no warning on the first pass: %v", sent)
	}
	sent = nil
	supervisor.Step()
	if strings.Contains(strings.Join(sent, "|"), "VERY IMPORTANT") {
		t.Errorf("the warning was repeated: %v", sent)
	}
}
