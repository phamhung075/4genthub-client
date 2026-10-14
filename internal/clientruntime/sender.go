package clientruntime

import (
	"os"
	"strings"
	"time"
)

// senderFallback is the honest label for a dispatch with no seat environment: an unattributable send is
// labelled rather than made to look attributed.
const senderFallback = "<unknown sender>"

// SendOptions is the send verb's option set.
type SendOptions struct {
	// Raw sends the text exactly, with no From/To/Sent envelope.
	Raw bool
	// Sender is the identity the envelope is written AS, for a caller whose own environment cannot carry
	// it: the message deliverer runs outside any seat shell and delivers text a human wrote, so it must send
	// AS that human rather than as the seat the deliverer happens to run under. Blank means this shell's own
	// session.
	Sender string
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
	reply := `↩ Reply: 4genteam send ` + label + ` "..."`
	if strings.HasSuffix(label, "@external") {
		reply = `↩ Reply: ` + label + ` is not a local session, and no local command delivers a reply to it.`
	}
	return strings.Join(append(header, "---", body, "---", reply), "\n")
}
