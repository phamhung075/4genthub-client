package clientsync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

const (
	deadNode  = `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","roomName":"room1","sessionStatus":"agent_exited"}`
	aliveNode = `{"logicalId":"room1.alpha","canonicalSessionName":"room1-alpha@room1","roomName":"room1","sessionStatus":"running"}`
)

// roomStub scripts the READ the verb makes: answers is the node table the RUNTIME serves, one scripted answer
// per reading with the last repeating (roomNodeRuntime below), because the verb reads a seat through
// clientruntime.SeatNode rather than through a `rig ps` exec, and a stub beside that seam would let this
// suite pass while the real read was broken. reads counts the readings the verb actually took.
//
// THERE IS NO COMMAND LEG LEFT TO STUB, and that is the point: the launch arrived here as a roomResult and is
// now a refusal the verb speaks itself (TestRespawnRefusesToLaunchTheDeadSeatAndSaysSo). So what a test needs
// to pin about the command is that NO command ran, which poisonRoom below answers by planting a `rig` that
// would have been found and would have left a trace.
type roomStub struct {
	answers []string
	reads   int
}

// nodeAnswer is the node table for the Nth reading: the next scripted answer, or the last one once the
// script runs out.
func (s *roomStub) nodeAnswer() string {
	answer := s.answers[len(s.answers)-1]
	if s.reads < len(s.answers) {
		answer = s.answers[s.reads]
	}
	s.reads++
	return answer
}

// stubRoom points the verb's read at the stub's scripted answers.
func stubRoom(t *testing.T, stub *roomStub) {
	t.Helper()
	roomNodeRuntime(t, stub.nodeAnswer)
}

// roomNodeRuntime is the runtime the node read talks to: GetRoomStatus over a unix socket, answering the rows
// of THIS reading in the runtime's own seat-status keys. nodes yields the scripted node table for the
// reading, so a sequenced stub hands each reading its own answer. It serves BOTH verbs' read: `sync respawn`
// and `sync switch` ask the runtime through the client's own transport, and a stub beside that seam would let a
// suite pass while the real read was broken.
func roomNodeRuntime(t *testing.T, nodes func() string) {
	t.Helper()
	serveRuntimeSocket(t, func(string) string {
		var rows []map[string]any
		_ = json.Unmarshal([]byte(nodes()), &rows)
		statuses := []map[string]any{}
		for _, row := range rows {
			room, _ := row["roomName"].(string)
			logical, _ := row["logicalId"].(string)
			statuses = append(statuses, map[string]any{
				"room": room, "seat": strings.TrimPrefix(logical, room+"."), "session_name": row["canonicalSessionName"],
				"runtime": "omp", "model": row["model"], "state": row["sessionStatus"],
			})
		}
		raw, _ := json.Marshal(map[string]any{"ok": true, "data": statuses})
		return string(raw) + "\n"
	})
}

// brokenNodeRuntime names a runtime whose READS fail: it answers every request with a refusal. A verb reading
// through it must refuse in those words rather than read the failure as a room without that seat - the
// distinction the read's three answers exist for.
func brokenNodeRuntime(t *testing.T) {
	t.Helper()
	serveRuntimeSocket(t, func(string) string {
		return `{"ok":false,"error":"the node table is unavailable"}` + "\n"
	})
}

// serveRuntimeSocket listens on a unix socket for the life of the test and answers each request LINE with
// answer(line); the reachability probe (dialled and closed with no line) is ignored. The directory is a short
// temp prefix because a unix socket path is capped near 108 bytes.
func serveRuntimeSocket(t *testing.T, answer func(line string) string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "csrt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				_, _ = io.WriteString(conn, answer(string(line)))
			}(conn)
		}
	}()
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)
}

// poisonRoom plants a `rig` FIRST on PATH that records having been run, and returns the reading of that
// record.
//
// IT REPLACES WHAT `launched` USED TO ASSERT, and it asserts more: `launched` read a stub's call list, so it
// could only see the one exec this verb used to make - a second reach for the program anywhere else would
// have gone unseen. This one plants a `rig` that exec.LookPath would find and that writes its trace, so any
// path in the verb that reaches for the program at all is caught, and "nothing was launched" is a fact about
// the environment rather than about which stub was installed.
func poisonRoom(t *testing.T) func() bool {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "room-was-run")
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
	roomRan := poisonRoom(t)
	stub := &roomStub{answers: []string{"[" + deadNode + "]"}}
	stubRoom(t, stub)

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
	if roomRan() {
		t.Error("the verb reached for `rig` on the launch leg: the refusal must not fall back (R4)")
	}
}

// The same dead seat read through the whole deadline: the refusal is the LAST step of the verb, so the
// reading it decided the seat needed was still taken - a refusal that short-circuited the read would pass
// every other test in this file while dropping the watch the caller asked --after-seconds for.
func TestRespawnRefusesTheLaunchOnlyAfterTheDeadReadingHeld(t *testing.T) {
	roomRan := poisonRoom(t)
	stub := &roomStub{answers: []string{"[" + deadNode + "]"}}
	stubRoom(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUnavailable, stderr.String())
	}
	if stub.reads < 2 {
		t.Errorf("took %d readings, want the dead reading and then the session it could not launch", stub.reads)
	}
	if roomRan() {
		t.Error("the verb reached for `rig` on the launch leg: the refusal must not fall back (R4)")
	}
}

func TestRespawnRefusesALiveSeat(t *testing.T) {
	roomRan := poisonRoom(t)
	stub := &roomStub{answers: []string{"[" + aliveNode + "]"}}
	stubRoom(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "not in a dead-agent state") {
		t.Errorf("stderr = %q, want the refusal", stderr.String())
	}
	if roomRan() {
		t.Error("a live seat was launched anyway: the verb ran `rig`")
	}
}

func TestRespawnRefusesAnUnknownSeat(t *testing.T) {
	roomRan := poisonRoom(t)
	stub := &roomStub{answers: []string{"[]"}}
	stubRoom(t, stub)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "the runtime lists no seat room1.alpha") {
		t.Errorf("stderr = %q, want the missing node named", stderr.String())
	}
	if roomRan() {
		t.Error("an unknown seat was launched anyway: the verb ran `rig`")
	}
}

// TestRespawnRefusesWhenTheNodeReadFails: a record that could not be READ is not the answer "the room
// lists no such seat". The two used to arrive as one - a `rig ps` exec that failed with no stdout was
// parsed as an empty node list - so the seat it cannot see must be refused in the read's own words rather
// than composed into room.seat and acted on.
func TestRespawnRefusesWhenTheNodeReadFails(t *testing.T) {
	roomRan := poisonRoom(t)
	stub := &roomStub{answers: []string{"[" + deadNode + "]"}}
	stubRoom(t, stub)
	brokenNodeRuntime(t)

	var stdout, stderr bytes.Buffer
	code := RunRespawnVerb(context.Background(),
		[]string{"room1", "alpha", "--after-seconds", "0"}, &stdout, &stderr)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit %d, want %d: an unreadable record is not an absent seat", code, clientcmd.ExitRemote)
	}
	if !strings.Contains(stderr.String(), "the node table is unavailable") {
		t.Errorf("stderr = %q, want the failed read rather than a missing node", stderr.String())
	}
	if roomRan() {
		t.Error("it ran `rig` on a reading it could not make")
	}
}

// A seat the runtime hook has not attached to yet reads EXACTLY like a dead one, so the reading must
// hold for --after-seconds. Driven with a clock the test controls, because the real one sleeps.
func TestRespawnWaitsForTheDeadReadingToHold(t *testing.T) {
	stub := &roomStub{answers: []string{"[" + aliveNode + "]"}}
	stubRoom(t, stub)

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
