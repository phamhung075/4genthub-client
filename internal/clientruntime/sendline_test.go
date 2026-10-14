package clientruntime

import (
	"strings"
	"testing"
)

func TestRuntimeSendLineNamesAnUnconfirmedSubmission(t *testing.T) {
	line := RuntimeSendLine("room-seat", []byte(`{"delivered":"room-seat","outcome":"unconfirmed"}`))
	if !strings.HasPrefix(line, "Pasted to room-seat, submission unconfirmed") {
		t.Fatalf("an unconfirmed send must not read as delivered, got %q", line)
	}
}

func TestRuntimeSendLineReadsSubmittedAndMissingOutcomeAsSent(t *testing.T) {
	for _, answer := range []string{`{"delivered":"s","outcome":"submitted"}`, `{"delivered":"s"}`, `not json`} {
		if got := RuntimeSendLine("s", []byte(answer)); got != "Sent to s\n" {
			t.Errorf("answer %s: got %q, want the plain Sent line", answer, got)
		}
	}
}
