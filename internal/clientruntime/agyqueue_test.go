package clientruntime

import (
	"errors"
	"testing"
	"time"
)

// The pane agy drew in room aa2-rs-peak, 2026-10-29, after a brief was pasted while it thought a turn ran.
const queuedAgyPane = `● Ran 2 commands
────────────────────────────────────────────────────────────────
  ▸ From: human@kernel To: agenthub--aa2-rs-peak--peak-dev... (alt+m to send immediately)
  Press up to edit queued messages
────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────
                                    Gemini 3.8 Flash · low`

const idleAgyPane = `● Ran 2 commands
────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────
                                    Gemini 3.8 Flash · low`

func withQueueSeams(t *testing.T, panes []string, pressErr error) *[]string {
	t.Helper()
	previousPane, previousPress, previousPause := queuedPane, pressSendImmediately, queuedPause
	t.Cleanup(func() { queuedPane, pressSendImmediately, queuedPause = previousPane, previousPress, previousPause })
	queuedPause = time.Millisecond
	reads := 0
	queuedPane = func(_, _ string) (string, error) {
		pane := panes[len(panes)-1]
		if reads < len(panes) {
			pane = panes[reads]
		}
		reads++
		return pane, nil
	}
	var pressed []string
	pressSendImmediately = func(session string) error {
		pressed = append(pressed, session)
		return pressErr
	}
	return &pressed
}

func TestAQueuedAgyMessageIsSentWithAltM(t *testing.T) {
	pressed := withQueueSeams(t, []string{queuedAgyPane, idleAgyPane}, nil)
	if got := SubmitQueuedMessage("", "agenthub--r--dev"); got != 1 || len(*pressed) != 1 {
		t.Fatalf("presses = %d (%v), want one alt+m for one queued message", got, *pressed)
	}
}

func TestTheQueueLineMayAppearAMomentAfterThePaste(t *testing.T) {
	pressed := withQueueSeams(t, []string{idleAgyPane, queuedAgyPane, idleAgyPane}, nil)
	if got := SubmitQueuedMessage("", "s"); got != 1 || len(*pressed) != 1 {
		t.Fatalf("presses = %d, want one: the second look found the queue line", got)
	}
}

func TestAPaneWithNoQueueIsNotTouched(t *testing.T) {
	pressed := withQueueSeams(t, []string{idleAgyPane}, nil)
	if got := SubmitQueuedMessage("", "s"); got != 0 || len(*pressed) != 0 {
		t.Fatalf("presses = %d, want none on a pane with no queued message", got)
	}
}

func TestAQueueThatNeverClearsIsPressedABoundedNumberOfTimes(t *testing.T) {
	pressed := withQueueSeams(t, []string{queuedAgyPane}, nil)
	if got := SubmitQueuedMessage("", "s"); got != queuedSubmitAttempts || len(*pressed) != queuedSubmitAttempts {
		t.Fatalf("presses = %d, want the bound %d", got, queuedSubmitAttempts)
	}
}

func TestAFailedKeyPressStops(t *testing.T) {
	pressed := withQueueSeams(t, []string{queuedAgyPane}, errors.New("no server"))
	if got := SubmitQueuedMessage("", "s"); got != 0 || len(*pressed) != 1 {
		t.Fatalf("presses counted = %d after %d tries, want 0 after 1", got, len(*pressed))
	}
}

func TestAMarkerScrolledUpIntoTheTranscriptIsNotAQueue(t *testing.T) {
	pane := "old (alt+m to send immediately)\n"
	for i := 0; i < queuedTailLines+2; i++ {
		pane += "line\n"
	}
	if PaneHoldsQueuedMessage(pane) {
		t.Fatal("a marker above the pane's bottom lines read as a live queued message")
	}
}

func TestOnlyAPastedSendIsChecked(t *testing.T) {
	pressed := withQueueSeams(t, []string{queuedAgyPane, idleAgyPane}, nil)
	submitIfQueued("", "s", []byte(`{"outcome":"relayed"}`))
	if len(*pressed) != 0 {
		t.Fatal("a relayed frame (runner seat) was treated as a paste")
	}
	submitIfQueued("", "s", []byte(`{"outcome":"submitted"}`))
	if len(*pressed) != 1 {
		t.Fatalf("presses = %d after a pasted send onto a queued pane, want 1", len(*pressed))
	}
}
