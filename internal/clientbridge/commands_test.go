package clientbridge

import (
	"bytes"
	"context"
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
