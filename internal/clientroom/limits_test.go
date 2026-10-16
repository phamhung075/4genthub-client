// What these tests pin is the verb's CONTRACT rather than its rendering: a read writes nothing, a write keeps
// the numbers it does not name, and OFF is not a ceiling of zero.
// The park's own rendering on a seat's row belongs to the park-semantics lane (clientroom/parkNote, which reads
// the same roombudget record and prints it for `status` and `reseat` alike), so nothing here asserts it.
package clientroom

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

// roomDir makes the room's own directory under a state root, which every limits run needs: the file lives in
// the room, and a room the state root does not hold is refused rather than created.
func roomDir(t *testing.T, stateDir string) string {
	t.Helper()
	dir := filepath.Join(stateDir, "rooms", testRoom)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRoomLimitsReadsWritesNothing: the limits SHIP OFF, so the first read of a room that has never had a file
// prints the zeros and leaves no file behind - a read that created one would invent the very state it reports.
func TestRoomLimitsReadsWritesNothing(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{})
	stateDir := daemon.stateDir
	dir := roomDir(t, stateDir)

	code, stdout, stderr := runRoom(t, daemon.args("limits", testRoom)...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := "room: " + testRoom + "@" + stateDir + " socket: " + daemon.socket + "\n" +
		"limits: " + roombudget.Path(dir) + "\n" +
		"state: off - nothing is measured\n" +
		"per seat: 0.00 USD/24h, 0 calls/min\n"
	// The socket is resolved from the environment when no flag names one: give the run the daemon's, so the
	// pair this test compares is the pair a caller would see.
	if stdout != want {
		t.Fatalf("stdout:\n got %q\nwant %q (stderr %q)", stdout, want, stderr)
	}
	if _, err := os.Stat(roombudget.Path(dir)); !os.IsNotExist(err) {
		t.Errorf("a read wrote the file: %v", err)
	}
}

// TestRoomLimitsWritesKeepsAndTurnsOff: the three writes that matter. `--on` writes the agreed defaults, a
// later write keeps the numbers it does not name, and `--off` turns the ceilings off WITHOUT forgetting them -
// an owner turning them off to finish a lane must not lose the numbers the room agreed.
func TestRoomLimitsWritesKeepsAndTurnsOff(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{})
	stateDir := daemon.stateDir
	dir := roomDir(t, stateDir)
	load := func() roombudget.Limits {
		t.Helper()
		limits, err := roombudget.Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return limits
	}

	code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--on")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if got := load(); got != (roombudget.Limits{Enabled: true, USDPerSeat24h: 25, CallsPerSeatMin: 300}) {
		t.Fatalf("limits = %+v, want the agreed ceilings ON", got)
	}

	code, _, stderr = runRoom(t, append(daemon.args("limits", testRoom), "--usd", "40")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if got := load(); got.USDPerSeat24h != 40 || got.CallsPerSeatMin != 300 || !got.Enabled {
		t.Fatalf("limits = %+v, want only the USD ceiling changed", got)
	}

	code, stdout, _ := runRoom(t, append(daemon.args("limits", testRoom), "--off")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d", code, clientcmd.ExitOK)
	}
	if got := load(); got.Enabled || got.USDPerSeat24h != 40 || got.CallsPerSeatMin != 300 {
		t.Fatalf("limits = %+v, want the numbers kept and the state off", got)
	}
	if !strings.Contains(stdout, "state: off - nothing is measured") || !strings.Contains(stdout, "40.00 USD/24h, 300 calls/min") {
		t.Errorf("stdout = %q, want the state and the ceilings it kept", stdout)
	}
}

// TestRoomLimitsRefusesACeilingOfZeroAndTwoAnswers: zero is not a ceiling (it would park a seat on its first
// call), and --on with --off is two answers to one question. Both are usage errors, and neither writes.
func TestRoomLimitsRefusesACeilingOfZeroAndTwoAnswers(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{})
	stateDir := daemon.stateDir
	dir := roomDir(t, stateDir)

	for _, args := range [][]string{
		append(daemon.args("limits", testRoom), "--usd", "0"),
		append(daemon.args("limits", testRoom), "--calls", "-5"),
		append(daemon.args("limits", testRoom), "--on", "--off"),
	} {
		code, _, stderr := runRoom(t, args...)
		if code != clientcmd.ExitRoomRefused {
			t.Errorf("%v exited %d, want %d", args, code, clientcmd.ExitRoomRefused)
		}
		if stderr == "" {
			t.Errorf("%v refused with nothing on stderr", args)
		}
	}
	if _, err := os.Stat(roombudget.Path(dir)); !os.IsNotExist(err) {
		t.Errorf("a refused write wrote the file: %v", err)
	}
}
