package clientroom

import (
	"context"
	"strings"
	"testing"
)

// No test in this package reaches the machine's herdr; a test about the close replaces closeWatch itself.
func init() { closeWatch = func(context.Context, string) error { return nil } }

func TestRoomStopClosesTheRoomsWatchWorkspaces(t *testing.T) {
	useTempAgentState(t, testRoom)
	daemon := newFakeDaemon(t, map[string]string{"StopRoom": okData("null")})
	var closed []string
	previous := closeWatch
	closeWatch = func(_ context.Context, room string) error { closed = append(closed, room); return nil }
	t.Cleanup(func() { closeWatch = previous })

	if code, _, stderr := runRoom(t, daemon.args("stop", testRoom)...); code != 0 {
		t.Fatalf("stop returned %d (stderr %q)", code, stderr)
	}
	if strings.Join(closed, ",") != testRoom {
		t.Errorf("closed the watch workspaces of %v, want [%s]", closed, testRoom)
	}
}
