package clientwatch

import (
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// The watch group refuses the runtime's spelling with the CLI-wide 2 and the one sentence, before a pane or
// a tmux server is touched. `runVerbT` is not used here on purpose: it fills in the root itself, which is the
// argument being tested.
func TestTheRuntimeSpellingIsRefusedBeforeAnyPaneOpens(t *testing.T) {
	for _, verb := range []string{"watch", "grid", "feed", "inputs", "input"} {
		for _, flag := range [][]string{{"--state-dir", "/x"}, {"--state-dir=/x"}} {
			args := append([]string{"--room", "room-a"}, flag...)
			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			code := runVerb(context.Background(), verb, args, stdout, stderr)
			if code != clientcmd.ExitUsage {
				t.Errorf("%s %q: exit %d, want %d (stderr %q)", verb, flag, code, clientcmd.ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), clientenv.StateRootRetiredMessage) {
				t.Errorf("%s %q: stderr = %q, want the one sentence", verb, flag, stderr.String())
			}
			if stdout.String() != "" {
				t.Errorf("%s %q: stdout = %q, want nothing printed", verb, flag, stdout.String())
			}
		}
	}
}
