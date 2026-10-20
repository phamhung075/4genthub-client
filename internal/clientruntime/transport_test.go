package clientruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// THE RUNTIME TRANSPORT, PINNED WHERE IT CAN BE FALSIFIED.
//
// The runtime socket is the only transport: a runtime that answers is the answer, and with none the refusal
// names the RUNTIME (its socket and the knob that points elsewhere).
//
// hermeticSeat empties the AGENTHUB_* variables a test does not explicitly want, so a test run
// from inside a seat shell (which is how this repo is worked on) cannot be answered by the shell's own
// environment instead of by the thing under test.
func hermeticSeat(t *testing.T) {
	t.Helper()
	for _, name := range []string{agentHubRoomEnv, agentHubSeatEnv, agentHubRoleEnv, agentHubRuntimeEnv, agentHubSessionEnv, agentHubURLEnv, agentHubTokenEnv} {
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
			`"roomName":"room-a","logicalId":"room-a.seat-1","sessionStatus":"running",` +
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

// TestWhoamiJsonAnswersFromTheSeatEnvironmentWithNoRuntime: with no runtime to ask, `whoami --json` still
// exits 0 and prints the identity this shell's environment carries, and says it came from the environment.
func TestWhoamiJsonAnswersFromTheSeatEnvironmentWithNoRuntime(t *testing.T) {
	hermeticSeat(t)
	noRuntimeSocket(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")
	t.Setenv(agentHubRuntimeEnv, "omp")

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("whoami --json exit = %d with no runtime, stderr = %q", code, errOut.String())
	}
	var printed struct {
		ResolvedBy string `json:"resolved_by"`
		Identity   struct {
			RoomName string `json:"roomName"`
			MemberID string `json:"memberId"`
			Runtime  string `json:"runtime"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not an identity document: %v (stdout=%q)", err, out.String())
	}
	if printed.ResolvedBy != "environment" {
		t.Errorf("resolved_by = %q, want the seat's own environment to say so", printed.ResolvedBy)
	}
	if printed.Identity.RoomName != "room-a" || printed.Identity.MemberID != "go-dev" || printed.Identity.Runtime != "omp" {
		t.Errorf("identity = %+v, want the seat the environment names", printed.Identity)
	}
}

// TestWhoamiPrefersTheRuntimesIdentity: an identity the runtime resolves wins over the environment's. The
// document is the runtime's own - its role exists nowhere in the environment.
func TestWhoamiPrefersTheRuntimesIdentity(t *testing.T) {
	hermeticSeat(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")
	t.Setenv(agentHubSessionEnv, "agenthub--room-a--go-dev")

	const identityDoc = `{"resolved_by":"session_name","identity":{"roomName":"room-a","memberId":"go-dev","runtime":"omp","sessionName":"agenthub--room-a--go-dev","logical_id":"go-dev","role":"developer"},"peers":[],"edges":{"outgoing":[],"incoming":[]},"transcript":{"enabled":true}}`
	sock, requests := fakeRuntime(t, func(string) string {
		return `{"ok":true,"data":` + identityDoc + "}\n"
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	if got := awaitRequest(t, requests); got != `{"action":"Whoami","payload":{"session":"agenthub--room-a--go-dev"}}` {
		t.Errorf("runtime frame:\n got %q\nwant the Whoami frame for this seat", got)
	}
	var printed struct {
		ResolvedBy string `json:"resolved_by"`
		Identity   struct {
			Role string `json:"role"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("stdout is not the runtime's document: %v (stdout=%q)", err, out.String())
	}
	if printed.ResolvedBy != "session_name" || printed.Identity.Role != "developer" {
		t.Errorf("printed %+v, want the runtime's own answer", printed)
	}
}

// TestWhoamiFallsThroughARefusedRuntimeToTheEnvironment: a runtime that ANSWERS with a refusal has not
// resolved this seat, so the seat's own environment still gets its turn.
func TestWhoamiFallsThroughARefusedRuntimeToTheEnvironment(t *testing.T) {
	hermeticSeat(t)
	t.Setenv(agentHubRoomEnv, "room-a")
	t.Setenv(agentHubSeatEnv, "go-dev")

	sock, _ := fakeRuntime(t, func(string) string {
		return `{"ok":false,"error":"seat identity could not be resolved from active rooms"}` + "\n"
	})
	t.Setenv(RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := whoamiCommand{}.Run(context.Background(), []string{"--json"}, &out, &errOut)

	if code != 0 {
		t.Fatalf("whoami exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"environment"`) || !strings.Contains(out.String(), `"go-dev"`) {
		t.Errorf("stdout = %q, want the environment's identity", out.String())
	}
}

// TestResolveTransportIsTheRuntimeOrNothing states the whole seam: a runtime that answers, or no transport.
func TestResolveTransportIsTheRuntimeOrNothing(t *testing.T) {
	t.Run("runtime present", func(t *testing.T) {
		hermeticSeat(t)
		sock, _ := fakeRuntime(t, func(string) string { return `{"ok":true}` + "\n" })
		t.Setenv(RuntimeSocketEnv, sock)
		if got := ResolveTransport(); got != TransportRuntime {
			t.Errorf("ResolveTransport() = %v, want the runtime", got)
		}
	})

	t.Run("no runtime", func(t *testing.T) {
		hermeticSeat(t)
		noRuntimeSocket(t)
		if got := ResolveTransport(); got != TransportNone {
			t.Errorf("ResolveTransport() = %v, want no transport", got)
		}
	})
}
