package clientrestore

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// continue and watchdog refuse the runtime's spelling with the one sentence before a rig is read or a seat
// is touched.
func TestTheRuntimeSpellingIsRefusedByTheRestoreVerbs(t *testing.T) {
	for _, command := range []clientcmd.Command{continueCommand{}, watchdogCommand{}} {
		for _, flag := range [][]string{{"--state-dir", "/x"}, {"--state-dir=/x"}} {
			var out, errOut bytes.Buffer
			code := command.Run(context.Background(), flag, &out, &errOut)
			if code != clientcmd.ExitUsage {
				t.Errorf("%s %q: exit %d, want %d (stderr %q)", command.Name(), flag, code, clientcmd.ExitUsage, errOut.String())
			}
			if !strings.Contains(errOut.String(), clientenv.StateRootRetiredMessage) {
				t.Errorf("%s %q: stderr = %q, want the one sentence", command.Name(), flag, errOut.String())
			}
			if out.String() != "" {
				t.Errorf("%s %q: stdout = %q, want nothing printed", command.Name(), flag, out.String())
			}
		}
	}
}
