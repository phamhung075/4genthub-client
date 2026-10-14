package clientruntime

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeSendLineNamesAnUnconfirmedSubmission(t *testing.T) {
	line := RuntimeSendLine("room-seat", []byte(`{"delivered":"room-seat","outcome":"unconfirmed"}`))
	if !strings.HasPrefix(line, "Pasted to room-seat, submission unconfirmed") {
		t.Fatalf("an unconfirmed send must not read as delivered, got %q", line)
	}
}

func TestRuntimeSendLineReadsSubmittedAndMissingOutcomeAsSent(t *testing.T) {
	for _, answer := range []string{`{"delivered":"s","outcome":"submitted"}`, `{"delivered":"s","outcome":"relayed","runner":{"relayed":"prompt"}}`, `{"delivered":"s"}`, `not json`} {
		if got := RuntimeSendLine("s", []byte(answer)); got != "Sent to s\n" {
			t.Errorf("answer %s: got %q, want the plain Sent line", answer, got)
		}
	}
}

func TestSendEnvelopeNamesSenderRecipientAndReply(t *testing.T) {
	stamp := time.Date(2026, 10, 14, 9, 5, 0, 0, time.UTC)
	got := sendEnvelope("room-a-lead", "room-a-dev", "hello", stamp)
	want := "From: room-a-lead\nTo: room-a-dev\nSent: 10-14 09:05Z\n---\nhello\n---\n↩ Reply: 4genteam send room-a-lead \"...\""
	if got != want {
		t.Errorf("envelope:\n got %q\nwant %q", got, want)
	}
	if blank := sendEnvelope(" ", "x", "b", time.Time{}); !strings.HasPrefix(blank, "From: "+senderFallback+"\nTo: x\n---") {
		t.Errorf("a blank sender must be labelled, got %q", blank)
	}
	if ext := sendEnvelope("human@external", "x", "b", time.Time{}); !strings.Contains(ext, "is not a local session") {
		t.Errorf("an @external sender gets no reply command, got %q", ext)
	}
}
