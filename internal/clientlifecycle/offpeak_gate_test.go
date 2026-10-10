package clientlifecycle

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
)

// deepSeekRoomNode is a room whose seat runs a DeepSeek model: the case the peak gate exists for, since
// a start or a resume of it bills the peak rate.
func deepSeekRoomNode() []map[string]any {
	return []map[string]any{{
		"logicalId":            "room-alpha.dev",
		"canonicalSessionName": "room-alpha-dev@room-alpha",
		"rigName":              "room-alpha",
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
// took an effect. ensureRig is the RESUME step: whether it ran is the difference between a refusal that
// saved the peak rate and one that only printed a sentence.
func stubWholeUp(t *testing.T) *[]string {
	t.Helper()
	ran := &[]string{}
	ensureDaemon = func(*clientcmd.Rig, io.Writer) error { *ran = append(*ran, "ensureDaemon"); return nil }
	ensureRig = func(*clientcmd.Rig, string, io.Writer) { *ran = append(*ran, "ensureRig") }
	ensureForcecompact = func(io.Writer) {}
	startBridge = func(io.Writer, io.Writer) {}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	spawnChild = func([]string, string) (supervisorChild, error) { return survivedChild{release}, nil }
	runWatchView = func(string, io.Writer) { *ran = append(*ran, "watch") }
	openUI = func(*clientcmd.Rig, io.Writer) {}
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
	rigNodes = func(*clientcmd.Rig, string) []map[string]any { return deepSeekRoomNode() }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), &clientcmd.Rig{}, []string{"room-alpha"}, &out, &errOut)
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
		clientoffpeak.FlagAnyTime,
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
		{"peak, --any-time", "2026-10-09 02:00", "", []string{clientoffpeak.FlagAnyTime}, clientcmd.ExitOK},
		{"peak, the setting is off", "2026-10-09 02:00", clientoffpeak.ValueOff, nil, clientcmd.ExitOK},
		{"peak, the setting is off but --offpeak-only", "2026-10-09 02:00", clientoffpeak.ValueOff,
			[]string{clientoffpeak.FlagOffpeakOnly}, clientcmd.ExitUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restoreAll(t)
			ran := stubWholeUp(t)
			rigNodes = func(*clientcmd.Rig, string) []map[string]any { return deepSeekRoomNode() }
			t.Setenv(clientoffpeak.HolidaysEnv, "")
			t.Setenv(clientoffpeak.PinEnv, c.pin)
			t.Setenv(clientoffpeak.ModeEnv, c.modeEnv)

			args := append([]string{"room-alpha"}, c.extra...)
			var out, errOut bytes.Buffer
			code := (upCommand{}).Run(context.Background(), &clientcmd.Rig{}, args, &out, &errOut)
			if code != c.want {
				t.Fatalf("exit %d, want %d (stderr %q)", code, c.want, errOut.String())
			}
			resumed := strings.Contains(strings.Join(*ran, ","), "ensureRig")
			if wantResumed := c.want == clientcmd.ExitOK; resumed != wantResumed {
				t.Errorf("resumed = %v, want %v (steps %v)", resumed, wantResumed, *ran)
			}
		})
	}
}

// TestUpRefusesBothModeFlagsAsACallerError: a caller who wrote both meant neither, and guessing one of
// them could start DeepSeek work the operator asked not to start.
func TestUpRefusesBothModeFlagsAsACallerError(t *testing.T) {
	restoreAll(t)
	rigNodes = func(*clientcmd.Rig, string) []map[string]any { return deepSeekRoomNode() }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), &clientcmd.Rig{},
		[]string{"room-alpha", clientoffpeak.FlagOffpeakOnly, clientoffpeak.FlagAnyTime}, &out, &errOut)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), clientoffpeak.FlagOffpeakOnly) {
		t.Errorf("the refusal did not name the flags: %q", errOut.String())
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
	rigNodes = func(*clientcmd.Rig, string) []map[string]any { return deepSeekRoomNode() }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (compactCommand{}).Run(context.Background(), nil, []string{"room-alpha"}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if spawned {
		t.Error("the supervisor was started anyway")
	}
	for _, want := range []string{"room-alpha.dev", "deepseek/deepseek-flash", clientoffpeak.FlagAnyTime} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, errOut.String())
		}
	}
}

// TestCompactStartsTheSupervisorWhenTheCheckIsOff is the control: the same room, off-peak or with the
// check turned off, still gets its supervisor.
func TestCompactStartsTheSupervisorWhenTheCheckIsOff(t *testing.T) {
	for _, c := range []struct {
		name  string
		pin   string
		extra []string
	}{
		{"off-peak", "2026-10-09 05:00", nil},
		{"--any-time at peak", "2026-10-09 02:00", []string{clientoffpeak.FlagAnyTime}},
	} {
		t.Run(c.name, func(t *testing.T) {
			restoreAll(t)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			var spawned bool
			spawnChild = func([]string, string) (supervisorChild, error) {
				spawned = true
				return survivedChild{release}, nil
			}
			SupervisorStartGrace = time.Millisecond
			rigNodes = func(*clientcmd.Rig, string) []map[string]any { return deepSeekRoomNode() }
			gateEnv(t, c.pin)

			args := append([]string{"room-alpha"}, c.extra...)
			var out, errOut bytes.Buffer
			code := (compactCommand{}).Run(context.Background(), nil, args, &out, &errOut)
			if code != clientcmd.ExitOK {
				t.Fatalf("exit %d, want 0 (stderr %q)", code, errOut.String())
			}
			if !spawned {
				t.Error("the supervisor was not started")
			}
		})
	}
}

// TestTheGateIsSkippedWhenTheRoomCannotBeRead: a reading that fails is not a refusal. The verbs each say
// what is wrong in their own words - guessing "peak" from a missing reading would block a room the client
// knows nothing about.
func TestTheGateIsSkippedWhenTheRoomCannotBeRead(t *testing.T) {
	restoreAll(t)
	ran := stubWholeUp(t)
	rigNodes = func(*clientcmd.Rig, string) []map[string]any { return nil }
	gateEnv(t, "2026-10-09 02:00")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), &clientcmd.Rig{}, []string{"room-alpha"}, &out, &errOut)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want 0: an unreadable room must not be refused (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(strings.Join(*ran, ","), "ensureRig") {
		t.Error("an unreadable room must still be restored, so its own error can be reported")
	}
}

// TestAnUnreadableClockIsACallerErrorNotARefusal: a malformed OFFPEAK_UTC is a typo in the environment,
// and this client fails loudly on those rather than guessing a clock.
func TestAnUnreadableClockIsACallerErrorNotARefusal(t *testing.T) {
	restoreAll(t)
	stubWholeUp(t)
	rigNodes = func(*clientcmd.Rig, string) []map[string]any { return deepSeekRoomNode() }
	gateEnv(t, "whenever")

	var out, errOut bytes.Buffer
	code := (upCommand{}).Run(context.Background(), &clientcmd.Rig{}, []string{"room-alpha"}, &out, &errOut)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), clientoffpeak.PinEnv) {
		t.Errorf("the error does not name the variable: %q", errOut.String())
	}
}
