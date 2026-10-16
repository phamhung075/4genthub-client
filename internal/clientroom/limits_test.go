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

// asOwner runs the test with the caller's marker ABSENT, which is the state - and the only one - the verb reads
// as the owner: a marker that is SET counts as a seat even when its value is EMPTY, because the shell hands a
// seat `AGENTHUB_SESSION_NAME= 4genteam room limits <room> --usd 999` for three characters, and that raise used
// to be written (measured 2026-10-16 at d964b8b: empty through the shell, 777 written, while the same seat's
// set-value attempt was refused). `t.Setenv(sessionEnv, "")` is therefore no longer the owner's spelling and
// cannot be: `t.Setenv` cannot UNSET. The usage names the same two states - a seat has the marker, the owner
// raises "running this with $AGENTHUB_SESSION_NAME unset".
func asOwner(t *testing.T) {
	t.Helper()
	old, had := os.LookupEnv(sessionEnv)
	if err := os.Unsetenv(sessionEnv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(sessionEnv, old)
		}
	})
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
	// The verb reads the CALLER's identity, and these are the OWNER's writes (25 -> 40 is a raise): the marker
	// is UNSET rather than emptied, because a set-but-empty marker is a seat.
	asOwner(t)
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

// loadLimits is the file's own state, read back the way the safeguard reads it: a test that asserts what a verb
// LEFT is asserting the file, not the verb's rendering of it.
func loadLimits(t *testing.T, dir string) roombudget.Limits {
	t.Helper()
	limits, err := roombudget.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return limits
}

// TestASeatDoesNotRaiseTheCeilingThatBoundsIt closes the hole this change exists for, measured on the real
// binary before it: a seat ran `room limits --usd 999` and the file went to 999 (and `--off` disabled the
// ceilings), so the seat a ceiling exists to stop was the one who could lift it. The refusal names the marker
// and the OWNER, writes nothing, and leaves TIGHTENING allowed - a lower number, or --on.
func TestASeatDoesNotRaiseTheCeilingThatBoundsIt(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{})
	dir := roomDir(t, daemon.stateDir)
	t.Setenv(sessionEnv, testSeat)
	// The agreed ceilings, written by the seat itself: there is no file yet, so nothing is being raised.
	if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--usd", "25", "--calls", "300", "--on")...); code != clientcmd.ExitOK {
		t.Fatalf("setup: exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := roombudget.Limits{Enabled: true, USDPerSeat24h: 25, CallsPerSeatMin: 300}

	for _, flags := range [][]string{
		{"--usd", "999"},
		{"--usd", "25.01"},
		{"--calls", "301"},
		{"--off"},
		{"--usd", "999", "--calls", "400"},
	} {
		code, stdout, stderr := runRoom(t, append(daemon.args("limits", testRoom), flags...)...)
		if code != clientcmd.ExitRoomRefused {
			t.Errorf("%v exited %d, want %d (stderr %q)", flags, code, clientcmd.ExitRoomRefused, stderr)
		}
		if !strings.Contains(stderr, sessionEnv) || !strings.Contains(stderr, "OWNER") {
			t.Errorf("%v refused with %q, want the marker and who may raise it named", flags, stderr)
		}
		if stdout != "" {
			t.Errorf("%v printed %q, want nothing: nothing was written", flags, stdout)
		}
		if got := loadLimits(t, dir); got != want {
			t.Fatalf("%v left %+v, want the ceilings unchanged at %+v", flags, got, want)
		}
	}

	// Tightening is not the hole: a lower number, a lower call ceiling, and --on (already on) all pass.
	for _, c := range []struct {
		flags []string
		want  roombudget.Limits
	}{
		{[]string{"--usd", "10"}, roombudget.Limits{Enabled: true, USDPerSeat24h: 10, CallsPerSeatMin: 300}},
		{[]string{"--calls", "100"}, roombudget.Limits{Enabled: true, USDPerSeat24h: 10, CallsPerSeatMin: 100}},
		{[]string{"--on"}, roombudget.Limits{Enabled: true, USDPerSeat24h: 10, CallsPerSeatMin: 100}},
	} {
		code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), c.flags...)...)
		if code != clientcmd.ExitOK {
			t.Errorf("%v exited %d, want %d: a seat may tighten what bounds it (stderr %q)", c.flags, code, clientcmd.ExitOK, stderr)
		}
		if got := loadLimits(t, dir); got != c.want {
			t.Errorf("%v left %+v, want %+v", c.flags, got, c.want)
		}
	}
}

// TestTheOwnerRaisesTheCeilingAndAnAlreadyOffCeilingIsNotARaise pins the two edges of the rule: the marker IS
// the whole check, so with $AGENTHUB_SESSION_NAME unset (the owner's own shell, which is where the owner runs
// it) a raise is written; and a seat asking --off for a room that is ALREADY off loosens nothing, because
// nothing is in force to lose.
func TestTheOwnerRaisesTheCeilingAndAnAlreadyOffCeilingIsNotARaise(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{})
	dir := roomDir(t, daemon.stateDir)

	asOwner(t)
	if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--usd", "25", "--calls", "300", "--on")...); code != clientcmd.ExitOK {
		t.Fatalf("setup: exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--usd", "999")...); code != clientcmd.ExitOK {
		t.Fatalf("the owner raising: exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if got := loadLimits(t, dir); got.USDPerSeat24h != 999 {
		t.Fatalf("limits = %+v, want the owner's 999 written", got)
	}

	t.Setenv(sessionEnv, testSeat)
	if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--off")...); code != clientcmd.ExitRoomRefused {
		t.Errorf("--off from a seat with the ceilings ON: exit %d, want %d (stderr %q)", code, clientcmd.ExitRoomRefused, stderr)
	}
	asOwner(t)
	if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--off")...); code != clientcmd.ExitOK {
		t.Fatalf("the owner turning them off: exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	t.Setenv(sessionEnv, testSeat)
	if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--off")...); code != clientcmd.ExitOK {
		t.Errorf("--off from a seat with the ceilings already OFF: exit %d, want %d - nothing in force is lost (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if got := loadLimits(t, dir); got.Enabled {
		t.Errorf("limits = %+v, want them still off", got)
	}
}

// TestAnEmptyMarkerIsStillASeat: the marker is read as a PAIR, so a seat that EMPTIES it in the prefix of its own
// command line - `AGENTHUB_SESSION_NAME= 4genteam room limits ROOM --usd 999`, which every shell accepts - is
// still a seat. Measured 2026-10-16 at d964b8b: through an empty marker the same seat wrote 777 while its own
// set-value attempt was refused, because the check read os.Getenv and could not tell an empty marker from an
// absent one. The guard's `FOO= ` hole is the same three characters seen from the other side. A WHITESPACE-ONLY
// marker is the same class and is pinned with it: the check trimmed before comparing, so `'   '` was the owner
// too, and what decides now is whether the marker is THERE, not what it holds.
func TestAnEmptyMarkerIsStillASeat(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{})
	dir := roomDir(t, daemon.stateDir)
	asOwner(t)
	if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--usd", "25", "--calls", "300", "--on")...); code != clientcmd.ExitOK {
		t.Fatalf("setup: exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	want := roombudget.Limits{Enabled: true, USDPerSeat24h: 25, CallsPerSeatMin: 300}

	for _, spelling := range []string{"", "   "} {
		t.Setenv(sessionEnv, spelling)
		for _, flags := range [][]string{{"--usd", "999"}, {"--calls", "301"}, {"--off"}} {
			code, stdout, stderr := runRoom(t, append(daemon.args("limits", testRoom), flags...)...)
			if code != clientcmd.ExitRoomRefused {
				t.Errorf("a marker of %q asking %v: exit %d, want %d (stderr %q)", spelling, flags, code, clientcmd.ExitRoomRefused, stderr)
			}
			if !strings.Contains(stderr, sessionEnv) || !strings.Contains(stderr, "OWNER") {
				t.Errorf("a marker of %q refusing %v said %q, want the marker and who may raise it named", spelling, flags, stderr)
			}
			if stdout != "" {
				t.Errorf("a marker of %q asking %v printed %q, want nothing", spelling, flags, stdout)
			}
			if got := loadLimits(t, dir); got != want {
				t.Fatalf("a marker of %q asking %v left %+v, want the ceilings unchanged at %+v", spelling, flags, got, want)
			}
		}
	}

	// Tightening through the same blank markers still passes: the rule is about a RAISE, not about the marker.
	// The second run writes the same 10 over the same 10, which raises nothing either.
	for _, spelling := range []string{"", "   "} {
		t.Setenv(sessionEnv, spelling)
		if code, _, stderr := runRoom(t, append(daemon.args("limits", testRoom), "--usd", "10")...); code != clientcmd.ExitOK {
			t.Errorf("a marker of %q LOWERING the ceiling: exit %d, want %d (stderr %q)", spelling, code, clientcmd.ExitOK, stderr)
		}
		if got := loadLimits(t, dir); got.USDPerSeat24h != 10 {
			t.Errorf("limits = %+v, want the seat's own 10 written through a marker of %q", got, spelling)
		}
	}
}
