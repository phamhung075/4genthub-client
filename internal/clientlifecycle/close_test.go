package clientlifecycle

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// No test in this package reaches the machine's herdr; a test about the close replaces closeWatch itself.
func init() { closeWatch = func(context.Context, string) error { return nil } }

func TestStopClosesTheRigsWatchWorkspaces(t *testing.T) {
	restoreAll(t)
	stopPattern = func(string) (bool, error) { return true, nil }
	var closed []string
	previous := closeWatch
	closeWatch = func(_ context.Context, rig string) error { closed = append(closed, rig); return nil }
	t.Cleanup(func() { closeWatch = previous })

	var out, errOut bytes.Buffer
	if code := (stopCommand{}).Run(context.Background(), []string{"my-rig"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("stop returned %d (stderr %q)", code, errOut.String())
	}
	if strings.Join(closed, ",") != "my-rig" {
		t.Errorf("closed the watch workspaces of %v, want [my-rig]", closed)
	}
}
