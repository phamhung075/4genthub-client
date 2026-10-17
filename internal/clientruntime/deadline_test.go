package clientruntime

import "testing"
import "time"

// TestTheSeatStartDeadlineOutlastsTheRuntimesReadyWindow pins the coupling the severed bridge was made of.
//
// The runtime gives a seat `SEAT_READY_WINDOW_SECS` seconds to prove itself (rust/agenthub-runtime/src/
// supervisor/mod.rs; the path is rust/, not runtimes/), and the client used to wait 10 s for that same verb.
// Measured: a cold start returned `read error: i/o timeout` at 10.15 s while the runtime was still legitimately
// waiting, so a verdict landing between the two numbers could never reach the client - it read as a transport
// error instead of a verdict. The window is 60 s now, because a first-ever launch in a fresh cache spends that
// time building the agent's model cache, and the architect measured a real cold start at 10.95 s on the
// 60 s-less build: past the old 10 s deadline, delivered as a verdict by the 30 s one.
//
// The two constants cannot be read from one another across the language boundary, so the runtime's number is
// named here: lowering the client's deadline below it, or raising the runtime's above it, has to face this
// test and the comment that explains it.
//
// The client's OTHER window is deliberately not this one: `RoomStartWindow` (internal/clientlifecycle) is the
// poll that begins AFTER StartRoom returns, so it sits on top of the runtime's wait rather than mirroring it.
// That both were 20 s was a coincidence, and nothing here may treat it as a relation.
func TestTheSeatStartDeadlineOutlastsTheRuntimesReadyWindow(t *testing.T) {
	const runtimeReadyWindow = 60 * time.Second
	if seatStartDeadline <= runtimeReadyWindow {
		t.Fatalf("seatStartDeadline = %s, want more than the runtime's %s readiness window: a verdict landing "+
			"near the end of that window would read as an i/o timeout, not a verdict", seatStartDeadline, runtimeReadyWindow)
	}
	if CallDeadline >= seatStartDeadline {
		t.Fatalf("CallDeadline = %s is not shorter than seatStartDeadline = %s: the seat-start verb is back on "+
			"the ordinary deadline", CallDeadline, seatStartDeadline)
	}
	// The daemon's own reply bound is REPLY_TIMEOUT = 8 s (rust/agenthub-runtime/src/runner/mod.rs): the wait
	// for a runner's answer to one frame must END before this client's socket deadline does, or the daemon's
	// named error (the frame reached the mailbox; the runner never answered) is lost to a bare i/o timeout and
	// the text's fate cannot be reported (task 83a39aa5). Named here because it cannot be read across the
	// language boundary, and pinned so a raised number has to face this test.
	const runtimeReplyBound = 8 * time.Second
	if CallDeadline <= runtimeReplyBound {
		t.Fatalf("CallDeadline = %s does not outlast the runtime's %s reply bound: the daemon's named error "+
			"could never reach this client before its own deadline fired", CallDeadline, runtimeReplyBound)
	}
}
