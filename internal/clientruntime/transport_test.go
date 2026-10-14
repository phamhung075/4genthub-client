package clientruntime

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
)

// THE RUNTIME-FIRST SEAM, PINNED WHERE IT CAN BE FALSIFIED.
//
// These tests are the U3 acceptance's instrument. The verb they exercise used to resolve its transport from
// the SEAT ENVIRONMENT first: with OPENRIG_SESSION_NAME set and OPENRIG_URL naming a daemon, `whoami` and
// `queue` went straight to HTTP and answered "the OpenRig daemon at http://127.0.0.1:1 is not answering" on a
// machine whose runtime was up and perfectly able to answer. The fix is the preference order, and the
// preference order is what is asserted below — in all three combinations:
//
//	runtime present (with a daemon also up)  -> the RUNTIME answers, and the daemon is NOT asked;
//	runtime absent, daemon present           -> the daemon answers, exactly as it did before;
//	neither                                  -> the refusal names the RUNTIME.
//
// They fail on the pre-change code for the plainest reason: the fake runtime socket below is never dialled.
//
// hermeticSeat empties the AGENTHUB_* / OPENRIG_* variables a test does not explicitly want, so a test run
// from inside a seat shell (which is how this repo is worked on) cannot be answered by the shell's own
// environment instead of by the thing under test.
func hermeticSeat(t *testing.T) {
	t.Helper()
	for _, name := range []string{agentHubRoomEnv, agentHubSeatEnv, agentHubRoleEnv, agentHubRuntimeEnv, agentHubSessionEnv, openRigSessionEnv, legacySessionEnv, openRigURLEnv, openRigPortEnv, openRigHomeEnv} {
		t.Setenv(name, "")
	}
}

// fakeRuntime answers the runtime socket the way the daemon does. It accepts connections for the life of the
// test, IGNORES the probe dial ResolveTransport makes (that connection is dialled and closed with no line),
// and answers each request LINE with handler(line).
//
// It returns the socket path and a channel carrying every request line it saw, so a test can pin the frame
// the verb wrote as well as its stdout.
func fakeRuntime(t *testing.T, handler func(line string) string) (string, <-chan string) {
	t.Helper()

	dir, err := os.MkdirTemp("", "runtimefirst")
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
					// The reachability probe: dialled to ask whether anyone is there, closed with no line.
					return
				}
				requests <- string(line)
				_, _ = conn.Write([]byte(handler(string(line))))
			}(conn)
		}
	}()
	return sock, requests
}

// noRuntimeSocket points the runtime transport at a path nothing can be listening on, which is how a test
// says "there is no runtime here" without depending on what the machine happens to be running.
func noRuntimeSocket(t *testing.T) {
	t.Helper()
	t.Setenv(RuntimeSocketEnv, filepath.Join(t.TempDir(), "no-runtime.sock"))
}

// TestListSessionsAsksForTheRoomOrForEveryRoomItHosts: an empty room must travel as an EXPLICIT null (the
// frame is adjacently tagged, so the key is a sibling of `action`) rather than being dropped, which would
// ask a different question than the runtime's own "every room this daemon hosts".
func TestListSessionsAsksForTheRoomOrForEveryRoomItHosts(t *testing.T) {
	sock, requests := fakeRuntime(t, func(string) string {
		return okDataResponse(`[{"canonicalSessionName":"room-a-seat-1@room-a","sessionName":"agenthub--room-a--seat-1",` +
			`"rigName":"room-a","logicalId":"room-a.seat-1","sessionStatus":"running",` +
			`"agentActivity":{"state":"idle","source":"idle"}}]`)
	})
	t.Setenv(RuntimeSocketEnv, sock)

	client := NewClient(sock)
	entries, err := client.ListSessions("")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v, want the one the runtime served", entries)
	}
	if entries[0].CanonicalSessionName != "room-a-seat-1@room-a" || entries[0].AgentActivity.State != "idle" {
		t.Errorf("entry = %#v, want the canonical name and the nested word", entries[0])
	}
	if got := awaitRequest(t, requests); got != `{"action":"ListSessions","payload":{"room":null}}` {
		t.Errorf("frame = %q, want the null room of every-room-it-hosts", got)
	}

	if _, err := client.ListSessions("room-a"); err != nil {
		t.Fatalf("ListSessions(room-a): %v", err)
	}
	if got := awaitRequest(t, requests); got != `{"action":"ListSessions","payload":{"room":"room-a"}}` {
		t.Errorf("frame = %q, want the named room", got)
	}
}

// TestContentReadsAskForTheSeatAndTheWindowTheCallerNamed: `lines` travels only when a window was asked for.
// An absent bound is the runtime's own "read the file whole", so sending a zero would be this client
// inventing a bound nobody chose - and a window is a LINE count, which is why it is not a byte estimate.
func TestContentReadsAskForTheSeatAndTheWindowTheCallerNamed(t *testing.T) {
	sock, requests := fakeRuntime(t, func(string) string {
		return okDataResponse(`{"session":"room-a-seat-1@room-a","path":"/tmp/room-a/logs/seat-1.jsonl","content":"one\ntwo\n"}`)
	})
	t.Setenv(RuntimeSocketEnv, sock)

	client := NewClient(sock)
	answer, err := client.Transcript("room-a-seat-1@room-a", 0)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if answer.Content != "one\ntwo\n" || answer.Path == "" || answer.Session == "" {
		t.Fatalf("answer = %#v, want the runtime's whole document", answer)
	}
	if got := awaitRequest(t, requests); got != `{"action":"Transcript","payload":{"session":"room-a-seat-1@room-a"}}` {
		t.Errorf("frame = %q, want no lines key when no window was asked for", got)
	}

	if _, err := client.Transcript("room-a-seat-1@room-a", 300); err != nil {
		t.Fatalf("Transcript with a window: %v", err)
	}
	if got := awaitRequest(t, requests); got != `{"action":"Transcript","payload":{"lines":300,"session":"room-a-seat-1@room-a"}}` {
		t.Errorf("frame = %q, want the window the caller named", got)
	}
}

// TestAContentReadRefusesAnAnswerThatNamesNoSession: the document PARSED and does not say which seat it is
// about. Reading that as empty content is the lie these actions exist to remove - "there was nothing to
// send" and "nobody told me what I asked about" are different facts, and only one of them is a measurement.
func TestAContentReadRefusesAnAnswerThatNamesNoSession(t *testing.T) {
	for _, verb := range []string{"Transcript", "Capture"} {
		t.Run(verb, func(t *testing.T) {
			sock, requests := fakeRuntime(t, func(string) string {
				return okDataResponse(`{"path":"/tmp/room-a/logs/seat-1.jsonl","content":"something"}`)
			})
			t.Setenv(RuntimeSocketEnv, sock)

			client := NewClient(sock)
			var err error
			if verb == "Transcript" {
				_, err = client.Transcript("room-a-seat-1@room-a", 0)
			} else {
				_, err = client.Capture("room-a-seat-1@room-a", 0)
			}
			_ = awaitRequest(t, requests)

			if err == nil {
				t.Fatalf("a %s answer naming no session was accepted", verb)
			}
			if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitRoomRefused {
				t.Errorf("exit code = %d, want the room contract's refusal", code)
			}
			if !strings.Contains(err.Error(), "named no session") {
				t.Errorf("refusal %q does not say what was missing", err)
			}
		})
	}
}

// awaitRequest is assertQueueFrame's sibling for the fake runtime's own channel.
func awaitRequest(t *testing.T, requests <-chan string) string {
	t.Helper()
	select {
	case line := <-requests:
		return strings.TrimSuffix(line, "\n")
	case <-time.After(5 * time.Second):
		t.Fatal("the verb never wrote a request line to the runtime socket")
		return ""
	}
}

// TestQueueListPrefersTheRuntimeEvenWhenADaemonIsUp is the primary preference: a runtime that answers wins,
// and the OpenRig daemon is NOT asked at all — its handler fails the test if a single request reaches it.
//
// The frame is pinned as the exact line, because the room and the destination are the two things a reader
// cannot check from the outside: the room comes from the seat's environment and the destination from this
// seat's native session name.
func TestQueueListPrefersTheRuntimeEvenWhenADaemonIsUp(t *testing.T) {
	hermeticSeat(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")
	t.Setenv(agentHubSessionEnv, "agenthub--room-a--go-dev")
	t.Setenv(openRigHomeEnv, t.TempDir())

	daemonCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonCalls++
		t.Errorf("the OpenRig daemon was asked while a runtime answered: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv(openRigURLEnv, srv.URL)

	sock, requests := fakeRuntime(t, func(string) string {
		return okDataResponse(`[` + queueTestRowJSON + `]`)
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("queue exit = %d, stderr = %q", code, errOut.String())
	}
	if daemonCalls != 0 {
		t.Fatalf("%d requests reached the OpenRig daemon, want none", daemonCalls)
	}
	want := `{"action":"QueueList","payload":{"filter":{"destination_session":"agenthub--room-a--go-dev","active_only":true},"room":"room-a"}}`
	if got := awaitRequest(t, requests); got != want {
		t.Errorf("runtime frame:\n got %q\nwant %q", got, want)
	}
	for _, want := range []string{"q-item-7", "in-progress", "sess-src", "wire the rail"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("queue printed %q, want it to carry %q", out.String(), want)
		}
	}
}

// TestQueueListNamesTheRuntimeWhenNothingAnswersEither is the acceptance's queue instrument, in the
// combination the acceptance cannot guarantee (no runtime on this machine): the refusal names the RUNTIME —
// its socket, and the knob that points elsewhere — and never the removed sentence about the OpenRig daemon.
func TestQueueListNamesTheRuntimeWhenNothingAnswersEither(t *testing.T) {
	hermeticSeat(t)
	noRuntimeSocket(t)
	t.Setenv(openRigSessionEnv, "4genthub-min-go-dev@4genthub-min")
	// A daemon this shell names but nothing is listening on: the shim's own measured failure mode.
	t.Setenv(openRigURLEnv, "http://127.0.0.1:1")

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code == 0 {
		t.Fatalf("queue exited 0 with no transport; stdout = %q", out.String())
	}
	if out.Len() != 0 {
		t.Errorf("a refused read printed %q on stdout, want nothing", out.String())
	}
	for _, want := range []string{"no runtime is answering at", RuntimeSocketEnv, "nothing was measured"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", errOut.String(), want)
		}
	}
	if strings.Contains(errOut.String(), "is not answering") {
		t.Errorf("the runtime-first refusal still blames the OpenRig daemon: %q", errOut.String())
	}
	if strings.Contains(out.String(), "Measured 0") {
		t.Errorf("a transportless queue claimed a measurement: %q", out.String())
	}
}

// TestQueueListReportsTheRuntimesOwnRefusalBySocket: a runtime that ANSWERS with a refusal has answered, and
// the answer names the transport it came from — never a fabricated empty list, and never the daemon whose
// queue was not even asked.
func TestQueueListReportsTheRuntimesOwnRefusalBySocket(t *testing.T) {
	hermeticSeat(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")

	daemonCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonCalls++
	}))
	defer srv.Close()
	t.Setenv(openRigURLEnv, srv.URL)

	sock, _ := fakeRuntime(t, func(string) string {
		return `{"ok":false,"error":"no such room 'room-a': /state/rooms/room-a is not a room directory"}` + "\n"
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code != 1 {
		t.Fatalf("exit = %d, want the refusal code 1; stderr = %q", code, errOut.String())
	}
	if daemonCalls != 0 {
		t.Errorf("%d requests reached the OpenRig daemon, want none", daemonCalls)
	}
	if out.Len() != 0 {
		t.Errorf("a refused read printed %q on stdout", out.String())
	}
	if !strings.Contains(errOut.String(), sock) || !strings.Contains(errOut.String(), "no such room 'room-a'") {
		t.Errorf("stderr = %q, want the runtime's socket and its own reason", errOut.String())
	}
}

// TestQueueListNamesAnOlderRuntimeThatCannotFrameTheRequest: this is the measured defect, driven end to end. The
// installed runtime does not know QueueList and says so in serde's own words; shown as the verb's answer that
// reads as a fact about the room, and it is not one - nothing about the room was asked or answered. The client
// must NAME the version boundary and leave with ExitUnavailable, with nothing on the (empty) stdout.
func TestQueueListNamesAnOlderRuntimeThatCannotFrameTheRequest(t *testing.T) {
	hermeticSeat(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")

	sock, _ := fakeRuntime(t, func(string) string {
		return "{\"ok\":false,\"error\":\"invalid request JSON: unknown variant `QueueList`, expected one of " +
			"`StartRoom`, `StopRoom`\"}\n"
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code != clientcmd.ExitUnavailable {
		t.Fatalf("exit = %d, want ExitUnavailable (%d); stderr = %q", code, clientcmd.ExitUnavailable, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("an unimplemented verb printed %q on stdout", out.String())
	}
	for _, want := range []string{"QueueList", sock, "different builds", "older than this client"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr does not name %q:\n%s", want, errOut.String())
		}
	}
}

// TestQueueListFallsBackToTheDaemonWhenNoRuntimeAnswers is the regression guard for the fallback: with no
// runtime on this machine, the daemon path must behave exactly as it did before this change.
func TestQueueListFallsBackToTheDaemonWhenNoRuntimeAnswers(t *testing.T) {
	hermeticSeat(t)
	noRuntimeSocket(t)
	t.Setenv(openRigSessionEnv, "4genthub-min-go-dev@4genthub-min")

	var gotPath, gotDest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotDest = r.URL.Path, r.URL.Query().Get("destinationSession")
		_, _ = w.Write([]byte(`[{"qitemId":"qitem-aaa","state":"pending","sourceSession":"s@r","summary":"first owed task"}]`))
	}))
	defer srv.Close()
	t.Setenv(openRigURLEnv, srv.URL)

	var out, errOut bytes.Buffer
	code := queueCommand{}.Run(context.Background(), nil, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("queue exit = %d, stderr = %q", code, errOut.String())
	}
	if gotPath != "/api/queue/list" || gotDest != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("asked %q for %q, want the daemon path unchanged", gotPath, gotDest)
	}
	if !strings.Contains(out.String(), "qitem-aaa") {
		t.Errorf("stdout = %q, want the daemon's row", out.String())
	}
}

// TestWhoamiJsonAnswersFromTheSeatEnvironmentWithNoDaemonAtAll IS the U3 acceptance, not an incidental case:
// with $OPENRIG_URL at a dead port and no runtime to ask, `whoami --json` must still exit 0 and print the
// seat identity — the identity this shell's environment carries. Before the change this verb exited 3 with
// the OpenRig daemon's connection error, because a set OPENRIG_SESSION_NAME sent it straight to HTTP.
func TestWhoamiJsonAnswersFromTheSeatEnvironmentWithNoDaemonAtAll(t *testing.T) {
	hermeticSeat(t)
	noRuntimeSocket(t)
	t.Setenv(openRigSessionEnv, "4genthub-min-go-dev@4genthub-min")
	t.Setenv(openRigRuntimeEnv, "omp")
	t.Setenv(openRigURLEnv, "http://127.0.0.1:1")

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("whoami --json exit = %d with no daemon at all, stderr = %q", code, errOut.String())
	}
	var printed struct {
		ResolvedBy string `json:"resolved_by"`
		Identity   struct {
			RigName     string `json:"rigName"`
			MemberID    string `json:"memberId"`
			Runtime     string `json:"runtime"`
			SessionName string `json:"sessionName"`
			LogicalID   string `json:"logical_id"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not an identity document: %v (stdout=%q)", err, out.String())
	}
	if printed.ResolvedBy != "environment" {
		t.Errorf("resolved_by = %q, want the seat's own environment to say so", printed.ResolvedBy)
	}
	if printed.Identity.RigName != "4genthub-min" || printed.Identity.MemberID != "4genthub-min-go-dev" {
		t.Errorf("identity = %+v, want the seat the session name names", printed.Identity)
	}
	if printed.Identity.SessionName != "4genthub-min-go-dev@4genthub-min" {
		t.Errorf("sessionName = %q, want the seat's own OPENRIG_SESSION_NAME", printed.Identity.SessionName)
	}
	if printed.Identity.Runtime != "omp" {
		t.Errorf("runtime = %q, want the seat's OPENRIG_RUNTIME", printed.Identity.Runtime)
	}
}

// TestWhoamiPrefersTheRuntimesIdentityOverTheDaemon: an identity the runtime resolves wins, and the daemon is
// not asked for it. The document is the runtime's own — the identity below exists nowhere in the environment.
func TestWhoamiPrefersTheRuntimesIdentityOverTheDaemon(t *testing.T) {
	hermeticSeat(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")
	t.Setenv(agentHubSessionEnv, "agenthub--room-a--go-dev")
	// The OpenRig daemon is named TOO, so the pre-change verb — which resolved its transport from the seat
	// environment — had somewhere to go and the handler below would have been called.
	t.Setenv(openRigSessionEnv, "4genthub-min-go-dev@4genthub-min")
	t.Setenv(openRigHomeEnv, t.TempDir())

	daemonCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonCalls++
		t.Errorf("the OpenRig daemon was asked for an identity the runtime answered: %s", r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv(openRigURLEnv, srv.URL)

	const identityDoc = `{"resolved_by":"session_name","identity":{"rigName":"room-a","memberId":"go-dev","runtime":"omp","sessionName":"agenthub--room-a--go-dev","logical_id":"go-dev","role":"developer"},"peers":[],"edges":{"outgoing":[],"incoming":[]},"transcript":{"enabled":true}}`
	sock, requests := fakeRuntime(t, func(string) string {
		return `{"ok":true,"data":` + identityDoc + "}\n"
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	if daemonCalls != 0 {
		t.Errorf("%d requests reached the OpenRig daemon, want none", daemonCalls)
	}
	if got := awaitRequest(t, requests); got != `{"action":"Whoami","payload":{"session":"agenthub--room-a--go-dev"}}` {
		t.Errorf("runtime frame:\n got %q\nwant the Whoami frame for this seat", got)
	}
	var printed struct {
		ResolvedBy string `json:"resolved_by"`
		Identity   struct {
			RigName string `json:"rigName"`
			Role    string `json:"role"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not the runtime's document: %v (stdout=%q)", err, out.String())
	}
	if printed.ResolvedBy != "session_name" || printed.Identity.Role != "developer" {
		t.Errorf("printed %+v, want the runtime's own answer", printed)
	}
}

// TestWhoamiFallsThroughARefusedRuntimeToTheDaemon: a runtime that ANSWERS with a refusal has not resolved
// this seat, so the daemon — when this shell names one — still gets its turn. Reporting the runtime's refusal
// here would make an identity question fail on the machine that could have answered it.
func TestWhoamiFallsThroughARefusedRuntimeToTheDaemon(t *testing.T) {
	hermeticSeat(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")
	t.Setenv(openRigSessionEnv, "4genthub-min-go-dev@4genthub-min")
	t.Setenv(openRigHomeEnv, t.TempDir())

	daemonCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonCalls++
		_, _ = w.Write([]byte(whoamiDoc))
	}))
	defer srv.Close()
	t.Setenv(openRigURLEnv, srv.URL)

	sock, _ := fakeRuntime(t, func(string) string {
		return `{"ok":false,"error":"seat identity could not be resolved from active rooms"}` + "\n"
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), nil, []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	if daemonCalls != 1 {
		t.Fatalf("%d requests reached the OpenRig daemon, want exactly one", daemonCalls)
	}
	if !strings.Contains(out.String(), `"4genthub-min.go-dev"`) {
		t.Errorf("stdout = %q, want the daemon's own document", out.String())
	}
}

// TestResolveTransportPrefersTheRuntimeThenTheDaemon states the preference order itself, in the three
// combinations, so a future edit to the seam fails here rather than in a room.
func TestResolveTransportPrefersTheRuntimeThenTheDaemon(t *testing.T) {
	t.Run("runtime first, daemon up too", func(t *testing.T) {
		hermeticSeat(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		t.Setenv(openRigURLEnv, srv.URL)
		sock, _ := fakeRuntime(t, func(string) string { return `{"ok":true}` + "\n" })
		t.Setenv(RuntimeSocketEnv, sock)
		if got := ResolveTransport(); got != TransportRuntime {
			t.Errorf("ResolveTransport() = %v, want the runtime", got)
		}
	})

	t.Run("no runtime, daemon named", func(t *testing.T) {
		hermeticSeat(t)
		noRuntimeSocket(t)
		t.Setenv(openRigURLEnv, "http://127.0.0.1:1")
		if got := ResolveTransport(); got != TransportDaemon {
			t.Errorf("ResolveTransport() = %v, want the daemon fallback", got)
		}
	})

	t.Run("neither", func(t *testing.T) {
		hermeticSeat(t)
		noRuntimeSocket(t)
		if got := ResolveTransport(); got != TransportNone {
			t.Errorf("ResolveTransport() = %v, want no transport", got)
		}
	})
}
