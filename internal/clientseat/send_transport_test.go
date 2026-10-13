package clientseat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// THE RUNTIME-FIRST SEAM FOR `send`, PINNED WHERE IT CAN BE FALSIFIED.
//
// `rig send <session> <text>` used to resolve its transport from the seat environment: with OPENRIG_URL set it
// went straight to the OpenRig daemon and never touched this client's own runtime. The preference order is
// the change, and it is what these tests assert — a runtime that answers carries the message and the daemon is
// NOT asked, while a machine with no runtime still reaches the daemon exactly as before.
//
// They fail on the pre-change code for the plainest reason: the fake runtime socket below is never dialled.

// noRuntimeSocket points the runtime transport at a path nothing can be listening on, which is how a
// daemon-path test says "there is no runtime here" instead of being answered by the machine's own.
func noRuntimeSocket(t *testing.T) {
	t.Helper()
	t.Setenv(clientruntime.RuntimeSocketEnv, filepath.Join(t.TempDir(), "no-runtime.sock"))
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

// TestSendPrefersTheRuntimeSocket is the preference itself: the runtime carries the message — in the SAME
// From/To/Sent envelope the daemon path sends, so the receiver's text does not depend on the transport — and
// the OpenRig daemon, which IS configured here, is never asked.
func TestSendPrefersTheRuntimeSocket(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	daemonCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonCalls++
		t.Errorf("the OpenRig daemon was asked while a runtime answered: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)

	sock, requests := fakeRuntime(t, func(string) string {
		return `{"ok":true,"data":{"delivered":"4genthub-min-fe-dev@4genthub-min"}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil,
		[]string{"4genthub-min-fe-dev@4genthub-min", "probe"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("send exit = %d, stderr = %q", code, errOut.String())
	}
	if daemonCalls != 0 {
		t.Fatalf("%d requests reached the OpenRig daemon, want none", daemonCalls)
	}
	if out.String() != "Sent to 4genthub-min-fe-dev@4genthub-min\n" {
		t.Errorf("stdout = %q, want the delivery confirmed by session name", out.String())
	}

	var frame struct {
		Action  string            `json:"action"`
		Payload map[string]string `json:"payload"`
	}
	select {
	case line := <-requests:
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("the frame the verb wrote is not JSON: %v (%q)", err, line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send never wrote a request line to the runtime socket")
	}
	if frame.Action != "SendText" {
		t.Errorf("action = %q, want SendText", frame.Action)
	}
	if frame.Payload["session"] != "4genthub-min-fe-dev@4genthub-min" {
		t.Errorf("session = %q, want the one the caller named", frame.Payload["session"])
	}
	text := frame.Payload["text"]
	if !strings.HasPrefix(text, "From: 4genthub-min-go-dev@4genthub-min\nTo: 4genthub-min-fe-dev@4genthub-min\n") ||
		!strings.Contains(text, "probe") {
		t.Errorf("delivered text = %q, want the envelope addressed to the target", text)
	}
}

// TestSendJsonPrintsTheRuntimesOwnAnswer: --json is the transport's own document. The runtime path answers
// with the daemon's SendText document rather than a re-rendering of it.
func TestSendJsonPrintsTheRuntimesOwnAnswer(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the OpenRig daemon was asked while a runtime answered: %s", r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)

	sock, _ := fakeRuntime(t, func(string) string {
		return `{"ok":true,"data":{"delivered":"peer@r"}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "probe", "--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("send --json exit = %d, stderr = %q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != `{"delivered":"peer@r"}` {
		t.Errorf("stdout = %q, want the runtime's own answer document", out.String())
	}
}

// TestSendNamesTheRuntimeWhenNoTransportAnswers: with no runtime and a dead $OPENRIG_URL, the refusal names
// the RUNTIME that is missing — the same rule the queue verb follows — and never the removed sentence about
// the OpenRig daemon.
func TestSendNamesTheRuntimeWhenNoTransportAnswers(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")
	noRuntimeSocket(t)
	t.Setenv("OPENRIG_URL", "http://127.0.0.1:1")

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"peer@r", "probe"}, &out, &errOut)

	if code == 0 {
		t.Fatalf("send exited 0 with no transport; stdout = %q", out.String())
	}
	if out.Len() != 0 {
		t.Errorf("a send that never happened reported %q", out.String())
	}
	for _, want := range []string{"no runtime is answering at", clientruntime.RuntimeSocketEnv, "nothing was sent"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", errOut.String(), want)
		}
	}
	if strings.Contains(errOut.String(), "is not answering") {
		t.Errorf("the runtime-first refusal still blames the OpenRig daemon: %q", errOut.String())
	}
}

// TestSendRigSeatShapeHonoursTheDaemonOnlySwitches PINS THE U3 GATE'S FALSIFICATION.
//
// `send RIG SEAT MESSAGE` used to call SendTextToSeat directly: every switch was parsed and then DROPPED, so
// `--wait-for-idle 5` typed into a busy seat without waiting and `--verify` reported nothing at all. The switch
// now decides the transport, exactly as it does for the SESSION shape, and the message goes to the seat's own
// session name over the daemon. It fails on the pre-change code twice over: the daemon is never asked at all,
// and the runtime IS - which the eval in the fake runtime's handler catches in the same run.
func TestSendRigSeatShapeHonoursTheDaemonOnlySwitches(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var got map[string]any
	daemonCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonCalls++
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"outcome":"delivered","verified":true}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)

	// A runtime IS answering here: that is the case a daemon-only switch has to override.
	sock, requests := fakeRuntime(t, func(line string) string {
		t.Errorf("the runtime was asked while a daemon-only switch named the daemon: %q", line)
		return `{"ok":true,"data":{}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil,
		[]string{"lab", "dev", "gate text", "--wait-for-idle", "5", "--verify"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("send RIG SEAT --wait-for-idle 5 --verify exit = %d, stderr = %q", code, errOut.String())
	}
	if daemonCalls != 1 {
		t.Fatalf("%d requests reached the daemon, want exactly 1: the switches name a handshake only it runs", daemonCalls)
	}
	if got["session"] != "agenthub--lab--dev" {
		t.Errorf("the daemon was asked for session %v, want the seat's own agenthub--lab--dev", got["session"])
	}
	if got["waitForIdleMs"] != float64(5000) {
		t.Errorf("waitForIdleMs = %v, want 5000: --wait-for-idle 5 has to reach the party that waits", got["waitForIdleMs"])
	}
	if got["verify"] != true {
		t.Errorf("verify = %v, want true", got["verify"])
	}
	select {
	case line := <-requests:
		t.Errorf("the runtime socket was written to as well: %q", line)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSendRigSeatShapeRawSendsTheTextExactly: --raw on this shape drops the shape's own "From NAME:"
// attribution, because raw means the text exactly; the daemon carries it as the text.
func TestSendRigSeatShapeRawSendsTheTextExactly(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"outcome":"delivered"}`))
	}))
	defer srv.Close()
	t.Setenv("OPENRIG_URL", srv.URL)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil,
		[]string{"lab", "dev", "gate text", "--raw"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("send RIG SEAT --raw exit = %d, stderr = %q", code, errOut.String())
	}
	if got["text"] != "gate text" {
		t.Errorf("text = %v, want the message exactly, with no From NAME: prefix", got["text"])
	}
}

// TestSendRigSeatShapeNamesTheDaemonWhenASwitchForcedIt is the gate's second finding, pinned where it can be
// falsified: a HEALTHY runtime and no daemon, with a switch that needed the daemon. The refusal must name that
// switch and the daemon - and must NOT say "no runtime is answering", which was false here and aimed the reader
// at a working transport.
func TestSendRigSeatShapeNamesTheDaemonWhenASwitchForcedIt(t *testing.T) {
	t.Setenv("OPENRIG_HOME", t.TempDir())
	t.Setenv("OPENRIG_SESSION_NAME", "4genthub-min-go-dev@4genthub-min")
	t.Setenv("OPENRIG_URL", "http://127.0.0.1:1")
	sock, _ := fakeRuntime(t, func(string) string { return `{"ok":true,"data":{}}` + "\n" })
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), nil, []string{"lab", "dev", "gate text", "--verify"}, &out, &errOut)

	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d, want %d (the transport that could run this handshake is missing); stderr = %q",
			code, clientcmd.ExitUnavailable, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("a send that never happened reported %q", out.String())
	}
	for _, want := range []string{"--verify needs the OpenRig daemon", "nothing was sent"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", errOut.String(), want)
		}
	}
	if strings.Contains(errOut.String(), "no runtime is answering") {
		t.Errorf("the refusal blamed the runtime while the runtime was answering: %q", errOut.String())
	}
}
