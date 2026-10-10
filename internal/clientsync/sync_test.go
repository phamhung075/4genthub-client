package clientsync

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// TestSyncAnswersHelp: the mission's rule is that EVERY verb answers --help. `sync --help` used to be
// read as an unknown verb, so it printed an error and exited non-zero.
func TestSyncAnswersHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	code := (syncCommand{}).Run(context.Background(), nil, []string{"--help"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Errorf("sync --help exited %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut.String())
	}
	if !strings.Contains(out.String(), "4genteam sync") {
		t.Errorf("sync --help did not name the verb: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("sync --help wrote to stderr: %q", errOut.String())
	}
}

// TestEveryDeclaredVerbAnswersHelp is the mission's rule applied to this group: every verb `sync`
// declares answers --help with its own usage on stdout and exit 0. It doubles as the guard on the
// declaration list, because a name added to syncVerbs without the case that implements it would answer
// a usage error instead - which is the advertises-then-denies defect this test exists to catch.
func TestEveryDeclaredVerbAnswersHelp(t *testing.T) {
	for _, verb := range syncVerbs {
		var out, errOut bytes.Buffer
		code := (syncCommand{}).Run(context.Background(), nil, []string{verb, "--help"}, &out, &errOut)
		if code != clientcmd.ExitOK {
			t.Errorf("sync %s --help exited %d, want %d (stderr %q)", verb, code, clientcmd.ExitOK, errOut.String())
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("sync %s --help printed nothing on stdout", verb)
		}
	}
}

// TestSyncRejectsAVerbItDoesNotDeclare: nothing this group declares refuses by name any more - every
// name in syncVerbs is implemented - so an undeclared name is a plain usage error, `watch` included,
// which Python never registered under sync.
func TestSyncRejectsAVerbItDoesNotDeclare(t *testing.T) {
	for _, args := range [][]string{{"nonsense"}, {"watch"}, nil} {
		var out, errOut bytes.Buffer
		if code := (syncCommand{}).Run(context.Background(), nil, args, &out, &errOut); code != clientcmd.ExitUsage {
			t.Errorf("sync %v exited %d, want %d", args, code, clientcmd.ExitUsage)
		}
	}
}
