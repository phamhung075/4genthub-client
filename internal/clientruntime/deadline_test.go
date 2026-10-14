package clientruntime

import (
	"testing"
	"time"
)

// TestTheSeatStartDeadlineOutlastsTheRuntimesReadyWindow pins the coupling the severed bridge was made of.
//
// The runtime gives a seat `SEAT_READY_WINDOW_SECS = 20` to prove itself (runtimes/agenthub-runtime/src/
// supervisor/mod.rs), and the client used to wait 10 s for that same verb. Measured: a cold start returned
// `read error: i/o timeout` at 10.15 s while the runtime was still legitimately waiting, so a verdict landing
// between the two numbers could never reach the client - it read as a transport error instead of a verdict.
//
// The two constants cannot be read from one another across the language boundary, so the runtime's number is
// named here: lowering the client's deadline below it, or raising the runtime's above it, has to face this
// test and the comment that explains it.
func TestTheSeatStartDeadlineOutlastsTheRuntimesReadyWindow(t *testing.T) {
	const runtimeReadyWindow = 20 * time.Second
	if seatStartDeadline <= runtimeReadyWindow {
		t.Fatalf("seatStartDeadline = %s, want more than the runtime's %s readiness window: a verdict landing "+
			"near the end of that window would read as an i/o timeout, not a verdict", seatStartDeadline, runtimeReadyWindow)
	}
	if callDeadline >= seatStartDeadline {
		t.Fatalf("callDeadline = %s is not shorter than seatStartDeadline = %s: the seat-start verb is back on "+
			"the ordinary deadline", callDeadline, seatStartDeadline)
	}
}
