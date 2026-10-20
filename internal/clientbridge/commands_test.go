package clientbridge

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestBridgeOncePrintRedactsThroughTheEnvironment drives the REAL VERB rather than a bridge this test
// built, and that is the whole point of it: the environment's known values are read at the CALL SITE
// (commands.go, secretscan.KnownValuesFromEnv) and handed to the bridge, so a test that injects the map
// into its own NewBridge proves only that redaction USES a map. If the call site ever passed nil, such
// a test would stay green while the report printed a secret - which is the failure mode this test
// exists to make impossible. A fake runtime on the socket quotes a value the environment declares;
// nothing else is stubbed, so the path exercised is the one an operator runs (`bridge once --print`).
func TestBridgeOncePrintRedactsThroughTheEnvironment(t *testing.T) {
	const value = "env-secret-value-0123456789"
	fakeRuntimeSocket(t, func(line string) string {
		if strings.Contains(line, `"GetRoomLinks"`) {
			return `{"ok":true,"data":[]}` + "\n"
		}
		return `{"ok":true,"data":[{"canonicalSessionName":"room-a-seat-a@room-a","sessionName":"agenthub--room-a--seat-a",` +
			`"roomName":"room-a","logicalId":"room-a.seat-a","runtime":"omp","sessionStatus":"running",` +
			`"agentActivity":{"state":"working"},"detail":"the token is ` + value + ` - rotate it"}]}` + "\n"
	})

	// The environment is the input under test: a name the scanner looks for (TOKEN) with a long enough
	// value, and a home so the seat store resolves.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEMO_TOKEN", value)

	var out, errOut bytes.Buffer
	code := (bridgeCommand{}).Run(context.Background(), []string{"once", "--print", "--machine-id", "test-machine"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("bridge once --print exited %d (stderr %q)", code, errOut.String())
	}
	var payload Payload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("the printed payload is not a payload: %v (%s)", err, out.String())
	}
	if len(payload.Seats) != 1 {
		t.Fatalf("printed %d seats, want 1: %s", len(payload.Seats), out.String())
	}
	seat := payload.Seats[0]
	if strings.Contains(seat.Detail, value) {
		t.Fatalf("the printed detail carries the environment's secret value: %q", seat.Detail)
	}
	if !seat.Redacted || !strings.Contains(seat.Detail, "[REDACTED:DEMO_TOKEN]") {
		t.Fatalf("detail = %q redacted = %v, want the marker naming the variable", seat.Detail, seat.Redacted)
	}
}

// TestBridgeSubVerbsAnswerHelp: `bridge run` and `bridge once` parse their own flags, so before the
// central helper `--help` was an unknown flag and exited 2 - the one argument that asks what a verb is
// was the one that errored.
func TestBridgeSubVerbsAnswerHelp(t *testing.T) {
	for _, args := range [][]string{{"run", "--help"}, {"once", "-h"}} {
		var out, errOut bytes.Buffer
		code := (bridgeCommand{}).Run(context.Background(), args, &out, &errOut)
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
	code := (bridgeCommand{}).Run(context.Background(), []string{"install-service", "--help"}, &out, &errOut)
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
	if code := (bridgeCommand{}).Run(context.Background(), []string{"install-service"}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("install-service exited %d (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "[Unit]") {
		t.Errorf("install-service printed %q, want the systemd unit", out.String())
	}
}

// test_the_path_defaults_do_not_follow_home pins the Python test of that name: the pins dir and the
// state path resolve from the ACCOUNT home, so a decoy $HOME - what the room orchestrator gives a seat - does not move
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
	if want := filepath.Join(home, ".config", "4genthub", "seats"); pins != want {
		t.Errorf("pins dir followed $HOME: %q, want %q", pins, want)
	}
	state, err := DefaultStatePath()
	if err != nil {
		t.Fatalf("DefaultStatePath: %v", err)
	}
	if want := filepath.Join(home, ".config", "4genthub", "bridge-sync.json"); state != want {
		t.Errorf("state path followed $HOME: %q, want %q", state, want)
	}
	if strings.HasPrefix(pins, decoy) || strings.HasPrefix(state, decoy) {
		t.Errorf("a default is under the decoy home: pins=%q state=%q", pins, state)
	}
}
