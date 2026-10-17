package clientheal

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// heal refuses the runtime's spelling with its own caller-error code (2) and the one sentence, before the
// room is read or anything is fixed.
func TestHealRefusesTheRuntimeSpelling(t *testing.T) {
	for _, flag := range [][]string{{"--state-dir", "/x"}, {"--state-dir=/x"}} {
		var out, errOut bytes.Buffer
		code := healCommand{}.Run(context.Background(), append([]string{"r"}, flag...), &out, &errOut)
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
