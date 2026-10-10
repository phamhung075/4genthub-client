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

// TestSyncStillRefusesAnUnportedVerbAndATypo: help is help, but a real invocation of a verb that has
// not moved still refuses by name, and a typo is still the caller's error.
func TestSyncStillRefusesAnUnportedVerbAndATypo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := (syncCommand{}).Run(context.Background(), nil, []string{"rig"}, &out, &errOut); code != clientcmd.ExitUnavailable {
		t.Errorf("an unported verb exited %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if !strings.Contains(errOut.String(), "not ported") {
		t.Errorf("the refusal did not say what is missing: %q", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := (syncCommand{}).Run(context.Background(), nil, []string{"nonsense"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("a typo exited %d, want %d", code, clientcmd.ExitUsage)
	}

	out.Reset()
	errOut.Reset()
	if code := (syncCommand{}).Run(context.Background(), nil, nil, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("no verb exited %d, want %d", code, clientcmd.ExitUsage)
	}
}
