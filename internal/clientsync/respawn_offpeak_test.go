package clientsync

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
)

// deadDeepSeekNode is room1.alpha's record when its agent has died AND it runs a DeepSeek model: the one
// record where a respawn would start DeepSeek work, which at peak is what the gate refuses.
const deadDeepSeekNode = `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","rigName":"room1",` +
	`"model":"deepseek/deepseek-flash","sessionStatus":"agent_exited"}`

// respawnGateEnv pins the clock and leaves the setting unset, with the machine's holiday file kept out.
func respawnGateEnv(t *testing.T, pin string) {
	t.Helper()
	t.Setenv(clientoffpeak.HolidaysEnv, "")
	t.Setenv(clientoffpeak.PinEnv, pin)
	t.Setenv(clientoffpeak.ModeEnv, "")
}

// TestRespawnRefusesADeadDeepSeekSeatAtPeak: this verb decides on a LAUNCH, so the same rule as `up` applies
// to the seat's own record - and the refusal must land before the dead-reading wait, or a caller who asked to
// wait --after-seconds would sit through it and then be refused anyway.
func TestRespawnRefusesADeepSeekSeatAtPeakBeforeAnyWait(t *testing.T) {
	rigRan := poisonRig(t)
	stub := &rigStub{answers: []string{"[" + deadDeepSeekNode + "]"}}
	stubRig(t, stub)
	respawnGateEnv(t, "2026-10-09 02:00")

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "300"}, &stdout, &stderr)

	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, stderr.String())
	}
	for _, want := range []string{
		"room1.alpha",
		"deepseek/deepseek-flash",
		"PEAK: 01:00-04:00 UTC",
		clientoffpeak.FlagAllowPeak,
		clientoffpeak.ModeEnv + "=" + clientoffpeak.ValueOff,
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, stderr.String())
		}
	}
	if got := stub.reads; got != 1 {
		t.Errorf("took %d readings, want 1: the gate refuses before the dead reading is waited out", got)
	}
	if rigRan() {
		t.Error("it ran `rig` anyway")
	}
}

// TestRespawnPassesADeepSeekSeatThroughTheGateWhenTheCheckIsOff is the control: off-peak, or with the check
// turned off by the flag or the setting, the gate does NOT refuse - the verb reaches its last step, which in
// this build is the launch refusal (the native runtime has no launch verb). The distinction this pins is
// WHICH refusal is spoken: the launch one, never the peak one, or an operator would chase a billing window
// for a step nothing can take.
func TestRespawnPassesADeepSeekSeatThroughTheGateWhenTheCheckIsOff(t *testing.T) {
	cases := []struct {
		name    string
		pin     string
		modeEnv string
		extra   []string
	}{
		{"off-peak", "2026-10-09 05:00", "", nil},
		{"peak, --any-time", "2026-10-09 02:00", "", []string{clientoffpeak.FlagAllowPeak}},
		{"peak, the setting is off", "2026-10-09 02:00", clientoffpeak.ValueOff, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rigRan := poisonRig(t)
			stub := &rigStub{answers: []string{"[" + deadDeepSeekNode + "]"}}
			stubRig(t, stub)
			t.Setenv(clientoffpeak.HolidaysEnv, "")
			t.Setenv(clientoffpeak.PinEnv, c.pin)
			t.Setenv(clientoffpeak.ModeEnv, c.modeEnv)

			args := append([]string{"room1", "alpha", "--after-seconds", "0"}, c.extra...)
			var stdout, stderr bytes.Buffer
			code := RunRespawnVerb(context.Background(), args, &stdout, &stderr)
			if code != clientcmd.ExitUnavailable {
				t.Fatalf("exit %d, want %d: the seat passed the gate, so the launch leg is the answer "+
					"(stderr %q)", code, clientcmd.ExitUnavailable, stderr.String())
			}
			if !strings.Contains(stderr.String(), "the native runtime has no launch verb") {
				t.Errorf("the verb did not reach the launch leg:\n%s", stderr.String())
			}
			if strings.Contains(stderr.String(), "PEAK:") {
				t.Errorf("the gate refused a seat it was told to let through:\n%s", stderr.String())
			}
			if rigRan() {
				t.Error("it ran `rig`")
			}
		})
	}
}

// TestRespawnPassesALocalSeatThroughTheGateAtPeak: the gate is about DeepSeek models only, so a room on a
// local runtime reaches the launch leg at any hour - which is what keeps this verb's gate honest outside the
// peak windows.
func TestRespawnPassesALocalSeatThroughTheGateAtPeak(t *testing.T) {
	localNode := `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","rigName":"room1",` +
		`"model":"claude-code/sonnet","sessionStatus":"agent_exited"}`
	rigRan := poisonRig(t)
	stub := &rigStub{answers: []string{"[" + localNode + "]"}}
	stubRig(t, stub)
	respawnGateEnv(t, "2026-10-09 02:00")

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)
	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d: a local seat passes the gate and meets the launch refusal "+
			"(stderr %q)", code, clientcmd.ExitUnavailable, stderr.String())
	}
	if !strings.Contains(stderr.String(), "the native runtime has no launch verb") {
		t.Errorf("the verb did not reach the launch leg:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "PEAK:") {
		t.Errorf("a local seat was refused at peak:\n%s", stderr.String())
	}
	if rigRan() {
		t.Error("it ran `rig`")
	}
}

// TestRespawnRefusesAFlagItDoesNotTake: the gate has ONE flag, and this verb's parser already refuses an
// argument it does not know, so the name this package used to invent is a caller error rather than a
// silent no-op.
func TestRespawnRefusesAFlagItDoesNotTake(t *testing.T) {
	rigRan := poisonRig(t)
	stub := &rigStub{answers: []string{"[" + deadDeepSeekNode + "]"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(), []string{"room1", "alpha", "--any-time"}, &stdout, &stderr)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--any-time") {
		t.Errorf("the refusal did not name the argument: %q", stderr.String())
	}
	if rigRan() {
		t.Error("it ran `rig` anyway")
	}
}

// TestRespawnHelpNamesTheGate: `--help` is the one place an operator learns that this verb is gated, so
// the usage must say which variables and flags decide it.
func TestRespawnHelpNamesTheGate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	for _, want := range []string{clientoffpeak.FlagAllowPeak, clientoffpeak.ModeEnv} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the usage does not name %q:\n%s", want, stdout.String())
		}
	}
}
