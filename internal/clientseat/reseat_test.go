package clientseat

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// THE ELEVEN-SITE CLASS, PINNED FOR THE READING. rigNodes used to shell out to `rig ps --nodes --room RIG
// --json`, and after this client's rename that name resolves to this binary. PATH is emptied here so the
// assertion is about the ABSENCE of the call rather than about a message: with the exec in place the
// function answers "rig is not on PATH" (an exec.LookPath failure), and without it the only possible
// failure comes from the local runtime.
func TestRoomNodesNeverShellsOutToRig(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	nodes, err := roomNodes("nosuchrig")
	if err != nil && strings.Contains(err.Error(), "is not on PATH") {
		t.Fatalf("roomNodes shelled out to `rig`: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "nosuchrig") {
		t.Errorf("the failure does not name the rig it could not read: %v", err)
	}
	if err == nil && len(nodes) != 0 {
		t.Errorf("an unreadable room answered with %d node records", len(nodes))
	}
}

func reseatFixture(waitOK bool) (*reseatDeps, *[]string) {
	var log []string
	return &reseatDeps{
		nodes: func(rig string) ([]map[string]any, error) {
			return []map[string]any{
				{"logicalId": rig + ".lead"}, {"logicalId": rig + ".go-dev"},
			}, nil
		},
		waitQuiet: func(rig, seat string) bool { log = append(log, "wait "+seat); return waitOK },
		relaunch:  func(rig, seat string) error { log = append(log, "relaunch "+seat); return nil },
		checkSeat: func(rig, seat string) []string { log = append(log, "check "+seat); return nil },
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

func TestReseatTakesTheRigBeforeItsSeatFlags(t *testing.T) {
	var out, errOut strings.Builder
	if code := runReseat([]string{"--seat", "lead", "r"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Fatalf("a flag before the rig must be refused with usage, got %d", code)
	}
	if code := runReseat([]string{"r"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Fatalf("a rig with no --seat must be refused with usage, got %d", code)
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
