package clientbridge

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// TestInstallServiceNamesTheClientsOwnEnvFile: the unit and the runtime must read the SAME file, and
// that file must be the one the client actually uses. The phase-1 move carried a PYTHON-ERA file name
// that nothing ever writes, so the installed service came up with no credentials while still looking
// healthy - a broken install that reports nothing is worse than one that refuses to install.
func TestInstallServiceNamesTheClientsOwnEnvFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := bridgeInstallService(&out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("install-service returned %d (stderr %q)", code, errOut.String())
	}
	want, err := clientenv.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	unit := out.String()

	// EXACTLY ONE EnvironmentFile, and it is the client's own: a single line is what makes this an
	// assertion about the whole unit rather than about a substring of it.
	if count := strings.Count(unit, "EnvironmentFile="); count != 1 {
		t.Errorf("the unit carries %d EnvironmentFile lines, want exactly one:\n%s", count, unit)
	}
	if !strings.Contains(unit, "EnvironmentFile="+want+"\n") {
		t.Errorf("the unit does not read the client's own env file %q:\n%s", want, unit)
	}
	if strings.Contains(unit, "%h") {
		t.Errorf("the unit should carry the resolved path, not a systemd escape:\n%s", unit)
	}
	if !strings.Contains(unit, " bridge run\n") {
		t.Errorf("the unit does not start this binary's own bridge sub-verb:\n%s", unit)
	}
}

// TestBridgeSubVerbsAnswerHelp: `bridge run` and `bridge once` parse their own flags, so before the
// central helper `--help` was an unknown flag and exited 2 - the one argument that asks what a verb is
// was the one that errored.
func TestBridgeSubVerbsAnswerHelp(t *testing.T) {
	for _, args := range [][]string{{"run", "--help"}, {"once", "-h"}} {
		var out, errOut bytes.Buffer
		code := (bridgeCommand{}).Run(context.Background(), nil, args, &out, &errOut)
		if code != clientcmd.ExitOK {
			t.Errorf("bridge %v exited %d, want %d (stderr %q)", args, code, clientcmd.ExitOK, errOut.String())
		}
		if !strings.Contains(out.String(), "4genteam bridge") {
			t.Errorf("bridge %v help did not name the group: %q", args, out.String())
		}
	}
}

// TestBridgeInstallServiceAnswersHelpNotTheUnit: the sweep in cmd/4genteam asked only for NON-EMPTY
// stdout, and install-service has an answer of its own - the systemd unit - so `install-service --help`
// printed the unit, exited 0, and passed. Help comes before the answer, as it does in run and once, and
// WITHOUT --help the unit is still what prints: the gate moved, it was not removed.
func TestBridgeInstallServiceAnswersHelpNotTheUnit(t *testing.T) {
	var out, errOut bytes.Buffer
	code := (bridgeCommand{}).Run(context.Background(), nil, []string{"install-service", "--help"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("install-service --help exited %d, want %d (stderr %q)", code, clientcmd.ExitOK, errOut.String())
	}
	if !strings.Contains(out.String(), "4genteam bridge <subcommand>") {
		t.Errorf("install-service --help printed %q, want the bridge's own usage", out.String())
	}
	if strings.Contains(out.String(), "[Unit]") {
		t.Error("install-service --help printed the systemd unit: help must come before the answer")
	}

	out.Reset()
	errOut.Reset()
	if code := (bridgeCommand{}).Run(context.Background(), nil, []string{"install-service"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("install-service exited %d (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "[Unit]") {
		t.Errorf("install-service printed %q, want the systemd unit", out.String())
	}
}

// test_the_path_defaults_do_not_follow_home pins the Python test of that name: the pins dir and the
// state path resolve from the ACCOUNT home, so a decoy $HOME - what OpenRig gives a seat - does not move
// either of them.
func TestThePathDefaultsDoNotFollowHome(t *testing.T) {
	decoy := t.TempDir()
	t.Setenv("HOME", decoy)

	home, err := clientenv.RealHome()
	if err != nil {
		t.Skipf("no account home on this machine: %v", err)
	}
	pins, err := defaultPinsDir()
	if err != nil {
		t.Fatalf("defaultPinsDir: %v", err)
	}
	if want := filepath.Join(home, ".openrig", "agenthub-seats"); pins != want {
		t.Errorf("pins dir followed $HOME: %q, want %q", pins, want)
	}
	state, err := DefaultStatePath()
	if err != nil {
		t.Fatalf("DefaultStatePath: %v", err)
	}
	if want := filepath.Join(home, ".openrig", "bridge-sync.json"); state != want {
		t.Errorf("state path followed $HOME: %q, want %q", state, want)
	}
	if strings.HasPrefix(pins, decoy) || strings.HasPrefix(state, decoy) {
		t.Errorf("a default is under the decoy home: pins=%q state=%q", pins, state)
	}
}
