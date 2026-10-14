package clientfeedback

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
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// runFeedback drives the verb the dispatcher drives: rig is nil because NeedsRig is false, which is
// also the path a test that wants the REAL lazy resolution takes.
func runFeedback(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return feedbackCommand{}.Run(ctx, nil, args, stdout, stderr)
}

// feedbackServer is a routed stand-in for the server: it records the one request it is asked to
// answer and replays a scripted status and payload. Every refusal test asserts hits() == 0, which is
// how "no dial" is proven rather than assumed.
type feedbackServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	method   string
	path     string
	auth     string
	ctype    string
	body     map[string]any
	status   int
	payload  string
}

func newFeedbackServer(t *testing.T) *feedbackServer {
	t.Helper()
	fs := &feedbackServer{status: http.StatusOK, payload: `{"success":true,"stored":true}`}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fs.mu.Lock()
		fs.requests++
		fs.method = r.Method
		fs.path = r.URL.Path
		fs.auth = r.Header.Get("Authorization")
		fs.ctype = r.Header.Get("Content-Type")
		_ = json.Unmarshal(raw, &fs.body)
		status, payload := fs.status, fs.payload
		fs.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (s *feedbackServer) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *feedbackServer) reply(status int, payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.payload = status, payload
}

func (s *feedbackServer) seen() (method, path, auth, ctype string, body map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.method, s.path, s.auth, s.ctype, s.body
}

// feedbackEnv is the credential every dialing test starts from: the one server variable this client
// reads, and the one bearer token.
func feedbackEnv(t *testing.T, serverURL, token string) {
	t.Helper()
	t.Setenv("AGENTHUB_URL", serverURL)
	t.Setenv("AGENTHUB_TOKEN", token)
}

// nativeChain isolates the identity chain from the machine the tests run on: no runtime socket answers, no
// OpenRig daemon is named, and no seat is stated. A test that wants an identity must then state one with
// seatEnv, and a test that wants the refusal gets it on every machine - the isolation the deleted rig stub
// used to give the PATH.
func nativeChain(t *testing.T) {
	t.Helper()
	t.Setenv("OPENRIG_RUNTIME_SOCKET", filepath.Join(t.TempDir(), "no-runtime.sock"))
	t.Setenv("OPENRIG_URL", "")
	t.Setenv("OPENRIG_PORT", "")
	t.Setenv("OPENRIG_SESSION_NAME", "")
	t.Setenv("RIGGED_SESSION_NAME", "")
	t.Setenv("AGENTHUB_SESSION_NAME", "")
	t.Setenv("AGENTHUB_ROOM", "")
	t.Setenv("AGENTHUB_SEAT", "")
}

// seatEnv states this shell's seat the way a native seat states it: the two halves by name. The chain
// renders that pair to SeatSessionName, which is the session name a test can therefore assert.
func seatEnv(t *testing.T, room, seat string) {
	t.Helper()
	t.Setenv("AGENTHUB_ROOM", room)
	t.Setenv("AGENTHUB_SEAT", seat)
}

// The ported Python door test: help is help, and it names the verb.
func TestFeedbackHelpPrintsUsageAndExitsZero(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"--layer", "runtime", "--help"}} {
		var stdout, stderr bytes.Buffer
		code := runFeedback(context.Background(), args, &stdout, &stderr)
		if code != clientcmd.ExitOK {
			t.Fatalf("%v: exit %d, want %d (stderr %q)", args, code, clientcmd.ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "4genteam feedback") {
			t.Errorf("%v: help did not name the verb: %q", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%v: help wrote to stderr: %q", args, stderr.String())
		}
	}
}

// Usage, a missing value and an invalid layer are all refused with a message naming what is wrong,
// and none of them reaches the server: the layer set is checked before any request is built.
func TestFeedbackUsageErrorsNeverDial(t *testing.T) {
	server := newFeedbackServer(t)
	feedbackEnv(t, server.URL, "test-token")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing layer", nil, "--layer is required"},
		{"layer flag with no value", []string{"--layer"}, "--layer is required"},
		{"missing text", []string{"--layer", "runtime"}, "--text is required"},
		{"text flag with no value", []string{"--layer", "runtime", "--text"}, "--text is required"},
		{"invalid layer", []string{"--layer", "harness", "--text", "t"}, `--layer "harness" is not one of runtime, openrig, cloud, seat-context, workspace, other`},
		{"unknown argument", []string{"--layer", "runtime", "--text", "t", "--nope"}, "unknown argument: --nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runFeedback(context.Background(), tc.args, &stdout, &stderr)
			if code != clientcmd.ExitUsage {
				t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want it to carry %q", stderr.String(), tc.want)
			}
			if stdout.Len() != 0 {
				t.Errorf("a usage refusal wrote to stdout: %q", stdout.String())
			}
		})
	}
	if server.hits() != 0 {
		t.Errorf("a usage refusal dialed the server %d time(s)", server.hits())
	}
}

func TestFeedbackMissingCredentialOrURLRefusesWithoutDialing(t *testing.T) {
	server := newFeedbackServer(t)
	report := []string{"--layer", "runtime", "--text", "t", "--room", "dev", "--seat", "alpha", "--session", "dev-alpha@dev"}

	t.Run("no token", func(t *testing.T) {
		feedbackEnv(t, server.URL, "")
		var stdout, stderr bytes.Buffer
		code := runFeedback(context.Background(), report, &stdout, &stderr)
		if code != clientcmd.ExitUsage {
			t.Fatalf("exit %d, want %d", code, clientcmd.ExitUsage)
		}
		if !strings.Contains(stderr.String(), "AGENTHUB_TOKEN") {
			t.Errorf("stderr = %q, want the missing token named", stderr.String())
		}
	})

	t.Run("no url", func(t *testing.T) {
		feedbackEnv(t, "", "tok")
		var stdout, stderr bytes.Buffer
		code := runFeedback(context.Background(), report, &stdout, &stderr)
		if code != clientcmd.ExitUsage {
			t.Fatalf("exit %d, want %d", code, clientcmd.ExitUsage)
		}
		if !strings.Contains(stderr.String(), "no server URL") {
			t.Errorf("stderr = %q, want the missing URL named", stderr.String())
		}
	})

	if server.hits() != 0 {
		t.Errorf("a refusal dialed the server %d time(s)", server.hits())
	}
}

// The happy path: exactly the MCP payload, on the MCP route, with the bearer token, and the answer
// relayed to stdout.
func TestFeedbackPostsTheReportAndRelaysTheAnswer(t *testing.T) {
	server := newFeedbackServer(t)
	feedbackEnv(t, server.URL, "test-token")

	var stdout, stderr bytes.Buffer
	code := runFeedback(context.Background(), []string{
		"--layer", "openrig",
		"--text", "it broke",
		"--room", "dev",
		"--seat", "alpha",
		"--session", "dev-alpha@dev",
	}, &stdout, &stderr)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	method, path, auth, ctype, body := server.seen()
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if path != feedbackPath {
		t.Errorf("path = %q, want %q", path, feedbackPath)
	}
	if auth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the bearer token", auth)
	}
	if ctype != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ctype)
	}
	want := map[string]any{
		"room": "dev", "seat": "alpha", "session": "dev-alpha@dev",
		"layer": "openrig", "text": "it broke",
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("body[%q] = %v, want %v (body %v)", key, body[key], value, body)
		}
	}
	if !strings.Contains(stdout.String(), `"stored":true`) {
		t.Errorf("stdout = %q, want the server's answer relayed", stdout.String())
	}
}

// The default is $AGENTHUB_URL and its trailing slash is trimmed before the route is appended;
// --url points the verb at another origin entirely.
func TestFeedbackURLDefaultAndOverride(t *testing.T) {
	defaultServer := newFeedbackServer(t)
	overrideServer := newFeedbackServer(t)
	feedbackEnv(t, defaultServer.URL+"/", "tok")
	report := []string{"--layer", "runtime", "--text", "t", "--room", "dev", "--seat", "alpha", "--session", "s"}

	var stdout, stderr bytes.Buffer
	if code := runFeedback(context.Background(), report, &stdout, &stderr); code != clientcmd.ExitOK {
		t.Fatalf("default URL: exit %d (stderr %q)", code, stderr.String())
	}
	if _, path, _, _, _ := defaultServer.seen(); path != feedbackPath {
		t.Errorf("the trailing slash was not trimmed: path = %q", path)
	}

	stdout.Reset()
	stderr.Reset()
	args := append(append([]string{}, report...), "--url", overrideServer.URL)
	if code := runFeedback(context.Background(), args, &stdout, &stderr); code != clientcmd.ExitOK {
		t.Fatalf("override URL: exit %d (stderr %q)", code, stderr.String())
	}
	if overrideServer.hits() != 1 {
		t.Errorf("--url did not receive the report: hits = %d", overrideServer.hits())
	}
	if defaultServer.hits() != 1 {
		t.Errorf("--url also dialed the default origin: hits = %d", defaultServer.hits())
	}
}

// Identity defaults come from the client's own identity chain - here its last link, this shell's seat
// environment - and a flag that IS given wins over that reading for that field only. This is the test that
// replaced the exec of `rig whoami --json`.
func TestFeedbackIdentityDefaultsComeFromTheSeatEnvironment(t *testing.T) {
	server := newFeedbackServer(t)
	feedbackEnv(t, server.URL, "tok")
	nativeChain(t)
	seatEnv(t, "dev", "alpha")

	var stdout, stderr bytes.Buffer
	code := runFeedback(context.Background(), []string{"--layer", "runtime", "--text", "t", "--seat", "beta"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	_, _, _, _, body := server.seen()
	want := map[string]any{"room": "dev", "seat": "beta", "session": "agenthub--dev--alpha"}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("body[%q] = %v, want %v (body %v)", key, body[key], value, body)
		}
	}
}

// Nothing in the chain answers - no runtime, no daemon, no seat environment - and an identity is still
// missing: the script's own refusal, at usage, before any dial.
func TestFeedbackIdentityUnavailableIsUsageAndNeverDials(t *testing.T) {
	server := newFeedbackServer(t)
	feedbackEnv(t, server.URL, "tok")
	nativeChain(t)

	var stdout, stderr bytes.Buffer
	code := runFeedback(context.Background(), []string{"--layer", "runtime", "--text", "t"}, &stdout, &stderr)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot read the seat identity") {
		t.Errorf("stderr = %q, want the identity refusal", stderr.String())
	}
	if server.hits() != 0 {
		t.Errorf("the identity refusal dialed the server %d time(s)", server.hits())
	}
}

// A refused answer is exit 1, and the answer's own body is still relayed to stdout.
func TestFeedbackNon2xxExitsRemoteAndRelaysTheAnswer(t *testing.T) {
	server := newFeedbackServer(t)
	server.reply(http.StatusInternalServerError, `{"error":"the server refused it"}`)
	feedbackEnv(t, server.URL, "tok")

	var stdout, stderr bytes.Buffer
	code := runFeedback(context.Background(), []string{
		"--layer", "cloud", "--text", "t", "--room", "dev", "--seat", "alpha", "--session", "s",
	}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 500") {
		t.Errorf("stderr = %q, want the status named", stderr.String())
	}
	if !strings.Contains(stdout.String(), "the server refused it") {
		t.Errorf("stdout = %q, want the answer relayed even on failure", stdout.String())
	}
}

func TestFeedbackTransportFailureExitsRemote(t *testing.T) {
	server := newFeedbackServer(t)
	url := server.URL
	server.Close()
	feedbackEnv(t, url, "tok")

	var stdout, stderr bytes.Buffer
	code := runFeedback(context.Background(), []string{
		"--layer", "cloud", "--text", "t", "--room", "dev", "--seat", "alpha", "--session", "s",
	}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
	}
	if !strings.Contains(stderr.String(), "the request to") {
		t.Errorf("stderr = %q, want the failed request named", stderr.String())
	}
}

// The failure names the REASON, which is what the shell client showed: `curl -sS` prints its own
// diagnostic before the script's line, so a compiled replacement that printed only the line would tell
// the operator less than the door it replaced. The transport is injected, so the reason is asserted
// verbatim with no network - and the credential is not in it.
func TestFeedbackTransportFailureNamesTheReason(t *testing.T) {
	const reason = "dial tcp: connect: connection refused"
	withFeedbackTransport(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New(reason)
	})
	feedbackEnv(t, "http://feedback.invalid", "tok")

	var stdout, stderr bytes.Buffer
	code := runFeedback(context.Background(), []string{
		"--layer", "cloud", "--text", "t", "--room", "dev", "--seat", "alpha", "--session", "s",
	}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
	}
	if !strings.Contains(stderr.String(), reason) {
		t.Errorf("stderr = %q, want the transport reason named", stderr.String())
	}
	if !strings.Contains(stderr.String(), "http://feedback.invalid") {
		t.Errorf("stderr = %q, want the URL named too, as the script did", stderr.String())
	}
	if strings.Contains(stderr.String(), "tok") {
		t.Errorf("stderr = %q, want no credential in it", stderr.String())
	}
}

// withFeedbackTransport installs an in-process transport for the duration of one test, because a push
// path cannot be exercised against a cloud. The previous client is restored in cleanup, so the tests
// that drive a real local server keep the real transport.
func withFeedbackTransport(t *testing.T, round func(*http.Request) (*http.Response, error)) {
	t.Helper()
	previous := feedbackClient
	feedbackClient = &http.Client{Timeout: httpTimeout, Transport: roundTripFunc(round)}
	t.Cleanup(func() { feedbackClient = previous })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestCommandsRegistersTheFeedbackVerb(t *testing.T) {
	commands := Commands()
	if len(commands) != 1 {
		t.Fatalf("Commands() = %d entries, want 1", len(commands))
	}
	command := commands[0]
	if command.Name() != "feedback" {
		t.Errorf("Name() = %q, want feedback", command.Name())
	}
	if command.Summary() == "" {
		t.Error("Summary() is empty")
	}
	if command.NeedsRig() {
		t.Error("NeedsRig() is true: the verb resolves rig lazily so a fully-specified call needs none")
	}
}
