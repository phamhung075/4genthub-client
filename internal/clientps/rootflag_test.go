package clientps

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// ps refuses the runtime's spelling with the one sentence, before it reads a room or a state root.
func TestPSRefusesTheRuntimeSpelling(t *testing.T) {
	for _, flag := range [][]string{{"--state-dir", "/x"}, {"--state-dir=/x"}} {
		var out, errOut bytes.Buffer
		code := command{}.Run(context.Background(), flag, &out, &errOut)
		if code != clientcmd.ExitUsage {
			t.Errorf("%q: exit %d, want %d (stderr %q)", flag, code, clientcmd.ExitUsage, errOut.String())
		}
		if !strings.Contains(errOut.String(), clientenv.StateRootRetiredMessage) {
			t.Errorf("%q: stderr = %q, want the one sentence", flag, errOut.String())
		}
		if out.String() != "" {
			t.Errorf("%q: stdout = %q, want nothing printed", flag, out.String())
		}
	}
}
