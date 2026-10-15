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

// TestOnlyASubmissionEarnsTheSentLine is the pin for the defect U3 run 1 and the live room both failed on: a
// `Sent to <session>` line, exit 0, for text the agent never received. The runtime proves a submission only
// with `outcome: submitted` — a pane that changed after Enter — while a runner seat's `relayed` means the
// frame reached the runner and the agent's own answer (which may be a refusal) is never reported back; a
// missing outcome, an outcome this build does not know, and an unreadable answer prove nothing at all, and
// those three used to print the delivery claim too.
func TestOnlyASubmissionEarnsTheSentLine(t *testing.T) {
	if got := RuntimeSendLine("s", []byte(`{"delivered":"s","outcome":"submitted"}`)); got != "Sent to s\n" {
		t.Errorf("a submitted send: got %q, want the Sent line", got)
	}
	for _, c := range []struct{ answer, want string }{
		{`{"delivered":"s","outcome":"relayed","runner":{"relayed":"prompt"}}`, "Relayed to s, submission unconfirmed"},
		{`{"delivered":"s"}`, "Submission unconfirmed for s"},
		{`{"delivered":"s","outcome":"queued"}`, `Submission unconfirmed for s: the runtime answered "queued"`},
		{`not json`, "Submission unconfirmed for s"},
	} {
		got := RuntimeSendLine("s", []byte(c.answer))
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("answer %s: got %q, want a prefix %q", c.answer, got, c.want)
		}
		if strings.HasPrefix(got, "Sent to ") {
			t.Errorf("answer %s: a delivery was claimed for it: %q", c.answer, got)
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
