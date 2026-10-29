// A text pasted into an agy pane while agy believes a turn is running is QUEUED, not submitted: agy shows it
// under the prompt as `▸ <text> (alt+m to send immediately)` with `Press up to edit queued messages`, and
// sends it only when the running turn ends - or never, when agy was still starting up and no turn ends.
//
// MEASURED (room aa2-rs-peak, 2026-10-29): `4genteam up` was followed within seconds by a brief through
// `4genteam input`; at 06:5x both agy panes sat at an empty `>` prompt with the brief queued under it, and
// nothing ran until the director pressed alt+m (tmux `send-keys M-m`) in each pane. At 08:10 the same start
// showed why agy thought a turn was running: the idle guard's stall nudge reached each fresh seat first
// (agy log: "Sending user message" 08:10:32, the brief "Forwarding mid-turn user message" 08:10:47).
//
// The runtime's paste reports `submitted` for a queued text too (the pane changed after Enter), so the
// sender settles it here: after a pane paste, a pane that shows the queued marker gets agy's own
// "send immediately" key.
package clientruntime

import (
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// agyQueuedMarker is agy's hint under a queued message; no other runtime prints it.
const agyQueuedMarker = "alt+m to send immediately"

// queuedTailLines is how many of the pane's last non-blank lines the marker is looked for in: the queued
// message sits between the transcript and the input box, so the bottom of the pane is enough, and an old
// marker scrolled up into the transcript is not read as a live one.
const queuedTailLines = 15

// queuedSubmitAttempts bounds the alt+m presses one send makes.
const queuedSubmitAttempts = 3

// Seams, so the suite drives the loop with no runtime and no tmux.
var (
	queuedPane = func(socket, session string) (string, error) {
		answer, err := NewClient(socket).Capture(session, 1)
		return answer.Content, err
	}
	pressSendImmediately = func(session string) error {
		cmd, err := clientenv.TmuxCommand("send-keys", "-t", session, "M-m")
		if err != nil {
			return err
		}
		return cmd.Run()
	}
	queuedPause = 500 * time.Millisecond
)

// PaneHoldsQueuedMessage says whether the bottom of a pane shows an agy queued message.
func PaneHoldsQueuedMessage(pane string) bool {
	lines := strings.Split(pane, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < queuedTailLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if strings.Contains(line, agyQueuedMarker) {
			return true
		}
	}
	return false
}

// SubmitQueuedMessage presses agy's "send immediately" key while the pane shows a queued message, at most
// queuedSubmitAttempts times, and returns how many presses it made. The pane is read once at once and once
// after a pause (agy draws the queue line a moment after Enter); a pane that never shows the marker costs
// one pause and nothing else. A pane that cannot be read is left alone.
func SubmitQueuedMessage(socket, session string) int {
	pressed := 0
	for look := 0; look < queuedSubmitAttempts+1; look++ {
		pane, err := queuedPane(socket, session)
		if err != nil {
			return pressed
		}
		if !PaneHoldsQueuedMessage(pane) {
			if look > 0 {
				return pressed
			}
			time.Sleep(queuedPause)
			continue
		}
		if pressed == queuedSubmitAttempts {
			return pressed
		}
		if pressSendImmediately(session) != nil {
			return pressed
		}
		pressed++
		time.Sleep(queuedPause)
	}
	return pressed
}

// submitIfQueued runs SubmitQueuedMessage for a send the runtime answered by PASTING into the pane
// (`submitted` or `unconfirmed`): only a seat with no runner - agy among them - is pasted into, and only a
// pasted text can sit in agy's queue. A relayed frame went to a runner's mailbox and is left alone.
func submitIfQueued(socket, session string, answer []byte) {
	verdict, readable := SendVerdict(answer)
	if !readable || (verdict != VerdictSubmitted && verdict != VerdictPastedUnconfirmed) {
		return
	}
	SubmitQueuedMessage(socket, session)
}
