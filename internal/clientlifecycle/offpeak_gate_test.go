package clientlifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// deepSeekRoomNode is a room whose seat runs a DeepSeek model: the case the peak gate exists for, since
// a start or a resume of it bills the peak rate.
func deepSeekRoomNode() []map[string]any {
	return []map[string]any{{
		"logicalId":            "room-alpha.dev",
		"canonicalSessionName": "room-alpha-dev@room-alpha",
		"roomName":             "room-alpha",
		"model":                "deepseek/deepseek-flash",
		"sessionStatus":        "stopped",
	}}
}

// gateEnv pins the clock and leaves the setting unset, so every case below is decided by the schedule
// alone unless it says otherwise. HolidaysEnv is emptied so the machine's own holiday file cannot change
// what these tests assert.
func gateEnv(t *testing.T, pin string) {
	t.Helper()
	t.Setenv(clientoffpeak.HolidaysEnv, "")
	t.Setenv(clientoffpeak.PinEnv, pin)
	t.Setenv(clientoffpeak.ModeEnv, "")
}

// stubWholeUp makes `up` runnable to its end without touching this machine, and records the steps that
// took an effect. ensureRoom is the RESUME step: whether it ran is the difference between a refusal that
// saved the peak rate and one that only printed a sentence.
func stubWholeUp(t *testing.T) *[]string {
	t.Helper()
	ran := &[]string{}
	ensureDaemon = func(string, io.Writer) (string, error) {
		*ran = append(*ran, "ensureDaemon")
		return "/tmp/daemon.log", nil
	}
	ensureRoom = func(string, clientruntime.RoomSpec, string, io.Writer, io.Writer) int {
		*ran = append(*ran, "ensureRoom")
		return clientcmd.ExitOK
	}
	// The room recorded and the authority started are the steps after the gate; a test about the gate
	// asserts whether the RESUME ran, so these two are stubbed to no-ops that cannot touch this machine.
	recordRoomFor = func(string, io.Writer) bool { return true }
	startAuthority = func(string, io.Writer, io.Writer) bool { return true }
	waitSafeguards = func(string, string) ([]clientservices.Health, bool, error) {
		return []clientservices.Health{{Safeguard: clientservices.Agent, State: clientservices.Running}}, true, nil
	}
	// The gate passes only for a `up` that may report, so the credentials the missing-env refusal would
	// otherwise catch are set here and never printed.
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTHUB_TOKEN", "not-a-real-token")
	t.Setenv("AGENTHUB_CONNECTOR_TOKEN", "not-a-real-token")
	runWatchView = func(string, io.Writer) { *ran = append(*ran, "watch") }
	openUI = func(io.Writer) {}
	runHerdr = func(io.Writer, io.Writer) int { return clientcmd.ExitOK }
	SupervisorStartGrace = time.Millisecond
	t.Setenv("HERDR_ENV", "1")
	return ran
}

// TestUpRefusesAtPeakWhenTheRoomRunsADeepSeekSeat. A refusal must name the seat, the model and the clock
// it is standing on, and must stop BEFORE the resume: that step is what would have started DeepSeek work
// at the peak rate. The daemon may already have been ensured - the models are read from it.
func TestUpRefusesAtPeakWhenTheRoomRunsADeepSeekSeat(t *testing.T) {
	restoreAll(t)
	ran := stubWholeUp(t)
	roomNodes = func(string, string) ([]map[string]any, bool, error) { return deepSeekRoomNode(), true, nil }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"room-alpha", "--team", writeRoomDefinition(t, "room-alpha")}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if strings.Join(*ran, ",") != "ensureDaemon" {
		t.Errorf("steps = %v, want the daemon and NOT the resume", *ran)
	}
	for _, want := range []string{
		"room-alpha.dev",
		"deepseek/deepseek-flash",
		"PEAK: 01:00-04:00 UTC",
		"04:00 UTC (off-peak resumes)",
		clientoffpeak.FlagAllowPeak,
		clientoffpeak.ModeEnv + "=" + clientoffpeak.ValueOff,
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, errOut.String())
		}
	}
	if strings.Contains(out.String(), "pid") {
		t.Errorf("a refused up printed a pid: %q", out.String())
	}
}

// TestUpProceedsOffPeakOrWhenTheCheckIsOff is the control for the refusal, and the precedence the
// operator has: off-peak passes, each way of turning the check off passes, and the flag outranks the
// environment.
func TestUpProceedsOffPeakOrWhenTheCheckIsOff(t *testing.T) {
	cases := []struct {
		name    string
		pin     string
		modeEnv string
		extra   []string
		want    int
	}{
		{"off-peak", "2026-10-09 05:00", "", nil, clientcmd.ExitOK},
		{"weekend", "2026-10-10 12:00", "", nil, clientcmd.ExitOK},
		{"peak, " + clientoffpeak.FlagAllowPeak, "2026-10-09 02:00", "", []string{clientoffpeak.FlagAllowPeak}, clientcmd.ExitOK},
		{"peak, the setting is off", "2026-10-09 02:00", clientoffpeak.ValueOff, nil, clientcmd.ExitOK},
		{"off-peak, " + clientoffpeak.FlagAllowPeak, "2026-10-09 05:00", "", []string{clientoffpeak.FlagAllowPeak}, clientcmd.ExitOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restoreAll(t)
			ran := stubWholeUp(t)
			roomNodes = func(string, string) ([]map[string]any, bool, error) { return deepSeekRoomNode(), true, nil }
			t.Setenv(clientoffpeak.HolidaysEnv, "")
			t.Setenv(clientoffpeak.PinEnv, c.pin)
			t.Setenv(clientoffpeak.ModeEnv, c.modeEnv)

			args := append([]string{"room-alpha", "--team", writeRoomDefinition(t, "room-alpha")}, c.extra...)
			var out, errOut bytes.Buffer
			code := (upCommand{}).Run(context.Background(), args, &out, &errOut)
			if code != c.want {
				t.Fatalf("exit %d, want %d (stderr %q)", code, c.want, errOut.String())
			}
			resumed := strings.Contains(strings.Join(*ran, ","), "ensureRoom")
			if wantResumed := c.want == clientcmd.ExitOK; resumed != wantResumed {
				t.Errorf("resumed = %v, want %v (steps %v)", resumed, wantResumed, *ran)
			}
		})
	}
}

// TestUpRefusesAFlagItDoesNotTake: the gate has ONE flag, so an argument this verb does not know is a
// caller error naming it - whether it sits where the room name goes or after it, because the failure
// shape this replaced was a verb that quietly ignored the extra argument.
//
// The code is 3, not the CLI-wide ExitUsage (2): for this verb 2 is a PARTIAL START - the daemon started
// but a seat did not (design §2.5) - so the two numbers must not mean the same thing.
func TestUpRefusesAFlagItDoesNotTake(t *testing.T) {
	restoreAll(t)
	roomNodes = func(string, string) ([]map[string]any, bool, error) { return deepSeekRoomNode(), true, nil }
	gateEnv(t, "2026-10-09 02:00")

	for _, args := range [][]string{
		{"room-alpha", "--any-time"},
		{"--any-time", "room-alpha"},
		{"room-alpha", clientoffpeak.FlagAllowPeak, clientoffpeak.FlagAllowPeak, "extra"},
	} {
		var out, errOut bytes.Buffer
		code := (upCommand{}).Run(context.Background(), args, &out, &errOut)
		if code != clientcmd.ExitRoomRefused {
			t.Fatalf("%v: exit %d, want %d (stderr %q)", args, code, clientcmd.ExitRoomRefused, errOut.String())
		}
		if !strings.Contains(errOut.String(), "--any-time") && strings.Contains(strings.Join(args, " "), "--any-time") {
			t.Errorf("%v: the refusal did not name the argument: %q", args, errOut.String())
		}
	}
}

// TestUpTakesTheOneFlagTwice is the same rule from the other side: the flag this verb DOES take may sit
// anywhere, and naming it twice is the same request twice rather than a caller error.
func TestUpTakesTheOneFlagTwice(t *testing.T) {
	restoreAll(t)
	ran := stubWholeUp(t)
	roomNodes = func(string, string) ([]map[string]any, bool, error) { return deepSeekRoomNode(), true, nil }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{clientoffpeak.FlagAllowPeak, "room-alpha", clientoffpeak.FlagAllowPeak, "--team", writeRoomDefinition(t, "room-alpha")}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want 0 (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(strings.Join(*ran, ","), "ensureRoom") {
		t.Errorf("steps = %v, want the resume to have run", *ran)
	}
}

// TestCompactRefusesAtPeakWhenTheRoomRunsADeepSeekSeat: the supervisor starts a room's seats, so the same
// rule applies, and the refusal must come before the supervisor is spawned.
func TestCompactRefusesAtPeakWhenTheRoomRunsADeepSeekSeat(t *testing.T) {
	restoreAll(t)
	var spawned bool
	spawnChild = func([]string, string) (supervisorChild, error) {
		spawned = true
		return diedChild{}, nil
	}
	SupervisorStartGrace = time.Millisecond
	roomNodes = func(string, string) ([]map[string]any, bool, error) { return deepSeekRoomNode(), true, nil }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (compactCommand{}).Run(context.Background(), []string{"room-alpha"}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if spawned {
		t.Error("the supervisor was started anyway")
	}
	for _, want := range []string{"room-alpha.dev", "deepseek/deepseek-flash", clientoffpeak.FlagAllowPeak} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, errOut.String())
		}
	}
}

// TestCompactProceedsWhenTheCheckIsOff is the control: the same room, off-peak or with the check turned
// off, still passes the gate. What the verb DOES afterwards changed with 7.1 - it records the room and
// reports the row instead of starting a supervisor - so this asserts the gate's decision alone: no
// refusal, and nothing spawned.
func TestCompactProceedsWhenTheCheckIsOff(t *testing.T) {
	for _, c := range []struct {
		name  string
		pin   string
		extra []string
	}{
		{"off-peak", "2026-10-09 05:00", nil},
		{clientoffpeak.FlagAllowPeak + " at peak", "2026-10-09 02:00", []string{clientoffpeak.FlagAllowPeak}},
	} {
		t.Run(c.name, func(t *testing.T) {
			restoreAll(t)
			var spawned bool
			spawnChild = func([]string, string) (supervisorChild, error) {
				spawned = true
				return diedChild{}, nil
			}
			SupervisorStartGrace = time.Millisecond
			agentRunning = func() (int, error) { return 4242, nil }
			rowsFor = func(string, string) ([]clientservices.Health, error) {
				return []clientservices.Health{{Room: "room-alpha", Safeguard: clientservices.Compact, State: clientservices.Running}}, nil
			}
			roomNodes = func(string, string) ([]map[string]any, bool, error) { return deepSeekRoomNode(), true, nil }
			gateEnv(t, c.pin)

			args := append([]string{"room-alpha"}, c.extra...)
			var out, errOut bytes.Buffer
			code := (compactCommand{}).Run(context.Background(), args, &out, &errOut)
			if code != clientcmd.ExitOK {
				t.Fatalf("exit %d, want 0 (stderr %q)", code, errOut.String())
			}
			if spawned {
				t.Error("compact spawned a supervisor, and agent is the only restart authority")
			}
		})
	}
}

// TestTheGateDecidesAFirstStartOnTheDefinition: a room with NO RECORDS is the shape of a first start, and `up`
// has the definition in hand, so the gate decides on the seats it declares. A definition with no DeepSeek seat
// passes, and nothing is said: the gate DID run. (The room-with-nothing-known case, where it did not, is
// TestPeakGateSaysWhenItDidNotRunForARoomItHasNoRecordsOf.)
func TestTheGateDecidesAFirstStartOnTheDefinition(t *testing.T) {
	restoreAll(t)
	ran := stubWholeUp(t)
	roomNodes = func(string, string) ([]map[string]any, bool, error) { return nil, false, nil }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"room-alpha", "--team", writeRoomDefinition(t, "room-alpha")}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want 0: the definition declares no DeepSeek seat (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(strings.Join(*ran, ","), "ensureRoom") {
		t.Error("a room with no records must still be restored, so its own error can be reported")
	}
	if strings.Contains(errOut.String(), "did NOT run") {
		t.Errorf("the gate decided on the definition, so it must not say it did not run: %q", errOut.String())
	}
}

// TestAFailedNodeReadRefusesAtTheGate is the contract this pass changed: returning OK when the reading could
// not be taken is how a peak-time resume proceeded with the gate never having decided. A FAILED read now
// refuses in words, and the verb does not go on to start anything.
func TestAFailedNodeReadRefusesAtTheGate(t *testing.T) {
	restoreAll(t)
	ran := stubWholeUp(t)
	roomNodes = func(string, string) ([]map[string]any, bool, error) {
		return nil, false, errors.New("the node read asked /api/ps and got HTTP 500")
	}
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"room-alpha", "--team", writeRoomDefinition(t, "room-alpha")}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d: a reading that failed must refuse (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	for _, want := range []string{"could not be read", "HTTP 500"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal does not carry %q: %q", want, errOut.String())
		}
	}
	if strings.Contains(strings.Join(*ran, ","), "ensureRoom") {
		t.Error("the resume ran although the gate could not decide")
	}
}

// TestAnUnreadableClockIsACallerErrorNotARefusal: a malformed OFFPEAK_UTC is a typo in the environment,
// and this client fails loudly on those rather than guessing a clock.
func TestAnUnreadableClockIsACallerErrorNotARefusal(t *testing.T) {
	restoreAll(t)
	stubWholeUp(t)
	roomNodes = func(string, string) ([]map[string]any, bool, error) { return deepSeekRoomNode(), true, nil }
	gateEnv(t, "whenever")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"room-alpha", "--team", writeRoomDefinition(t, "room-alpha")}, &out, &errOut)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), clientoffpeak.PinEnv) {
		t.Errorf("the error does not name the variable: %q", errOut.String())
	}
}

// A FIRST START HAS NO RECORDS, and `up` knows what it is about to run: the definition's seats. At peak a
// definition that declares a DeepSeek seat is refused before the room is touched, instead of proceeding with a
// printed "the gate did NOT run".
func TestUpRefusesAtPeakAFirstStartWhoseDefinitionDeclaresADeepSeekSeat(t *testing.T) {
	restoreAll(t)
	ran := stubWholeUp(t)
	roomNodes = func(string, string) ([]map[string]any, bool, error) { return nil, false, nil }
	gateEnv(t, "2026-10-09 02:00")

	directory := t.TempDir()
	body := `{"room":{"slug":"room-alpha"},"seats":[{"seat_key":"dev","seat_type":"developer","runtime":"omp","model":"deepseek/deepseek-flash"}],"links":[],"seat_overlays":{}}`
	if err := os.WriteFile(filepath.Join(directory, "team.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), []string{"room-alpha", "--team", directory}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if strings.Join(*ran, ",") != "ensureDaemon" {
		t.Errorf("steps = %v, want the daemon and NOT the room", *ran)
	}
	if !strings.Contains(errOut.String(), "room-alpha.dev") || !strings.Contains(errOut.String(), "deepseek/deepseek-flash") {
		t.Errorf("the refusal does not name the declared seat and model:\n%s", errOut.String())
	}
}
