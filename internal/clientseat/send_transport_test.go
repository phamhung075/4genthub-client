package clientseat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// noRuntimeSocket points the runtime transport at a path nothing can be listening on, which is how a
// test says "there is no runtime here" instead of being answered by the machine's own.
func noRuntimeSocket(t *testing.T) {
	t.Helper()
	t.Setenv(clientruntime.RuntimeSocketEnv, filepath.Join(t.TempDir(), "no-runtime.sock"))
}

// THE SEND PATH DECIDES BEFORE IT DELIVERS, so every case that wants a delivery has to declare who may send to
// whom, and every case at all needs a store to write its audit lines into. These two keep that out of the
// assertions: the links are what a room declares, the store is a temp directory so no test writes the
// operator's own audit lines.
func roomLinksDoc(from, to string) string {
	return `{"ok":true,"data":[{"from":"` + from + `","to":"` + to + `","kind":"collaborates_with"}]}` + "\n"
}

func auditStoreUnder(t *testing.T) {
	t.Helper()
	previous := sendStore
	store := t.TempDir()
	sendStore = func() (string, error) { return store, nil }
	t.Cleanup(func() { sendStore = previous })
}

// fakeRuntime answers the runtime socket the way the daemon does. It accepts connections for the life of the
// test, IGNORES the probe dial ResolveTransport makes (that connection is dialled and closed with no line),
// and answers each request LINE with handler(line). It returns the socket path and a channel of the lines it
// saw, so a test can pin the frame the verb wrote as well as its stdout.
func fakeRuntime(t *testing.T, handler func(line string) string) (string, <-chan string) {
	t.Helper()

	dir, err := os.MkdirTemp("", "sendshim")
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

	requests := make(chan string, 8)
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
				requests <- string(line)
				_, _ = conn.Write([]byte(handler(string(line))))
			}(conn)
		}
	}()
	return sock, requests
}

// pinGoDevShell STATES THE IDENTITY THIS FILE'S FAKES LICENSE, so the shell that RUNS the tests cannot decide
// their outcome. Measured 2026-10-15: this command is green in go-dev's shell and red in the reviewer's with
// `reviewer may not send a notice to dev (no link)`, because AGENTHUB_ROOM, AGENTHUB_SEAT and
// AGENTHUB_SESSION_NAME are inherited from the seat that runs it and the resolver reads them (clientruntime's
// seat environment IS the identity when the runtime has not named one). A send test that leaves the sender's
// seat to the ambient environment tests the machine it ran on, not the guard.
func pinGoDevShell(t *testing.T) {
	t.Helper()
	t.Setenv("AGENTHUB_ROOM", "4genthub-min")
	t.Setenv("AGENTHUB_SEAT", "go-dev")
	t.Setenv("AGENTHUB_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")
}

// TestSendGoesThroughTheRuntimeSocket: the runtime carries the message in a From/To/Sent envelope.
func TestSendGoesThroughTheRuntimeSocket(t *testing.T) {
	pinGoDevShell(t)
	auditStoreUnder(t)

	sock, requests := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"GetRoomLinks"`) {
			return roomLinksDoc("go-dev", "fe-dev")
		}
		if strings.Contains(line, `"Whoami"`) {
			// THE SENDER'S OWN IDENTITY IS ASKED OF THE RUNTIME FIRST, so a fake daemon has to answer it the
			// way a daemon does: this shell is go-dev of 4genthub-min.
			return `{"ok":true,"data":{"identity":{"rigName":"4genthub-min","memberId":"go-dev",` +
				`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
		}
		if strings.Contains(line, `"GetRoomStatus"`) {
			// The canonical name the caller typed is resolved through the seat's own record before the send -
			// the runtime is the transport that cannot address the canonical form (F-B). This record names the
			// same string the caller did, so nothing is rewritten and the delivery below is still measured on
			// what the caller chose.
			return `{"ok":true,"data":[{"room":"4genthub-min","seat":"fe-dev",` +
				`"session_name":"4genthub-min-fe-dev@4genthub-min","runtime":"omp","state":"Running"}]}` + "\n"
		}
		return `{"ok":true,"data":{"delivered":"4genthub-min-fe-dev@4genthub-min","outcome":"submitted"}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"4genthub-min-fe-dev@4genthub-min", "probe", "--intent", "notice"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("send exit = %d, stderr = %q", code, errOut.String())
	}
	if out.String() != "Sent to 4genthub-min-fe-dev@4genthub-min\n" {
		t.Errorf("stdout = %q, want the delivery confirmed by session name", out.String())
	}

	// TWO requests reach the runtime, in this order: the room's own record, asked for the seat's session name,
	// and the send itself.
	var send struct {
		Action  string            `json:"action"`
		Payload map[string]string `json:"payload"`
	}
	var seen []string
	timeout := time.After(5 * time.Second)
	for send.Action != "SendText" {
		select {
		case line := <-requests:
			var frame struct {
				Action  string            `json:"action"`
				Payload map[string]string `json:"payload"`
			}
			if err := json.Unmarshal([]byte(line), &frame); err != nil {
				t.Fatalf("the frame the verb wrote is not JSON: %v (%q)", err, line)
			}
			seen = append(seen, frame.Action)
			if frame.Action == "SendText" {
				send.Action, send.Payload = frame.Action, frame.Payload
			}
		case <-timeout:
			t.Fatalf("no SendText reached the runtime socket; actions seen: %v", seen)
		}
	}
	if len(seen) == 0 || seen[0] != "GetRoomStatus" {
		t.Errorf("the runtime was asked for %v, want the room's record first and then the send", seen)
	}
	if send.Payload["session"] != "4genthub-min-fe-dev@4genthub-min" {
		t.Errorf("session = %q, want the one the caller named", send.Payload["session"])
	}
	// THE ORIGIN TRAVELS WITH THE TEXT: the seat's runner is where a send can end up held, and at its bound it
	// hands the text BACK to this session rather than dropping it - which is only possible if the request says
	// who is sending and which verb carried it (hub task c94cecfc: three sends to the lead seat lost in one
	// evening, with the sender told nothing).
	if send.Payload["from"] != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("from = %q, want this shell's own session", send.Payload["from"])
	}
	if send.Payload["verb"] != "send" {
		t.Errorf("verb = %q, want the verb that carried the text", send.Payload["verb"])
	}
	text := send.Payload["text"]
	if !strings.HasPrefix(text, "From: 4genthub-min-go-dev@4genthub-min\nTo: 4genthub-min-fe-dev@4genthub-min\n") ||
		!strings.Contains(text, "probe") {
		t.Errorf("delivered text = %q, want the envelope addressed to the target", text)
	}
}

// TestSendSocketAimsTheVerbAtThatDaemon: `--socket PATH` is the only way to reach a daemon that does not own
// this machine's own socket, and it has to aim BOTH halves of the verb - the record the seat's name is read
// from, and the send itself - or the name comes from one runtime while the message goes to another. Measured
// 2026-10-14: a seat of an isolated run resolved this box's live socket, and its send was refused for a mailbox
// under the LIVE runtime's state directory.
//
// The other daemon (the environment's own) answers every well-formed request here, so a leak to it is not a
// connection error a test could read past: it lands on its channel, and the check is that its channel is empty.
func TestSendSocketAimsTheVerbAtThatDaemon(t *testing.T) {
	pinGoDevShell(t)

	other, otherSeen := fakeRuntime(t, func(string) string {
		return `{"ok":true,"data":[]}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, other)

	auditStoreUnder(t)
	aimed, aimedSeen := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"GetRoomLinks"`) {
			return roomLinksDoc("go-dev", "dev")
		}
		if strings.Contains(line, `"Whoami"`) {
			// THE SENDER'S OWN IDENTITY IS ASKED OF THE RUNTIME FIRST, so a fake daemon has to answer it the
			// way a daemon does: this shell is go-dev of 4genthub-min.
			return `{"ok":true,"data":{"identity":{"rigName":"4genthub-min","memberId":"go-dev",` +
				`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
		}
		if strings.Contains(line, `"GetRoomStatus"`) {
			// THIS daemon's record, and its own session name for the seat: the name the verb sends with has to
			// be the one the daemon it writes to uses.
			return `{"ok":true,"data":[{"room":"isolated","seat":"dev",` +
				`"session_name":"agenthub--isolated--dev","runtime":"omp","state":"Running"}]}` + "\n"
		}
		return `{"ok":true,"data":{"delivered":"agenthub--isolated--dev","outcome":"submitted"}}` + "\n"
	})

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(),
		[]string{"isolated-dev@isolated", "probe", "--intent", "notice", "--socket", aimed}, &out, &errOut)
	if code != 0 {
		t.Fatalf("send exit = %d, stderr = %q", code, errOut.String())
	}
	if out.String() != "Sent to agenthub--isolated--dev\n" {
		t.Errorf("stdout = %q, want the delivery confirmed by the name the AIMED daemon's record carries", out.String())
	}

	// FOUR questions, in this order: the seat's record (the name to write to), the room's declared links (the
	// decision's source), and the send itself. The identity question is NOT one of them - see below.
	var actions []string
	timeout := time.After(5 * time.Second)
	for len(actions) < 3 {
		select {
		case line := <-aimedSeen:
			var frame struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal([]byte(line), &frame); err != nil {
				t.Fatalf("the frame the verb wrote is not JSON: %v (%q)", err, line)
			}
			actions = append(actions, frame.Action)
		case <-timeout:
			t.Fatalf("the aimed socket saw %v, want the record, the links and then the send", actions)
		}
	}
	want := "GetRoomStatus,GetRoomLinks,SendText"
	if strings.Join(actions, ",") != want {
		t.Errorf("the aimed daemon was asked for %v, want %s: the decision and the delivery travel the SAME socket", actions, want)
	}
	// THE ENVIRONMENT'S DAEMON IS ASKED ONE QUESTION, AND ONLY ONE: this shell's own identity. Which seat is
	// sending is a fact about this shell rather than about the daemon the message is aimed at, so the resolver
	// asks this shell's own transport - and nothing of the decision or the delivery may leak there.
	drained := false
	for !drained {
		select {
		case line := <-otherSeen:
			if !strings.Contains(line, `"Whoami"`) {
				t.Errorf("the environment's daemon was asked %q: only this shell's identity may go there", line)
			}
		default:
			drained = true
		}
	}
}

// TestSendJsonPrintsTheRuntimesOwnAnswer: --json is the transport's own document. The runtime path answers
// with the daemon's SendText document rather than a re-rendering of it.
func TestSendJsonPrintsTheRuntimesOwnAnswer(t *testing.T) {
	pinGoDevShell(t)

	auditStoreUnder(t)
	sock, _ := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"GetRoomLinks"`) {
			return roomLinksDoc("go-dev", "peer")
		}
		if strings.Contains(line, `"Whoami"`) {
			return `{"ok":true,"data":{"identity":{"rigName":"4genthub-min","memberId":"go-dev"}}}` + "\n"
		}
		if strings.Contains(line, `"ListSessions"`) {
			return `{"ok":true,"data":[{"rigName":"r","logicalId":"r.peer","sessionName":"peer@r"}]}` + "\n"
		}
		return `{"ok":true,"data":{"delivered":"peer@r","outcome":"submitted"}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"peer@r", "probe", "--json", "--intent", "notice"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("send --json exit = %d, stderr = %q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != `{"delivered":"peer@r","outcome":"submitted"}` {
		t.Errorf("stdout = %q, want the runtime's own answer document", out.String())
	}
}

// TestSendNamesTheRuntimeWhenNoTransportAnswers: with no runtime answering, the refusal names
// the RUNTIME that is missing — the same rule the queue verb follows — and never the removed sentence about
// the rig daemon.
func TestSendNamesTheRuntimeWhenNoTransportAnswers(t *testing.T) {
	pinGoDevShell(t)
	noRuntimeSocket(t)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"peer@r", "probe", "--intent", "notice"}, &out, &errOut)

	if code == 0 {
		t.Fatalf("send exited 0 with no transport; stdout = %q", out.String())
	}
	if out.Len() != 0 {
		t.Errorf("a send that never happened reported %q", out.String())
	}
	// The first thing a send now needs is the RECIPIENT's seat key, read back from the daemon the delivery is
	// aimed at: with no daemon answering, that read is what fails, and the refusal names the runtime and the
	// socket it could not read rather than the sentence about a rig daemon.
	for _, want := range []string{"the runtime at ", "no-runtime.sock", "nothing was sent"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", errOut.String(), want)
		}
	}
}

// TestSendRigSeatShapeRawSendsTheTextExactly: --raw on the RIG SEAT shape drops the shape's own
// "From NAME:" attribution and the envelope, so the runtime receives the text exactly.
func TestSendRigSeatShapeRawSendsTheTextExactly(t *testing.T) {
	auditStoreUnder(t)
	sock, requests := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"GetRoomLinks"`) {
			return roomLinksDoc("go-dev", "dev")
		}
		if strings.Contains(line, `"Whoami"`) {
			return `{"ok":true,"data":{"identity":{"rigName":"4genthub-min","memberId":"go-dev",` +
				`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
		}
		if strings.Contains(line, `"GetRoomStatus"`) {
			return `{"ok":true,"data":[{"room":"lab","seat":"dev","session_name":"agenthub--lab--dev",` +
				`"runtime":"omp","state":"Running"}]}` + "\n"
		}
		return `{"ok":true,"data":{}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"lab", "dev", "gate text", "--raw", "--intent", "notice"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("send RIG SEAT --raw exit = %d, stderr = %q", code, errOut.String())
	}
	var sent string
	for len(requests) > 0 {
		if line := <-requests; strings.Contains(line, `"SendText"`) {
			sent = line
		}
	}
	var frame struct {
		Payload map[string]string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(sent), &frame); err != nil {
		t.Fatalf("no SendText frame reached the runtime: %q (%v)", sent, err)
	}
	if frame.Payload["session"] != "agenthub--lab--dev" || frame.Payload["text"] != "gate text" {
		t.Errorf("payload = %v, want the seat's own session and the text exactly", frame.Payload)
	}
}

// TestSendTranslatesTheCanonicalSessionNameAndPassesTheNativeOneThrough PINS F-B'S OWN TWO COMMANDS.
//
// Measured by the architect on the native surface at 08839f0, in a natively started room: the canonical form
// the help documents, `send leg-solo-solo@leg-solo "..."`, was REFUSED by the runtime - "tmux paste-buffer
// failed: can't find pane: leg-solo-solo@leg-solo" - while the runtime's own string, `send
// agenthub--leg-solo--solo "..."`, delivered with a real envelope. The address every seat's own name uses was
// dead, and the working one was tribal knowledge.
//
// Both commands run here. The canonical name resolves through the seat's own record - the room the NAME names,
// not this shell's ambient room - and travels on the transport that record names; the native name is not the
// canonical form, so it is passed through untouched and no record is consulted.
func TestSendTranslatesTheCanonicalSessionNameAndPassesTheNativeOneThrough(t *testing.T) {
	const native = "agenthub--leg-solo--solo"

	run := func(t *testing.T, target string) (int, string, string, []string) {
		t.Helper()
		pinGoDevShell(t)

		var mu sync.Mutex
		var lines []string
		auditStoreUnder(t)
		sock, _ := fakeRuntime(t, func(line string) string {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
			if strings.Contains(line, `"GetRoomLinks"`) {
				return roomLinksDoc("go-dev", "solo")
			}
			if strings.Contains(line, `"Whoami"`) {
				// THE SENDER'S OWN IDENTITY IS ASKED OF THE RUNTIME FIRST, so a fake daemon has to answer it the
				// way a daemon does: this shell is go-dev of 4genthub-min.
				return `{"ok":true,"data":{"identity":{"rigName":"4genthub-min","memberId":"go-dev",` +
					`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
			}
			if strings.Contains(line, `"ListSessions"`) {
				// The native name is read back to its seat key here: the room's links name seat keys, so the
				// daemon the delivery goes to is asked which seat that session is.
				return `{"ok":true,"data":[{"rigName":"leg-solo","logicalId":"leg-solo.solo","sessionName":"` +
					native + `","canonicalSessionName":"leg-solo-solo@leg-solo"}]}` + "\n"
			}
			if strings.Contains(line, `"GetRoomStatus"`) {
				return `{"ok":true,"data":[{"room":"leg-solo","seat":"solo","session_name":"` + native +
					`","runtime":"omp","state":"Running"}]}` + "\n"
			}
			return `{"ok":true,"data":{"delivered":"` + native + `","outcome":"submitted"}}` + "\n"
		})
		t.Setenv(clientruntime.RuntimeSocketEnv, sock)

		var out, errOut bytes.Buffer
		code := sendCommand{}.Run(context.Background(), []string{target, "LEG CHECK ONE", "--intent", "notice"}, &out, &errOut)
		mu.Lock()
		defer mu.Unlock()
		return code, out.String(), errOut.String(), append([]string(nil), lines...)
	}

	t.Run("the canonical name is translated", func(t *testing.T) {
		code, out, errOut, lines := run(t, "leg-solo-solo@leg-solo")
		if code != 0 {
			t.Fatalf("send leg-solo-solo@leg-solo exit = %d, stderr = %q", code, errOut)
		}
		if got := countContaining(lines, `"GetRoomStatus"`); got != 1 {
			t.Errorf("GetRoomStatus asked %d times, want 1: the record is where the name comes from", got)
		}
		send := firstContaining(lines, `"SendText"`)
		if send == "" {
			t.Fatalf("no SendText reached the runtime; requests were %q", lines)
		}
		if !strings.Contains(send, native) {
			t.Errorf("SendText = %s, want the record's own name %s", send, native)
		}
		if strings.Contains(send, "leg-solo-solo@leg-solo") {
			t.Errorf("the canonical string was handed to the runtime as-is: %s", send)
		}
		if !strings.Contains(out, "Sent to "+native) {
			t.Errorf("stdout = %q, want the name that was actually written to", out)
		}
	})

	t.Run("the native name is passed through", func(t *testing.T) {
		code, _, errOut, lines := run(t, native)
		if code != 0 {
			t.Fatalf("send %s exit = %d, stderr = %q", native, code, errOut)
		}
		if got := countContaining(lines, `"GetRoomStatus"`); got != 0 {
			t.Errorf("GetRoomStatus asked %d times, want 0: the native name is not the canonical form", got)
		}
		if send := firstContaining(lines, `"SendText"`); !strings.Contains(send, native) {
			t.Errorf("SendText = %s, want the native name untouched", send)
		}
	})
}

func countContaining(lines []string, want string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, want) {
			n++
		}
	}
	return n
}

func firstContaining(lines []string, want string) string {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return line
		}
	}
	return ""
}
