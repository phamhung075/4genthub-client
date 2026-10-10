package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func rigMissing(t *testing.T) {
	t.Helper()
	previous := clientcmd.LookPath
	clientcmd.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { clientcmd.LookPath = previous })
}

// TestUnportedCommandRefusesRatherThanStubbing closes the hole a stub would leave: a command that
// exists in the Python client and not yet here must say exactly that and exit non-zero. A command that
// answered an empty success would be the "looks complete and is not" shape in a new place.
//
// THE TABLE IS ONLY THE COMMANDS STILL UNPORTED, and an entry leaves it the moment its verb becomes
// real: `sync status` was here until 64f8ecde and `sync pull` until the pull commit, and both now have
// their ported behaviour pinned by TestPortedVerbsRefuseOnTheEnvironment below. What remains here is what
// the client still refuses by name - the other sync verbs among them - which is what this test exists to
// keep honest.
func TestUnportedCommandRefusesRatherThanStubbing(t *testing.T) {
	rigMissing(t)
	for _, args := range [][]string{
		{"feedback", "oops"},
		{"seatcheck", "send", "x", "y"},
		{"sync", "bundle", "4genthub-dev", "alpha"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != clientcmd.ExitUnavailable {
			t.Errorf("%v: exit code = %d, want %d", args, code, clientcmd.ExitUnavailable)
		}
		if !strings.Contains(errOut.String(), "not ported") {
			t.Errorf("%v: message = %q, want it to say the command is not ported", args, errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout = %q, want nothing: a refused command does no partial work", args, out.String())
		}
	}
}

// TestPortedVerbsRefuseOnTheEnvironment is the OTHER half of the test above, and it replaces the entries
// that sat inside it: `sync status` stopped being unported in 64f8ecde and `sync pull` in the pull commit,
// so the property to pin is no longer the porting note but the failure a ported verb gives when the
// platform is not configured - the same one the binary was smoked against: exit 2 and the variable's
// name, with nothing on stdout.
//
// It stays in the exit-code-and-message shape rather than being loosened to "some error", because the
// code is the part a caller scripts against and the message is the part an operator acts on.
func TestPortedVerbsRefuseOnTheEnvironment(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	for _, args := range [][]string{
		{"sync", "status", "4genthub-dev"},
		{"sync", "pull", "4genthub-dev", "alpha"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != clientcmd.ExitUsage {
			t.Errorf("%v: exit code = %d, want %d (a usage/environment error)", args, code, clientcmd.ExitUsage)
		}
		if !strings.Contains(errOut.String(), "AGENTHUB_URL is not set") {
			t.Errorf("%v: message = %q, want the environment's own fault named", args, errOut.String())
		}
		if strings.Contains(errOut.String(), "not ported") {
			t.Errorf("%v: message = %q: the verb IS ported now, so the porting note would be a lie", args, errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout = %q, want nothing before the cloud answers", args, out.String())
		}
	}
}

// TestBridgeIsRefusedByThePlatformMatrixBeforeItIsRefusedAsUnported: `bridge` is the one pending
// command that needs rig, so with no rig it must fail on the MATRIX - the reason a user can act on -
// rather than on the porting note.
func TestBridgeIsRefusedByThePlatformMatrixBeforeItIsRefusedAsUnported(t *testing.T) {
	rigMissing(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"bridge", "once"}, &out, &errOut); code != clientcmd.ExitUnavailable {
		t.Fatalf("exit code = %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if !strings.Contains(errOut.String(), "rig is not on PATH") {
		t.Errorf("message = %q, want the MATRIX's reason, not the porting note", errOut.String())
	}
}

// TestUsageAndVersionAreTheOnlyFreeCommands: no arguments is a usage error, an unknown command is a
// usage error, and the two commands that need nothing at all succeed.
func TestUsageAndVersionAreTheOnlyFreeCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("no arguments: exit code = %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "commands:") {
		t.Errorf("no arguments: stderr = %q, want the command list", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"nonsense"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("unknown command: exit code = %d, want %d", code, clientcmd.ExitUsage)
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"version"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Errorf("version: exit code = %d, want %d", code, clientcmd.ExitOK)
	}
	if !strings.Contains(out.String(), "4genteam "+clientVersion) {
		t.Errorf("version output = %q, want the client version", out.String())
	}
}

// TestSyncRefusesAnUnknownVerbAsUsageNotAsUnavailable: an unknown verb is the CALLER's error, and
// telling it apart from "not ported yet" is what keeps a typo from reading as a missing feature.
func TestSyncRefusesAnUnknownVerbAsUsageNotAsUnavailable(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"sync", "nonsense"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("exit code = %d, want %d (a typo is not a missing feature)", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "unknown verb") {
		t.Errorf("message = %q, want it to say the verb is unknown", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"sync"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("sync with no verb: exit code = %d, want %d", code, clientcmd.ExitUsage)
	}
}
