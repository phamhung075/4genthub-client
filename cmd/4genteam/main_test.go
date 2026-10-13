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

// rigPresent is rigMissing's other half, and it exists because a test that leaves the seam at the real
// exec.LookPath does not test the dispatcher, it tests the HOST: `4genteam` with no arguments reaches
// `up`'s own argument check only when rig is on PATH, and stops at the platform matrix when it is not.
// A masked clone at b01a211 with no rig on PATH took the second answer and failed this package, which is
// the mission's own rule - tests need no network, rig or seat - broken in the one place a reader would
// not look for it. The stub is a PATH string: RequireRig only looks the tool up, it does not run it.
func rigPresent(t *testing.T) {
	t.Helper()
	previous := clientcmd.LookPath
	clientcmd.LookPath = func(string) (string, error) { return "/stub/bin/rig", nil }
	t.Cleanup(func() { clientcmd.LookPath = previous })
}

// TestUnportedCommandRefusesRatherThanStubbing closes the hole a stub would leave: a command that
// exists in the Python client and not yet here must say exactly that and exit non-zero. A command that
// answered an empty success would be the "looks complete and is not" shape in a new place.
//
// THE TABLE IS ONLY THE COMMANDS STILL UNPORTED, and an entry leaves it the moment its verb becomes
// real: `sync status` was here until 64f8ecde, `sync pull` until the pull commit, and `feedback`,
// `sync bundle` and `sync watch` all left the same way - `watch` because Python never registered that
// verb under sync at all, so the name is gone rather than promised. `seatcheck` is the one name that
// remains, because the checker is its own binary rather than a verb this client implements, and that is
// what this test exists to keep honest.
func TestUnportedCommandRefusesRatherThanStubbing(t *testing.T) {
	rigMissing(t)
	for _, args := range [][]string{
		{"seatcheck", "send", "x", "y"},
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

// TestAPendingVerbAnswersHelpToo: `seatcheck` refuses as a command, but the one argument that asks
// what it is must not be the one that errors.
func TestAPendingVerbAnswersHelpToo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"seatcheck", "--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("seatcheck --help exited %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut.String())
	}
	if !strings.Contains(out.String(), "seatcheck") {
		t.Errorf("the help did not name the checker: %q", out.String())
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

// TestUsageAndVersionAreTheOnlyFreeCommands: no arguments means `up` (which then needs a rig), an
// unknown command is a usage error, and the two commands that need nothing at all succeed.
//
// THE RIG SEAM IS PINNED IN BOTH DIRECTIONS, because the no-argument answer depends on it: with rig on
// PATH the dispatcher reaches `up`'s own argument check, and with rig absent it stops at the platform
// matrix. Leaving the seam alone here read the HOST instead - a masked clone at b01a211 with no rig on
// PATH got the matrix's answer and this assertion failed, on exactly the machine the client must stay
// honest on. Both answers are now asserted rather than one assumed.
func TestUsageAndVersionAreTheOnlyFreeCommands(t *testing.T) {
	rigPresent(t)
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != clientcmd.ExitRoomRefused {
		t.Errorf("no arguments: exit code = %d, want %d", code, clientcmd.ExitRoomRefused)
	}
	// No argument means `up`, as the Python client's dispatcher did, so the failure names THAT verb and the
	// argument it is missing - not the command list it would have printed before. `up` is room-scoped now
	// (NeedsRig is false) and takes its seats from a LOCAL definition, so the argument it is missing is
	// --team, and the room contract numbers that 3 rather than the CLI-wide ExitUsage (2).
	if !strings.Contains(errOut.String(), "4genteam up:") || !strings.Contains(errOut.String(), "--team") {
		t.Errorf("no arguments: stderr = %q, want up's missing-definition error", errOut.String())
	}

	// THE OTHER HALF: a machine that has never had OpenRig. It is pinned on a verb that STILL needs the
	// platform matrix (`ui`), NOT on `up`: `up` no longer consults the matrix at all, because the point of
	// the native start path is that it needs no OpenRig process - NeedsRig is false and its seats come from
	// a local definition. `status` is deliberately not used here either: it reports this machine's own
	// safeguard rows and answers without a rig by design.
	out.Reset()
	errOut.Reset()
	rigMissing(t)
	if code := run([]string{"ui"}, &out, &errOut); code != clientcmd.ExitUnavailable {
		t.Errorf("ui with no rig on PATH: exit code = %d, want %d", code, clientcmd.ExitUnavailable)
	}
	if !strings.Contains(errOut.String(), "rig is not on PATH") {
		t.Errorf("ui with no rig on PATH: stderr = %q, want the platform matrix's reason", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("ui with no rig on PATH: stdout = %q, want nothing", out.String())
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

// TestHelpIsAnsweredBeforeThePlatformMatrix: a verb's own usage must be readable on a machine that has
// never had OpenRig, and the client's README states that rule without conditions. Six verbs used to
// refuse here instead - `up`, `ui`, `bridge`, and bridge's `run`, `once` and `install-service` - because
// the dispatcher resolved rig BEFORE it called the verb, so the verb's own help path, which every one of
// them answers first, was never reached. Those six are the whole extent: measured against the binary with
// a PATH that has no rig on it, they were the only refusing shapes of forty, and every other verb already
// answered.
//
// The second half is the half a fix like this usually breaks: the SAME verbs driven WITHOUT --help must
// still refuse with the matrix's reason, which is what makes this a move of the gate rather than its
// removal.
func TestHelpIsAnsweredBeforeThePlatformMatrix(t *testing.T) {
	rigMissing(t)

	for _, args := range [][]string{
		{"up", "--help"},
		{"up", "4genthub-dev", "--help"},
		{"ui", "--help"},
		{"bridge", "--help"},
		{"bridge", "run", "--help"},
		{"bridge", "once", "--help"},
		{"bridge", "install-service", "--help"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != clientcmd.ExitOK {
			t.Errorf("%v: exit code = %d, want %d (stderr %q)", args, code, clientcmd.ExitOK, errOut.String())
			continue
		}
		if !strings.Contains(out.String(), "usage:") && !strings.HasPrefix(out.String(), "4genteam bridge <subcommand>") {
			t.Errorf("%v: stdout is not a usage: %q", args, out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("%v: stderr = %q, want nothing beside the help", args, errOut.String())
		}
	}

	// And it is the VERB's own help, not the command list the top-level usage prints.
	var out, errOut bytes.Buffer
	if code := run([]string{"up", "--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("up --help exited %d, want %d", code, clientcmd.ExitOK)
	}
	if !strings.Contains(out.String(), "usage: 4genteam up RIG") {
		t.Errorf("up --help printed %q, want up's own usage line", out.String())
	}

	// The same for the shape that used to answer with something else entirely: install-service has an
	// ANSWER of its own - the systemd unit - so a non-empty stdout check passed while `--help` printed
	// the unit. Help comes before the answer; the unit is still what prints without --help.
	out.Reset()
	errOut.Reset()
	if code := run([]string{"bridge", "install-service", "--help"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("bridge install-service --help exited %d, want %d", code, clientcmd.ExitOK)
	}
	if !strings.HasPrefix(out.String(), "4genteam bridge <subcommand>") || strings.Contains(out.String(), "[Unit]") {
		t.Errorf("bridge install-service --help printed %q, want the bridge's usage and not the unit", out.String())
	}

	// The refusal is NOT softened: the same verbs without --help still fail on the matrix, naming the
	// missing tool and printing nothing on stdout.
	//
	// `up` is NOT in this list anymore, and that is the cutover rather than a softening: it is room-scoped
	// (NeedsRig false) and starts a room from a LOCAL definition, so it deliberately does not consult the
	// matrix at all. TestUsageAndVersionAreTheOnlyFreeCommands pins that from the other side by using `ui`
	// for the matrix half.
	for _, args := range [][]string{
		{"ui"},
		{"bridge"},
		{"bridge", "once"},
	} {
		out.Reset()
		errOut.Reset()
		if code := run(args, &out, &errOut); code != clientcmd.ExitUnavailable {
			t.Errorf("%v: exit code = %d, want %d (the matrix still governs)", args, code, clientcmd.ExitUnavailable)
		}
		if !strings.Contains(errOut.String(), "rig") {
			t.Errorf("%v: stderr = %q, want the missing tool named", args, errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout = %q, want nothing: a refused verb does no partial work", args, out.String())
		}
	}
}
