package clientseat

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// THE ELEVEN-SITE CLASS, PINNED FOR THE READING. roomNodes used to shell out to `rig ps --nodes --room RIG
// --json`, and after this client's rename that name resolves to this binary. PATH is emptied here so the
// assertion is about the ABSENCE of the call rather than about a message: with the exec in place the
// function answers "room is not on PATH" (an exec.LookPath failure), and without it the only possible
// failure comes from the local runtime.
func TestRoomNodesNeverShellsOutToRoom(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	nodes, err := roomNodes("nosuchroom")
	if err != nil && strings.Contains(err.Error(), "is not on PATH") {
		t.Fatalf("roomNodes shelled out to `rig`: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "nosuchroom") {
		t.Errorf("the failure does not name the room it could not read: %v", err)
	}
	if err == nil && len(nodes) != 0 {
		t.Errorf("an unreadable room answered with %d node records", len(nodes))
	}
}

func reseatFixture(waitOK bool) (*reseatDeps, *[]string) {
	var log []string
	return &reseatDeps{
		nodes: func(room string) ([]map[string]any, error) {
			return []map[string]any{
				{"logicalId": room + ".lead"}, {"logicalId": room + ".go-dev"},
			}, nil
		},
		waitQuiet: func(room, seat string) bool { log = append(log, "wait "+seat); return waitOK },
		relaunch:  func(room, seat string) error { log = append(log, "relaunch "+seat); return nil },
		checkSeat: func(room, seat string) []string { log = append(log, "check "+seat); return nil },
		getenv:    func(k string) string { return map[string]string{"OFFPEAK_ONLY": "off"}[k] },
		logf:      func(string, ...any) {},
	}, &log
}

func TestReseatRestartsEachSeatInOrderAfterItIsQuiet(t *testing.T) {
	deps, log := reseatFixture(true)
	code, err := reseatSeats("r", []string{"lead", "go-dev"}, false, deps)
	if err != nil || code != clientcmd.ExitOK {
		t.Fatalf("code %d err %v", code, err)
	}
	// Each seat is waited for, restarted, and then CHECKED - the checks report what the new process loaded, so
	// they come after its restart and before the next seat's wait.
	want := []string{"wait lead", "relaunch lead", "check lead", "wait go-dev", "relaunch go-dev", "check go-dev"}
	if len(*log) != len(want) {
		t.Fatalf("log %v", *log)
	}
	for i := range want {
		if (*log)[i] != want[i] {
			t.Fatalf("log %v, want %v", *log, want)
		}
	}
}

func TestReseatLeavesASeatThatIsNotQuietRunning(t *testing.T) {
	deps, log := reseatFixture(false)
	code, err := reseatSeats("r", []string{"lead"}, false, deps)
	if err != nil || code != ExitFailed {
		t.Fatalf("code %d err %v", code, err)
	}
	if len(*log) != 1 {
		t.Fatalf("a seat that is not quiet must not be relaunched: %v", *log)
	}
}

func TestReseatRefusesAnUnknownSeatBeforeTouchingAny(t *testing.T) {
	deps, log := reseatFixture(true)
	_, err := reseatSeats("r", []string{"lead", "nobody"}, false, deps)
	var coded *clientcmd.CodedError
	if !errors.As(err, &coded) || coded.Code != clientcmd.ExitUsage {
		t.Fatalf("err %v", err)
	}
	if len(*log) != 0 {
		t.Fatalf("nothing may run before the seats are checked: %v", *log)
	}
}

func TestReseatTakesTheRoomBeforeItsSeatFlags(t *testing.T) {
	var out, errOut strings.Builder
	if code := runReseat([]string{"--seat", "lead", "r"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Fatalf("a flag before the room must be refused with usage, got %d", code)
	}
	if code := runReseat([]string{"r"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Fatalf("a room with no --seat must be refused with usage, got %d", code)
	}
}

// THE VERB IS LEGIBLE WHILE IT WAITS, which is the defect this lane started from: `seat reseat` printed NOTHING
// from the moment it began to wait until a seat was quiet, inside a budget of up to quietWaitSeconds, so a seat
// being waited for and a verb that had hung were the same terminal. Measured live 2026-10-15: the verb was killed
// at 300s inside that wait having printed nothing at all.
//
// The assertions are about the SHAPE of the three lines, because the order is the information: the wait before it
// begins (with its budget as a duration), the restart before the call that stops a session and starts another, and
// the answer after it.
func TestReseatSaysWhatItWaitsForAndThatItRestarts(t *testing.T) {
	var said []string
	deps, _ := reseatFixture(true)
	deps.logf = func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }

	if code, err := reseatSeats("r", []string{"lead"}, false, deps); err != nil || code != clientcmd.ExitOK {
		t.Fatalf("code %d err %v", code, err)
	}
	if len(said) != 3 {
		t.Fatalf("the verb said %d lines, want the wait, the restart and the answer: %v", len(said), said)
	}
	if !strings.Contains(said[0], "waiting for quiet") || !strings.Contains(said[0], quietBudget()) {
		t.Errorf("the wait is not announced with its own budget (%s): %q", quietBudget(), said[0])
	}
	if !strings.Contains(said[1], "restarting") {
		t.Errorf("the restart is not announced before it happens: %q", said[1])
	}
	if !strings.Contains(said[2], "restarted") {
		t.Errorf("the sequence does not end with the answer: %q", said[2])
	}
}

// AND A SEAT THAT MISSED THE BUDGET IS NAMED WITH THE SAME DURATION, not with a floor of minutes: the sentence this
// replaces divided quietWaitSeconds by 60, so a budget that is not a whole number of minutes named a window that
// was not the window.
func TestReseatNamesTheBudgetASeatMissedWithADuration(t *testing.T) {
	var said []string
	deps, _ := reseatFixture(false)
	deps.logf = func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) }

	if code, err := reseatSeats("r", []string{"lead"}, false, deps); err != nil || code != ExitFailed {
		t.Fatalf("code %d err %v", code, err)
	}
	if len(said) != 2 {
		t.Fatalf("want the wait and the outcome, got %v", said)
	}
	if !strings.Contains(said[1], quietBudget()) || !strings.Contains(said[1], "left running") {
		t.Errorf("the missed budget is not named with the duration (%s): %q", quietBudget(), said[1])
	}
}

// THE FLAG REACHES THE DEPS, not only the parser: `seat reseat` called ResolveStateRoot("") and never read a
// --state-root at all, so a caller working in another state tree was refused the flag and quietly given the
// machine's own root - the quiet waiter and the seat doctor then read a tree the caller did not name.
func TestReseatReadsTheStateRootFlagInsteadOfTheMachineDefault(t *testing.T) {
	root := t.TempDir()
	got := ""
	old := reseatDepsFor
	reseatDepsFor = func(stateRoot string) *reseatDeps {
		got = stateRoot
		deps, _ := reseatFixture(true)
		return deps
	}
	defer func() { reseatDepsFor = old }()

	var out, errOut strings.Builder
	if code := runReseat([]string{"r", "--seat", "lead", "--state-root", root}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("seat reseat --state-root exited %d (%s); want the flag honoured", code, errOut.String())
	}
	if got != root {
		t.Fatalf("the verb handed its deps %q; want the flag's root %q", got, root)
	}

	// The runtime's retired spelling is refused by the same extraction, before any seat is touched.
	got = ""
	out.Reset()
	errOut.Reset()
	if code := runReseat([]string{"r", "--seat", "lead", "--state-dir", root}, &out, &errOut); code != clientcmd.ExitUsage || !strings.Contains(errOut.String(), "--state-root DIR") {
		t.Fatalf("--state-dir exited %d (%q); want the one refusal naming --state-root", code, errOut.String())
	}
	if got != "" {
		t.Fatalf("a refused flag still built deps for %q", got)
	}

	// The flag is advertised, or a caller reading another tree cannot discover it.
	out.Reset()
	errOut.Reset()
	if code := runReseat([]string{"--help"}, &out, &errOut); code != clientcmd.ExitOK || !strings.Contains(out.String(), "[--state-root DIR]") {
		t.Fatalf("usage does not list --state-root: %q", out.String())
	}
}
