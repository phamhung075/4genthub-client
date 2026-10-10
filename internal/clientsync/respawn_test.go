package clientsync

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

const (
	deadNode  = `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","rigName":"room1","sessionStatus":"running","agentActivity":{"state":"unknown","reason":"no_runtime_hook"}}`
	aliveNode = `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","rigName":"room1","sessionStatus":"running","agentActivity":{"state":"idle","reason":null}}`
)

// rigStub answers `rig ps` from a scripted list - the last answer repeats - and records every call, so
// a test asserts what was launched and what the reading was when it decided.
type rigStub struct {
	answers []string
	launch  rigResult
	calls   [][]string
	reads   int
}

func (s *rigStub) run(args ...string) rigResult {
	s.calls = append(s.calls, args)
	if args[0] != "ps" {
		return s.launch
	}
	answer := s.answers[len(s.answers)-1]
	if s.reads < len(s.answers) {
		answer = s.answers[s.reads]
	}
	s.reads++
	return rigResult{stdout: answer}
}

func stubRig(t *testing.T, stub *rigStub) {
	t.Helper()
	original := runRigCapture
	runRigCapture = stub.run
	t.Cleanup(func() { runRigCapture = original })
}

func launched(stub *rigStub) []string {
	for _, call := range stub.calls {
		if call[0] == "seat" {
			return call
		}
	}
	return nil
}

// The six behaviours the Python tests pin: it launches only for a seat that holds the dead reading,
// it judges the launch by the seat's state rather than by the exit code, and it refuses an unknown or
// a live seat without launching anything.
func TestRespawnLaunchesOnlyWhenTheSeatReadsDead(t *testing.T) {
	stub := &rigStub{answers: []string{"[" + deadNode + "]"}, launch: rigResult{stdout: "fresh occupant started\n"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fresh occupant started") {
		t.Errorf("stdout = %q, want the launch's own report", stdout.String())
	}
	want := []string{"seat", "launch", "room1-alpha@room1", "--fresh", "--stop", "--reason", defaultRespawnReason}
	if got := launched(stub); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("launched %v, want %v", got, want)
	}
}

func TestRespawnReportsSuccessWhenTheLaunchWarnedButTheSeatCameUp(t *testing.T) {
	stub := &rigStub{
		answers: []string{"[" + deadNode + "]", "[" + aliveNode + "]"},
		launch: rigResult{
			stderr: "Fresh occupant started but runtime identity requires attention\n",
			err:    errors.New("exit status 1"),
		},
	}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d: a seat that came up is a successful respawn", code, clientcmd.ExitOK)
	}
	if !strings.Contains(stderr.String(), "rig seat launch warned: Fresh occupant started but runtime identity requires attention") {
		t.Errorf("stderr = %q, want the launch's warning and the fact the seat came up", stderr.String())
	}
}

func TestRespawnFailsWhenTheSeatIsStillDeadAfterTheLaunch(t *testing.T) {
	stub := &rigStub{
		answers: []string{"[" + deadNode + "]"},
		launch:  rigResult{stderr: "cannot attach\n", err: errors.New("exit status 1")},
	}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitRemote)
	}
	if !strings.Contains(stderr.String(), "rig seat launch reported: cannot attach") {
		t.Errorf("stderr = %q, want the launch's message rather than a traceback", stderr.String())
	}
}

func TestRespawnRefusesALiveSeat(t *testing.T) {
	stub := &rigStub{answers: []string{"[" + aliveNode + "]"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "not in the dead-agent state") {
		t.Errorf("stderr = %q, want the refusal", stderr.String())
	}
	if got := launched(stub); got != nil {
		t.Errorf("a live seat was launched anyway: %v", got)
	}
}

func TestRespawnRefusesAnUnknownSeat(t *testing.T) {
	stub := &rigStub{answers: []string{"[]"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "OpenRig lists no node room1.alpha") {
		t.Errorf("stderr = %q, want the missing node named", stderr.String())
	}
	if got := launched(stub); got != nil {
		t.Errorf("an unknown seat was launched anyway: %v", got)
	}
}

// A seat the runtime hook has not attached to yet reads EXACTLY like a dead one, so the reading must
// hold for --after-seconds. Driven with a clock the test controls, because the real one sleeps.
func TestRespawnWaitsForTheDeadReadingToHold(t *testing.T) {
	stub := &rigStub{answers: []string{"[" + aliveNode + "]"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "30"}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d: a live reading refuses before any deadline is waited out", code, clientcmd.ExitUsage)
	}
	if reads := stub.reads; reads != 1 {
		t.Errorf("took %d readings of a seat that was never dead, want 1", reads)
	}
}
