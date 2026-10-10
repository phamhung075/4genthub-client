package clientfeedback

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

// stubRig writes a `rig` that answers `whoami --json` with identity and nothing else, and returns its
// path. It is a script on disk rather than a substituted function so the verb's REAL resolution path
// (clientcmd.RequireRig -> exec.LookPath) runs.
func stubRig(t *testing.T, whoamiJSON string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"whoami\" ]; then printf '%s' '" + whoamiJSON + "'; exit 0; fi\nexit 1\n"
	path := filepath.Join(dir, "rig")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func withRigOnPath(t *testing.T, rig string) {
	t.Helper()
	t.Setenv("PATH", filepath.Dir(rig)+string(os.PathListSeparator)+os.Getenv("PATH"))
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

// Identity defaults come from `rig whoami --json`, and a flag that IS given wins over the rig's own
// reading for that field only.
func TestFeedbackIdentityDefaultsComeFromRig(t *testing.T) {
	server := newFeedbackServer(t)
	feedbackEnv(t, server.URL, "tok")
	withRigOnPath(t, stubRig(t, `{"identity":{"rigName":"dev","memberId":"alpha","sessionName":"dev-alpha@dev"}}`))

	var stdout, stderr bytes.Buffer
	code := runFeedback(context.Background(), []string{"--layer", "runtime", "--text", "t", "--seat", "beta"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	_, _, _, _, body := server.seen()
	want := map[string]any{"room": "dev", "seat": "beta", "session": "dev-alpha@dev"}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("body[%q] = %v, want %v (body %v)", key, body[key], value, body)
		}
	}
}

// The identical path with a rig the dispatcher already resolved: a nil rig is resolved lazily, a
// handed one is used directly.
func TestFeedbackUsesTheRigItWasHanded(t *testing.T) {
	server := newFeedbackServer(t)
	feedbackEnv(t, server.URL, "tok")
	rig := stubRig(t, `{"identity":{"rigName":"dev","memberId":"alpha","sessionName":"s"}}`)

	var stdout, stderr bytes.Buffer
	code := feedbackCommand{}.Run(context.Background(), &clientcmd.Rig{Program: rig},
		[]string{"--layer", "runtime", "--text", "t"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	if _, _, _, _, body := server.seen(); body["room"] != "dev" || body["seat"] != "alpha" {
		t.Errorf("the handed rig's identity was not used: body = %v", body)
	}
}

// No rig at all, and an identity still missing: the script's own refusal, at usage, before any dial.
func TestFeedbackIdentityUnavailableIsUsageAndNeverDials(t *testing.T) {
	server := newFeedbackServer(t)
	feedbackEnv(t, server.URL, "tok")
	t.Setenv("PATH", t.TempDir())

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
