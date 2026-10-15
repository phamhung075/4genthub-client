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
	code := (syncCommand{}).Run(context.Background(), []string{"--help"}, &out, &errOut)
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
		code := (syncCommand{}).Run(context.Background(), []string{verb, "--help"}, &out, &errOut)
		if code != clientcmd.ExitOK {
			t.Errorf("sync %s --help exited %d, want %d (stderr %q)", verb, code, clientcmd.ExitOK, errOut.String())
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("sync %s --help printed nothing on stdout", verb)
		}
	}
}

// test_help_states_offline_use_is_manual (tests/test_seat_sync.py:1544): the help says offline use is
// the operator's own choice and how the bundle is put to use. Python's argparse printed it as the sync
// parser's epilog; the Go help had dropped it, which left the row with no Go witness. The test carries
// the row's name, so the parity instrument pairs it by name rather than by an alias entry, and it
// asserts the row's own sentences on stdout with exit 0 - `script` became `client` in the port.
//
// THE ROW'S OTHER HALF IS C3's RULE FOR A PRINTED LINE: it RUNS or it is ABSENT. The epilog named
// `rig up <bundle> --target <dir>`, which is the retired orchestrator CLI and the one launch this stack
// forbids, and this client has no verb that starts a room from a bundle - so the sentence names
// `sync offline-install` instead and the dead procedure is asserted ABSENT here. That assertion is the
// half with teeth: it fails the moment someone re-adds a line a reader cannot run.
func TestHelpStatesOfflineUseIsManual(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := (syncCommand{}).Run(context.Background(), []string{"--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("sync --help exited %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut.String())
	}
	for _, want := range []string{"Offline use is an explicit operator choice", "4genteam sync offline-install", "never falls back"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("sync --help does not say %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "rig up") {
		t.Errorf("sync --help prints the retired `rig up` procedure again - a line a reader cannot run:\n%s", out.String())
	}
}

// TestSyncRejectsAVerbItDoesNotDeclare: nothing this group declares refuses by name any more - every
// name in syncVerbs is implemented - so an undeclared name is a plain usage error, `watch` included,
// which Python never registered under sync.
func TestSyncRejectsAVerbItDoesNotDeclare(t *testing.T) {
	for _, args := range [][]string{{"nonsense"}, {"watch"}, nil} {
		var out, errOut bytes.Buffer
		if code := (syncCommand{}).Run(context.Background(), args, &out, &errOut); code != clientcmd.ExitUsage {
			t.Errorf("sync %v exited %d, want %d", args, code, clientcmd.ExitUsage)
		}
	}
}

// The usage errors speak this client's own dialect, not Python's argparse: each one exits 2 AND names
// the argument it is about. What is pinned is the exit code and the name - never the sentence - so a
// verb may rephrase ("missing argument: room") without breaking this, while a return to argparse's
// "the following arguments are required" or "unrecognized arguments" fails it.
func TestUsageErrorsNameTheOffendingArgument(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bundle with one positional", []string{"bundle", "room1"}, "room, seat"},
		{"bundle with an unknown flag",
			[]string{"bundle", "room1", "seat1", "--rig-yaml", "y", "--rig-root", "r", "--nope"}, "--nope"},
		{"switch with one positional", []string{"switch", "room1"}, "room, seat"},
		{"respawn with one positional", []string{"respawn", "room1"}, "room, seat"},
		{"offline-install with no target", []string{"offline-install"}, "target"},
		{"install-checker with no --go-dir", []string{"install-checker"}, "--go-dir"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := (syncCommand{}).Run(context.Background(), testCase.args, &out, &errOut)
			if code != clientcmd.ExitUsage {
				t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, errOut.String())
			}
			message := errOut.String()
			if !strings.Contains(message, testCase.want) {
				t.Errorf("the refusal does not name %q: %q", testCase.want, message)
			}
			for _, argparse := range []string{"arguments are required", "unrecognized", "expected one argument"} {
				if strings.Contains(message, argparse) {
					t.Errorf("the refusal still speaks argparse (%q): %q", argparse, message)
				}
			}
		})
	}
}
