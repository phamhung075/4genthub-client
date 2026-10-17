package clientruntime

import (
	"os"
	"strings"
	"time"
)

// senderFallback is the honest label for a dispatch with no seat environment: an unattributable send is
// labelled rather than made to look attributed.
const senderFallback = "<unknown sender>"

// HumanSender is the label a text a human wrote arrives under. The message deliverer runs outside any seat
// shell and sends AS the human, and the pane's only confirmed envelope from a human carried exactly
// `From: human@kernel`. A human has no runtime session at all, so `4genteam send` cannot address this label
// (measured 2026-10-17: it answers `lists no session "human@kernel"` and sends nothing) - the reply hint
// reaches the human through the rail instead.
const HumanSender = "human@kernel"

// SendOptions is the send verb's option set.
type SendOptions struct {
	// Raw sends the text exactly, with no From/To/Sent envelope.
	Raw bool
	// Sender is the identity the envelope is written AS, for a caller whose own environment cannot carry
	// it: the message deliverer runs outside any seat shell and delivers text a human wrote, so it must send
	// AS that human rather than as the seat the deliverer happens to run under. Blank means this shell's own
	// session.
	Sender string
	// Verb is the client verb that CARRIED this frame, stamped onto it so a text a mid-turn seat cannot take
	// is handed back naming the way it arrived. Blank means the `send` verb; the carriers whose own name is
	// not `send` name themselves here (a rig's say, its abort, the wake, sync messages).
	Verb string
}

// ownSession is the session name this shell's environment claims, or "" when the shell is not a seat's.
func ownSession() string { return strings.TrimSpace(os.Getenv(agentHubSessionEnv)) }

// sendEnvelope renders the envelope a one-to-one send is wrapped in: the From/To/Sent header, the body
// between two `---` rules, and the reply hint. The pane text is a receiver's whole account of who wrote the
// message, so the sender label is rendered exactly as it arrived; only the blank test uses the trimmed value.
func sendEnvelope(sender, recipient, body string, stamp time.Time) string {
	label := sender
	if strings.TrimSpace(label) == "" {
		label = senderFallback
	}
	header := []string{"From: " + label, "To: " + recipient}
	if !stamp.IsZero() {
		header = append(header, "Sent: "+stamp.UTC().Format("01-02 15:04")+"Z")
	}
	reply := replyLine(label, recipient)
	return strings.Join(append(header, "---", body, "---", reply), "\n")
}

// replyLine is the hint under a message: the one route that reaches its sender again. `send` addresses a
// runtime session; the human has none (see HumanSender), and a rail row must name a seat of the room (the
// daemon refuses a destination that names no seat, and stores a seat key as that seat's session) - so the
// human's route is a row to the room's lead, the seat that carries an owner ask to the owner. Every other
// label is left to `send`, whose own refusal names the session it could not resolve.
func replyLine(label, recipient string) string {
	switch {
	case strings.HasSuffix(label, "@external"):
		return `↩ Reply: ` + label + ` is not a local session, and no local command delivers a reply to it.`
	case label == HumanSender:
		room := roomOf(recipient)
		return `↩ Reply: ` + label + ` is the human, not a local session; the rail carries the reply: ` +
			`4genteam room queue create ` + room + ` --to agenthub--` + room + `--lead --summary "..."`
	case label == senderFallback:
		return `↩ Reply: the sender is unattributable, so no reply can be addressed.`
	}
	return `↩ Reply: 4genteam send ` + label + ` "..."`
}

// roomOf reads the room a frame is delivered to, for a hint that must name one: a session name is
// agenthub--<room>--<seat>, and a seat key names its room after the '@'. A value of neither shape leaves the
// <room> placeholder rather than guessing one.
func roomOf(recipient string) string {
	if parts := strings.SplitN(recipient, "--", 3); len(parts) == 3 && parts[0] == "agenthub" && parts[1] != "" {
		return parts[1]
	}
	if at := strings.LastIndexByte(recipient, '@'); at > 0 && at+1 < len(recipient) {
		return recipient[at+1:]
	}
	return "<room>"
}
