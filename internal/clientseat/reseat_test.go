package clientseat

import (
	"errors"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

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
