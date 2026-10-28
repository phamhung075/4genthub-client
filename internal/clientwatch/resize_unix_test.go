//go:build unix

package clientwatch

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// A zoom sends the terminal's resize signal; the feed has to hear it to redraw at the new width, and a burst of
// signals settles into one redraw.
func TestAResizeSignalReachesTheFeedAndABurstSettles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resized := watchResize(ctx)
	time.Sleep(10 * time.Millisecond)
	for range 3 {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-resized:
	case <-time.After(2 * time.Second):
		t.Fatal("no resize delivered for SIGWINCH")
	}
	settleResize(resized)
	select {
	case <-resized:
		t.Fatal("the burst was not drained into one redraw")
	default:
	}
}
