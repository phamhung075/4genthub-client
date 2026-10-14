package clientsync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// rigStub scripts the READ the verb makes: answers is the node table the DAEMON serves, one scripted answer
// per reading with the last repeating (rigNodeDaemon below), because the verb reads a seat through
// clientruntime.SeatNode rather than through a `rig ps` exec, and a stub beside that seam would let this
// suite pass while the real read was broken. reads counts the readings the verb actually took.
//
// THERE IS NO COMMAND LEG LEFT TO STUB, and that is the point: the launch arrived here as a rigResult and is
// now a refusal the verb speaks itself (TestRespawnRefusesToLaunchTheDeadSeatAndSaysSo). So what a test needs
// to pin about the command is that NO command ran, which poisonRig below answers by planting a `rig` that
// would have been found and would have left a trace.
type rigStub struct {
	answers []string
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

// stubRig points the verb's read at the stub's scripted answers.
func stubRig(t *testing.T, stub *rigStub) {
	t.Helper()
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

// poisonRig plants a `rig` FIRST on PATH that records having been run, and returns the reading of that
// record.
//
// IT REPLACES WHAT `launched` USED TO ASSERT, and it asserts more: `launched` read a stub's call list, so it
// could only see the one exec this verb used to make - a second reach for the program anywhere else would
// have gone unseen. This one plants a `rig` that exec.LookPath would find and that writes its trace, so any
// path in the verb that reaches for the program at all is caught, and "nothing was launched" is a fact about
// the environment rather than about which stub was installed.
func poisonRig(t *testing.T) func() bool {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "rig-was-run")
	script := "#!/bin/sh\n: > '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "rig"), []byte(script), 0o755); err != nil {
		t.Fatalf("planting a `rig` on PATH: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
}

// The behaviours the Python tests pin, and the one this build cannot perform: the seat's own record is the
// one that decides, an unknown or a live seat is refused without any command being run, an unreadable record
// refuses in the read's own words, and the dead reading must hold for --after-seconds - after which the
// launch REFUSES in words, because the native runtime has no launch verb for it (C3 §4).
//
// The two behaviours the old launch seam pinned are DELETED WITH IT, not retargeted: "judge the launch by
// the seat's state rather than by the exit code" and "a warned launch over a seat that came up is a success"
// were both about reading a `rig` invocation, and this verb makes none. What replaces them is the pin that
// no invocation happens at all.
func TestRespawnRefusesToLaunchTheDeadSeatAndSaysSo(t *testing.T) {
	rigRan := poisonRig(t)
	stub := &rigStub{answers: []string{"[" + deadNode + "]"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d: a launch this build cannot make is not a success (stderr %q)",
			code, clientcmd.ExitUnavailable, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing at all: the refusal is spoken on stderr", stdout.String())
	}
	for _, want := range []string{
		"cannot launch room1-alpha@room1",
		"the native runtime has no launch verb",
		"4genteam room reseat",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, stderr.String())
		}
	}
	if rigRan() {
		t.Error("the verb reached for `rig` on the launch leg: the refusal must not fall back (R4)")
	}
}

// The same dead seat read through the whole deadline: the refusal is the LAST step of the verb, so the
// reading it decided the seat needed was still taken - a refusal that short-circuited the read would pass
// every other test in this file while dropping the watch the caller asked --after-seconds for.
func TestRespawnRefusesTheLaunchOnlyAfterTheDeadReadingHeld(t *testing.T) {
	rigRan := poisonRig(t)
	stub := &rigStub{answers: []string{"[" + deadNode + "]"}}
	stubRig(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, stderr.String())
	}
	if stub.reads < 2 {
		t.Errorf("took %d readings, want the dead reading and then the session it could not launch", stub.reads)
	}
	if rigRan() {
		t.Error("the verb reached for `rig` on the launch leg: the refusal must not fall back (R4)")
	}
}

func TestRespawnRefusesALiveSeat(t *testing.T) {
	rigRan := poisonRig(t)
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
	if rigRan() {
		t.Error("a live seat was launched anyway: the verb ran `rig`")
	}
}

func TestRespawnRefusesAnUnknownSeat(t *testing.T) {
	rigRan := poisonRig(t)
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
	if rigRan() {
		t.Error("an unknown seat was launched anyway: the verb ran `rig`")
	}
}

// TestRespawnRefusesWhenTheNodeReadFails: a record that could not be READ is not the answer "the room
// lists no such seat". The two used to arrive as one - a `rig ps` exec that failed with no stdout was
// parsed as an empty node list - so the seat it cannot see must be refused in the read's own words rather
// than composed into room.seat and acted on.
func TestRespawnRefusesWhenTheNodeReadFails(t *testing.T) {
	rigRan := poisonRig(t)
	stub := &rigStub{answers: []string{"[" + deadNode + "]"}}
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
	if rigRan() {
		t.Error("it ran `rig` on a reading it could not make")
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
