package clientsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const (
	deadNode  = `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","rigName":"room1","sessionStatus":"running","agentActivity":{"state":"unknown","reason":"no_runtime_hook"}}`
	aliveNode = `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","rigName":"room1","sessionStatus":"running","agentActivity":{"state":"idle","reason":null}}`
)

// rigStub answers the rig's COMMAND leg - `rig seat launch` - and records every call, so a test asserts
// what was launched and what the reading was when it decided. It does NOT answer the READ any more:
// answers is the node table the DAEMON serves, one scripted answer per reading with the last repeating
// (rigNodeDaemon below), because the verb reads a seat through clientruntime.SeatNode rather than through
// a `rig ps` exec, and a stub beside that seam would let this suite pass while the real read was broken.
// reads counts the readings the verb actually took.
type rigStub struct {
	answers []string
	launch  rigResult
	calls   [][]string
	reads   int
}

// nodeAnswer is the node table for the Nth reading: the next scripted answer, or the last one once the
// script runs out.
func (s *rigStub) nodeAnswer() string {
	answer := s.answers[len(s.answers)-1]
	if s.reads < len(s.answers) {
		answer = s.answers[s.reads]
	}
	s.reads++
	return answer
}

// rigNames is the rigs the scripted rows name, deduplicated: the daemon's rig list is what makes the node
// route askable at all, since that route is keyed on the rig's id rather than on its name.
func (s *rigStub) rigNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, answer := range s.answers {
		var rows []map[string]any
		if err := json.Unmarshal([]byte(answer), &rows); err != nil {
			continue
		}
		for _, row := range rows {
			name, _ := row["rigName"].(string)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func (s *rigStub) run(args ...string) rigResult {
	s.calls = append(s.calls, args)
	return s.launch
}

func stubRig(t *testing.T, stub *rigStub) {
	t.Helper()
	original := runRigCapture
	runRigCapture = stub.run
	t.Cleanup(func() { runRigCapture = original })
	rigNodeDaemon(t, stub.rigNames(), stub.nodeAnswer)
}

// rigNodeDaemon is the daemon the node read talks to, in the shape internal/clientrestore's world uses:
// GET /api/ps names the rigs the rows come from (the node route is keyed on the id, not the name), and
// GET /api/rigs/<rigId>/nodes carries the rows themselves. rigs is what the list advertises, and nodes
// answers the table for THIS reading, so a sequenced stub hands each reading its own answer. It serves
// BOTH verbs' read: `sync respawn` and `sync switch` now ask the daemon through the client's own
// transport, and a stub beside that seam would let a suite pass while the real read was broken.
func rigNodeDaemon(t *testing.T, rigs []string, nodes func() string) {
	t.Helper()
	daemon := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/api/ps":
			list := make([]map[string]string, 0, len(rigs))
			for _, rig := range rigs {
				list = append(list, map[string]string{"rigId": "01TESTRIG" + rig, "name": rig, "rigName": rig})
			}
			out.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(out).Encode(list)
		case strings.HasPrefix(req.URL.Path, "/api/rigs/") && strings.HasSuffix(req.URL.Path, "/nodes"):
			_, _ = io.WriteString(out, nodes())
		default:
			out.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(daemon.Close)
	t.Setenv("OPENRIG_URL", daemon.URL)
	t.Setenv("OPENRIG_PORT", "")
	// And no runtime room: the socket names a path nothing listens on, so the runtime rung of the read
	// fails fast and the daemon is the transport that answers - the order a seat's box takes.
	t.Setenv(clientruntime.RuntimeSocketEnv, filepath.Join(t.TempDir(), "no-runtime.sock"))
}

// brokenNodeDaemon names a daemon whose READS fail: the rig list answers HTTP 500, and so does the node
// table. A verb reading through it must refuse in those words rather than read the failure as a room
// without that seat - the distinction the read's three answers exist for.
func brokenNodeDaemon(t *testing.T) {
	t.Helper()
	daemon := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		out.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(out, `{"error":"the node table is unavailable"}`)
	}))
	t.Cleanup(daemon.Close)
	t.Setenv("OPENRIG_URL", daemon.URL)
	t.Setenv("OPENRIG_PORT", "")
	t.Setenv(clientruntime.RuntimeSocketEnv, filepath.Join(t.TempDir(), "no-runtime.sock"))
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

// TestRespawnRefusesWhenTheNodeReadFails: a record that could not be READ is not the answer "the room
// lists no such seat". The two used to arrive as one - a `rig ps` exec that failed with no stdout was
// parsed as an empty node list - and a respawn is a launch, so the seat it cannot see must be refused in
// the read's own words rather than composed into room.seat and launched.
func TestRespawnRefusesWhenTheNodeReadFails(t *testing.T) {
	stub := &rigStub{
		answers: []string{"[" + deadNode + "]"},
		launch:  rigResult{stdout: "fresh occupant started\n"},
	}
	stubRig(t, stub)
	brokenNodeDaemon(t)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d: an unreadable record is not an absent seat", code, clientcmd.ExitRemote)
	}
	if !strings.Contains(stderr.String(), "the node read asked /api/ps") {
		t.Errorf("stderr = %q, want the failed read rather than a missing node", stderr.String())
	}
	if got := launched(stub); got != nil {
		t.Errorf("it launched on a reading it could not make: %v", got)
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
