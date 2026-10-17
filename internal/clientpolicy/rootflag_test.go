package clientpolicy

import (
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// policy render refuses the runtime's spelling with the CLI-wide 2 and the one sentence, before the hub is
// asked for anything - its own flag table no longer names the root at all.
func TestPolicyRenderRefusesTheRuntimeSpelling(t *testing.T) {
	for _, flag := range [][]string{{"--state-dir", "/x"}, {"--state-dir=/x"}} {
		code, stdout, stderr := runPolicy(t, append([]string{"render", "--room", "room1", "--seat", "seat1"}, flag...)...)
		if code != clientcmd.ExitUsage {
			t.Errorf("%q: exit %d, want %d (stderr %q)", flag, code, clientcmd.ExitUsage, stderr)
		}
		if !strings.Contains(stderr, clientenv.StateRootRetiredMessage) {
			t.Errorf("%q: stderr = %q, want the one sentence", flag, stderr)
		}
		if stdout != "" {
			t.Errorf("%q: stdout = %q, want nothing printed", flag, stdout)
		}
	}
}
