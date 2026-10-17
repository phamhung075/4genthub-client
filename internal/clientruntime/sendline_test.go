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

// TestTheReplyHintNamesOnlyRoutesThatReachTheSender is the pin for the defect measured 2026-10-17: an owner
// message arrives with `Reply: 4genteam send human@kernel "..."`, and running it answers `lists no session
// "human@kernel"` - the one destination the hint named was one its verb could not resolve. The decision is
// that the HINT is wrong, not send's resolution: a send addresses a runtime session, a human has none, and
// the row that reaches the room's owner-carrying lead is the route the hint names instead.
func TestTheReplyHintNamesOnlyRoutesThatReachTheSender(t *testing.T) {
	const seat = "agenthub--4genthub-min--go-dev"
	if got := sendEnvelope(HumanSender, seat, "b", time.Time{}); !strings.Contains(got, "4genteam room queue create 4genthub-min --to agenthub--4genthub-min--lead ") || strings.Contains(got, "4genteam send") {
		t.Errorf("the human's hint:\n got %q\nwant the rail row to the room's lead and no send", got)
	}
	// The room is read from a seat key recipient too, so the hint cannot name another room's lead.
	if got := sendEnvelope(HumanSender, "4genthub-min-go-dev@4genthub-min", "b", time.Time{}); !strings.Contains(got, "create 4genthub-min --to agenthub--4genthub-min--lead ") {
		t.Errorf("the human's hint to a seat key:\n got %q\nwant the room the recipient names", got)
	}
	// A recipient of no readable shape leaves the placeholder rather than guessing a room.
	if got := sendEnvelope(HumanSender, "nonsense", "b", time.Time{}); !strings.Contains(got, "create <room> --to agenthub--<room>--lead ") {
		t.Errorf("the human's hint to an unreadable recipient:\n got %q\nwant the <room> placeholder", got)
	}
	for _, sender := range []string{"agenthub--4genthub-min--lead", "go-dev@4genthub-min", "room-a-lead"} {
		if got := sendEnvelope(sender, seat, "b", time.Time{}); !strings.Contains(got, "4genteam send "+sender+` "..."`) {
			t.Errorf("a session sender %q:\n got %q\nwant the send hint it resolves", sender, got)
		}
	}
	if got := sendEnvelope(" ", seat, "b", time.Time{}); strings.Contains(got, "4genteam send") || !strings.Contains(got, "unattributable") {
		t.Errorf("a blank sender:\n got %q\nwant no command at all", got)
	}
}
