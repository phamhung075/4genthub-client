package clientroom

import (
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// EVERY sub-verb of this group refuses the runtime's spelling with the CLI-wide 2 and the one sentence -
// including the ones that read their own flags before the group's parser (queue, report, metrics, limits) -
// and none of them reaches a daemon: this group's other bad invocations are 3, and the spelling is the one
// case the ruling numbers apart.
func TestTheRuntimeSpellingIsRefusedByEveryRoomSubVerbWithTheOneSentence(t *testing.T) {
	for _, args := range [][]string{
		{"status", "room-a"},
		{"stop", "room-a"},
		{"reseat", "room-a", "seat-a"},
		{"compact", "room-a"},
		{"queue", "list", "room-a"},
		{"report", "room-a"},
		{"metrics", "room-a"},
		{"limits", "room-a"},
	} {
		for _, flag := range [][]string{{"--state-dir", "/x"}, {"--state-dir=/x"}} {
			code, stdout, stderr := runRoom(t, append(args, flag...)...)
			if code != clientcmd.ExitUsage {
				t.Errorf("%q: exit %d, want %d (stderr %q)", args, code, clientcmd.ExitUsage, stderr)
			}
			if !strings.Contains(stderr, clientenv.StateRootRetiredMessage) {
				t.Errorf("%q: stderr = %q, want the one sentence", args, stderr)
			}
			if stdout != "" {
				t.Errorf("%q: stdout = %q, want nothing printed", args, stdout)
			}
		}
	}
}
