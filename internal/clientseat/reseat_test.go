package clientseat

import (
	"errors"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// THE ELEVEN-SITE CLASS, PINNED FOR THE READING. rigNodes used to shell out to `rig ps --nodes --rig RIG
// --json`, and after this client's rename that name resolves to this binary. PATH is emptied here so the
// assertion is about the ABSENCE of the call rather than about a message: with the exec in place the
// function answers "rig is not on PATH" (an exec.LookPath failure), and without it the only possible
// failure comes from the local runtime.
func TestRoomNodesNeverShellsOutToRig(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OPENRIG_HOME", t.TempDir())

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
	want := []string{"wait lead", "relaunch lead", "wait go-dev", "relaunch go-dev"}
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
