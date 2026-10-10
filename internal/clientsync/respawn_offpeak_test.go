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
	`"model":"deepseek/deepseek-flash","sessionStatus":"running",` +
	`"agentActivity":{"state":"unknown","reason":"no_runtime_hook"}}`

// respawnGateEnv pins the clock and leaves the setting unset, with the machine's holiday file kept out.
func respawnGateEnv(t *testing.T, pin string) {
	t.Helper()
	t.Setenv(clientoffpeak.HolidaysEnv, "")
	t.Setenv(clientoffpeak.PinEnv, pin)
	t.Setenv(clientoffpeak.ModeEnv, "")
}

// TestRespawnRefusesADeadDeepSeekSeatAtPeak: this verb exists to LAUNCH a fresh occupant, so the same rule
// as `up` applies to the seat's own record - and the refusal must land before the dead-reading wait, or a
// caller who asked to wait --after-seconds would sit through it and then be refused anyway.
func TestRespawnRefusesADeepSeekSeatAtPeakBeforeAnyWait(t *testing.T) {
	stub := &rigStub{
		answers: []string{"[" + deadDeepSeekNode + "]"},
		launch:  rigResult{stdout: "fresh occupant started\n"},
	}
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
		clientoffpeak.FlagAnyTime,
		clientoffpeak.ModeEnv + "=" + clientoffpeak.ValueOff,
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, stderr.String())
		}
	}
	if got := launched(stub); got != nil {
		t.Errorf("it launched anyway: %v", got)
	}
}

// TestRespawnLaunchesADeepSeekSeatWhenTheCheckIsOff is the control: off-peak, or with the check turned off
// by the flag or the setting, the same dead seat is respawned.
func TestRespawnLaunchesADeepSeekSeatWhenTheCheckIsOff(t *testing.T) {
	cases := []struct {
		name    string
		pin     string
		modeEnv string
		extra   []string
	}{
		{"off-peak", "2026-10-09 05:00", "", nil},
		{"peak, --any-time", "2026-10-09 02:00", "", []string{clientoffpeak.FlagAnyTime}},
		{"peak, the setting is off", "2026-10-09 02:00", clientoffpeak.ValueOff, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := &rigStub{
				answers: []string{"[" + deadDeepSeekNode + "]"},
				launch:  rigResult{stdout: "fresh occupant started\n"},
			}
			stubRig(t, stub)
			t.Setenv(clientoffpeak.HolidaysEnv, "")
			t.Setenv(clientoffpeak.PinEnv, c.pin)
			t.Setenv(clientoffpeak.ModeEnv, c.modeEnv)

			args := append([]string{"room1", "alpha", "--after-seconds", "0"}, c.extra...)
			var stdout, stderr bytes.Buffer
			code := RunRespawnVerb(context.Background(), args, &stdout, &stderr)
			if code != clientcmd.ExitOK {
				t.Fatalf("exit %d, want 0 (stderr %q)", code, stderr.String())
			}
			if got := launched(stub); got == nil {
				t.Error("the seat was not respawned")
			}
		})
	}
}

// TestRespawnStillRespawnsALocalSeatAtPeak: the gate is about DeepSeek models only. A room on a local
// runtime is respawned at any hour, which is what keeps this verb usable outside the peak windows.
func TestRespawnStillRespawnsALocalSeatAtPeak(t *testing.T) {
	localNode := `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","rigName":"room1",` +
		`"model":"claude-code/sonnet","sessionStatus":"running",` +
		`"agentActivity":{"state":"unknown","reason":"no_runtime_hook"}}`
	stub := &rigStub{
		answers: []string{"[" + localNode + "]"},
		launch:  rigResult{stdout: "fresh occupant started\n"},
	}
	stubRig(t, stub)
	respawnGateEnv(t, "2026-10-09 02:00")

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want 0 (stderr %q)", code, stderr.String())
	}
	if got := launched(stub); got == nil {
		t.Error("a local seat was refused at peak")
	}
}

// TestRespawnRefusesBothModeFlags: the pair is a caller error in this verb too.
func TestRespawnRefusesBothModeFlags(t *testing.T) {
	stub := &rigStub{answers: []string{"[" + deadDeepSeekNode + "]"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(), []string{"room1", "alpha",
		clientoffpeak.FlagOffpeakOnly, clientoffpeak.FlagAnyTime}, &stdout, &stderr)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), clientoffpeak.FlagAnyTime) {
		t.Errorf("the refusal did not name the flags: %q", stderr.String())
	}
	if got := launched(stub); got != nil {
		t.Errorf("it launched anyway: %v", got)
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
	for _, want := range []string{clientoffpeak.FlagAnyTime, clientoffpeak.ModeEnv} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the usage does not name %q:\n%s", want, stdout.String())
		}
	}
}
