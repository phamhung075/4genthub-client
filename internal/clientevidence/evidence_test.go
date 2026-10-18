package clientevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// The two SHAs every dialing test starts from, and the raw numstat the diff seam prints for their range.
const (
	baseSHA = "1111111111111111111111111111111111111111"
	headSHA = "2222222222222222222222222222222222222222"
	numstat = "12\t3\tinternal/foo.go\n0\t7\tdocs/bar.md\n"
)

// runEvidence drives the verb the dispatcher drives: no rig is involved.
func runEvidence(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return evidenceCommand{}.Run(ctx, args, stdout, stderr)
}

// evidenceServer is a routed stand-in for the route: it records the one request it is asked to answer
// and replays a scripted status and payload. hits() == 0 is how "no dial" is proven rather than
// assumed, and the RAW body is kept so a test can assert nothing outside the wire fields leaked into it.
type evidenceServer struct {
	*httptest.Server
	mu sync.Mutex
	// requests counts the SUBMISSIONS. The done report's row read is a second request and is counted apart
	// (reads), so a case can still say "the refusal dialed the route once" and "a stored submission also read
	// the row it closed" without the two numbers standing for different things.
	requests int
	reads    int
	method   string
	path     string
	auth     string
	ctype    string
	raw      []byte
	status   int
	payload  string
	lastRead string
}

func newEvidenceServer(t *testing.T) *evidenceServer {
	t.Helper()
	es := &evidenceServer{
		status:  http.StatusCreated,
		payload: `{"id":7,"task_id":"task-1","seq":3,"kind":"evidence_submitted","actor_kind":"user","actor_id":"u1","payload":{},"created_at":"2026-10-11T00:00:00Z"}`,
	}
	es.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodGet {
			// The done report's row read: the board's own envelope, `{"success": true, "task": {...}}`.
			es.mu.Lock()
			es.reads++
			es.lastRead = r.URL.Path
			es.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"success":true,"task":{"id":"task-1","title":"a row the report names","status":"in_progress"}}`)
			return
		}
		es.mu.Lock()
		es.requests++
		es.method = r.Method
		es.path = r.URL.Path
		es.auth = r.Header.Get("Authorization")
		es.ctype = r.Header.Get("Content-Type")
		es.raw = raw
		status, payload := es.status, es.payload
		es.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(es.Close)
	return es
}

func (s *evidenceServer) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *evidenceServer) reply(status int, payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.payload = status, payload
}

func (s *evidenceServer) seen() (method, path, auth, ctype string, raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.method, s.path, s.auth, s.ctype, s.raw
}

// evidenceEnv is the credential every dialing test starts from: the one server variable this client
// reads, and the one bearer token.
func evidenceEnv(t *testing.T, serverURL, token string) {
	t.Helper()
	t.Setenv("AGENTHUB_URL", serverURL)
	t.Setenv("AGENTHUB_TOKEN", token)
}

// fakeGit is the git seam: it answers by argv key and records every call, so the verb's exact argv is
// asserted without a repository.
type fakeGit struct {
	calls   [][]string
	replies map[string]string
	fails   map[string]error
}

func (f *fakeGit) output(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args, " ")
	if err, ok := f.fails[key]; ok {
		return "", err
	}
	if out, ok := f.replies[key]; ok {
		return out, nil
	}
	return "", fmt.Errorf("unexpected git call: %s", key)
}

func withGit(t *testing.T, f *fakeGit) *fakeGit {
	t.Helper()
	previous := gitOutput
	gitOutput = f.output
	t.Cleanup(func() { gitOutput = previous })
	return f
}

// standardGit is the seam every dialing test starts from: the base and HEAD both resolve, and the diff
// prints the two numstat lines the body must carry verbatim.
func standardGit() *fakeGit {
	return &fakeGit{replies: map[string]string{
		"rev-parse --verify --end-of-options main^{commit}": baseSHA + "\n",
		"rev-parse --verify --end-of-options HEAD^{commit}": headSHA + "\n",
		"diff --numstat " + baseSHA + ".." + headSHA:        numstat,
	}}
}

// testRun is what the --test seam answers: combined output, exit code, or a failure to START.
type testRun struct {
	output   string
	exitCode int
	startErr error
}

func withTestCommand(t *testing.T, run testRun) *[][]string {
	t.Helper()
	calls := &[][]string{}
	previous := runTestCommand
	runTestCommand = func(_ context.Context, argv []string) (string, int, error) {
		*calls = append(*calls, argv)
		return run.output, run.exitCode, run.startErr
	}
	t.Cleanup(func() { runTestCommand = previous })
	return calls
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// withEvidenceTransport installs an in-process transport for the duration of one test, because this push
// path cannot be exercised against a cloud.
func withEvidenceTransport(t *testing.T, round func(*http.Request) (*http.Response, error)) {
	t.Helper()
	previous := evidenceClient
	evidenceClient = &http.Client{Transport: roundTripFunc(round)}
	t.Cleanup(func() { evidenceClient = previous })
}

func TestEvidenceHelpPrintsUsageAndExitsZero(t *testing.T) {
	git := withGit(t, standardGit())
	for _, args := range [][]string{{"-h"}, {"--help"}, {"--task", "task-1", "--help"}} {
		var stdout, stderr bytes.Buffer
		code := runEvidence(context.Background(), args, &stdout, &stderr)
		if code != clientcmd.ExitOK {
			t.Fatalf("%v: exit %d, want %d (stderr %q)", args, code, clientcmd.ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "4genteam evidence") {
			t.Errorf("%v: help did not name the verb: %q", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%v: help wrote to stderr: %q", args, stderr.String())
		}
	}
	if len(git.calls) != 0 {
		t.Errorf("help ran git: %v", git.calls)
	}
}

func TestEvidenceUsageErrorsNeverWork(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	git := withGit(t, standardGit())

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing task", nil, "--task is required"},
		{"task flag with no value", []string{"--task"}, "--task is required"},
		{"missing base", []string{"--task", "task-1"}, "--base is required"},
		{"base flag with no value", []string{"--task", "task-1", "--base"}, "--base is required"},
		{"unknown argument", []string{"--task", "task-1", "--base", "main", "--nope"}, "unknown argument: --nope"},
		{"unterminated quote in the test line", []string{"--task", "task-1", "--base", "main", "--test", "go test 'oops"}, "--test has an unterminated quote"},
		{"a sentence in the test line", []string{"--task", "task-1", "--base", "main", "--test", "go test ./... (green at 1daa0e6d; repoguards 0.2s)"}, "--test is split into an argument list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runEvidence(context.Background(), tc.args, &stdout, &stderr)
			if code != clientcmd.ExitUsage {
				t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want it to name %q", stderr.String(), tc.want)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: a usage refusal does no work", stdout.String())
			}
			if server.hits() != 0 {
				t.Errorf("the usage refusal dialed the server %d time(s)", server.hits())
			}
			if len(git.calls) != 0 {
				t.Errorf("the usage refusal ran git: %v", git.calls)
			}
		})
	}
}

func TestEvidenceMissingCredentialOrURLRefusesWithoutWorking(t *testing.T) {
	git := withGit(t, standardGit())
	server := newEvidenceServer(t)

	t.Run("no token", func(t *testing.T) {
		evidenceEnv(t, server.URL, "")
		var stdout, stderr bytes.Buffer
		code := runEvidence(context.Background(), []string{"--task", "t", "--base", "main"}, &stdout, &stderr)
		if code != clientcmd.ExitUsage {
			t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "AGENTHUB_TOKEN") {
			t.Errorf("stderr = %q, want the missing variable named", stderr.String())
		}
	})

	t.Run("no url", func(t *testing.T) {
		evidenceEnv(t, "", "tok")
		var stdout, stderr bytes.Buffer
		code := runEvidence(context.Background(), []string{"--task", "t", "--base", "main"}, &stdout, &stderr)
		if code != clientcmd.ExitUsage {
			t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "AGENTHUB_URL") {
			t.Errorf("stderr = %q, want the missing variable named", stderr.String())
		}
	})

	if server.hits() != 0 {
		t.Errorf("a missing credential dialed the server %d time(s)", server.hits())
	}
	if len(git.calls) != 0 {
		t.Errorf("a missing credential ran git: %v", git.calls)
	}
}

// The happy path with a FAILING test run: the frozen payload on the frozen route, with the bearer token,
// the created event relayed to stdout, and the test failure surfaced instead of swallowed.
func TestEvidencePostsTheFrozenPayloadAndSurfacesTheFailedRun(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "test-token")
	git := withGit(t, standardGit())
	const output = "=== RUN   TestAlpha\n" +
		"SECRET-MARKER the raw output must never be sent\n" +
		"--- FAIL: TestAlpha (0.00s)\n" +
		"    evidence_test.go:12: want 1 got 2\n" +
		"password=hunter2\n" +
		"--- FAIL: TestBeta (0.01s)\n" +
		"FAIL\n"
	calls := withTestCommand(t, testRun{output: output, exitCode: 1})

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{
		"--task", "task-1", "--base", "main", "--test", "go test -v ./...",
	}, &stdout, &stderr)
	if code != exitTestFailed {
		t.Fatalf("exit %d, want %d (a stored submission whose test run failed; stderr %q)", code, exitTestFailed, stderr.String())
	}

	method, path, auth, ctype, raw := server.seen()
	if method != http.MethodPost || path != "/api/v2/tasks/task-1/evidence" {
		t.Errorf("%s %s, want POST /api/v2/tasks/task-1/evidence", method, path)
	}
	if auth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the bearer token", auth)
	}
	if ctype != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ctype)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body is not JSON: %v (%s)", err, raw)
	}
	assertKeys(t, "the payload", body, "base_sha", "head_sha", "numstat", "test")
	test, ok := body["test"].(map[string]any)
	if !ok {
		t.Fatalf("test = %T, want an object", body["test"])
	}
	assertKeys(t, "the test block", test, "command", "exit_code", "failed")

	if body["base_sha"] != baseSHA {
		t.Errorf("base_sha = %v, want %s", body["base_sha"], baseSHA)
	}
	if body["head_sha"] != headSHA {
		t.Errorf("head_sha = %v, want %s", body["head_sha"], headSHA)
	}
	if body["numstat"] != numstat {
		t.Errorf("numstat = %q, want the raw git output %q", body["numstat"], numstat)
	}
	if test["command"] != "go test -v ./..." {
		t.Errorf("test.command = %v, want the line as typed", test["command"])
	}
	if test["exit_code"] != float64(1) {
		t.Errorf("test.exit_code = %v, want 1", test["exit_code"])
	}
	failed, ok := test["failed"].([]any)
	if !ok {
		t.Fatalf("test.failed = %T, want an array", test["failed"])
	}
	if len(failed) != 2 || failed[0] != "TestAlpha" || failed[1] != "TestBeta" {
		t.Errorf("test.failed = %v, want [TestAlpha TestBeta]", failed)
	}

	if got := (*calls)[0]; len(got) != 4 || got[0] != "go" || got[1] != "test" || got[2] != "-v" || got[3] != "./..." {
		t.Errorf("the test argv = %v, want [go test -v ./...]", got)
	}

	// THE PRIVACY RULE: no output body and no file content may reach the wire, only names.
	for _, leak := range []string{"SECRET-MARKER", "hunter2", "want 1 got 2", "=== RUN"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("the payload leaked %q: %s", leak, raw)
		}
	}

	if !strings.Contains(stdout.String(), "evidence_submitted") {
		t.Errorf("stdout = %q, want the created event relayed", stdout.String())
	}
	if !strings.Contains(stderr.String(), "exited 1") || !strings.Contains(stderr.String(), "TestAlpha") {
		t.Errorf("stderr = %q, want the test failure surfaced", stderr.String())
	}

	// The argv the verb ran is the frozen one, in order, over the RESOLVED range.
	wantCalls := []string{
		"rev-parse --verify --end-of-options main^{commit}",
		"rev-parse --verify --end-of-options HEAD^{commit}",
		"diff --numstat " + baseSHA + ".." + headSHA,
	}
	if len(git.calls) != len(wantCalls) {
		t.Fatalf("git calls = %v, want %v", git.calls, wantCalls)
	}
	for i, want := range wantCalls {
		if got := strings.Join(git.calls[i], " "); got != want {
			t.Errorf("git call %d = %q, want %q", i, got, want)
		}
	}
}

// postedFailed reads the failed list out of the body the server was sent.
func postedFailed(t *testing.T, server *evidenceServer) []string {
	t.Helper()
	_, _, _, _, raw := server.seen()
	var body struct {
		Test struct {
			Failed []string `json:"failed"`
		} `json:"test"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body is not JSON: %v (%s)", err, raw)
	}
	return body.Test.Failed
}

// A PASSING run names no failures, whatever its own output printed: extraction runs only on a non-zero
// exit, so a program's line - even one spelled exactly like a runner's summary - cannot become a name in
// evidence that reports a green run.
func TestEvidencePassingRunNamesNoFailures(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, standardGit())
	withTestCommand(t, testRun{
		output: "FAILED tests/test_x.py::test_y - AssertionError\n" +
			"--- FAIL: TestAlpha (0.00s)\n" +
			"  1 passed (1.3s)\n",
		exitCode: 0,
	})

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{
		"--task", "t", "--base", "main", "--test", "go test -v ./...",
	}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	if failed := postedFailed(t, server); len(failed) != 0 {
		t.Errorf("a passing run's evidence named failures %v, want none", failed)
	}
}

// A FAILING run whose output names no test carries the explicit marker: an empty list would read as
// "nothing failed", which is the reading the room made of seq 26's evidence.
func TestEvidenceFailingRunWithNoNamedTestSaysSo(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, standardGit())
	withTestCommand(t, testRun{output: "Error: cannot find module 'playwright'\n", exitCode: 1})

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{
		"--task", "t", "--base", "main", "--test", "playwright test",
	}, &stdout, &stderr)
	if code != exitTestFailed {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitTestFailed, stderr.String())
	}
	failed := postedFailed(t, server)
	if len(failed) != 1 || failed[0] != unnamedFailure {
		t.Errorf("a failing run that named no test stored %v, want [%s]", failed, unnamedFailure)
	}
	if !strings.Contains(stderr.String(), "exited 1") {
		t.Errorf("stderr = %q, want the failing run surfaced", stderr.String())
	}
}

func TestEvidenceWithoutATestRunCarriesAnEmptyBlock(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, standardGit())
	calls := withTestCommand(t, testRun{startErr: errors.New("the test seam must not be reached")})

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "t", "--base", "main"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	if len(*calls) != 0 {
		t.Errorf("a submission with no --test ran a test command: %v", *calls)
	}
	_, _, _, _, raw := server.seen()
	var body struct {
		Test struct {
			Command  string   `json:"command"`
			ExitCode int      `json:"exit_code"`
			Failed   []string `json:"failed"`
		} `json:"test"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body is not JSON: %v (%s)", err, raw)
	}
	if body.Test.Command != "" || body.Test.ExitCode != 0 || body.Test.Failed == nil || len(body.Test.Failed) != 0 {
		t.Errorf("test block = %+v, want an empty command, exit 0 and an empty (not missing) failure list", body.Test)
	}
	if !strings.Contains(string(raw), `"failed":[]`) {
		t.Errorf("body = %s, want the failure list present as []", raw)
	}
}

// A 409 is a REFUSAL, not a success: nothing was stored, the previous evidence for that head_sha stands,
// and the refusal names it. stdout must not carry the refusal, where a caller would read it as stored.
func TestEvidenceConflictIsARefusalNotASuccess(t *testing.T) {
	server := newEvidenceServer(t)
	server.reply(http.StatusConflict, `{"detail":"evidence for head_sha `+headSHA+` already exists (event 7)"}`)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, standardGit())

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "task-1", "--base", "main"}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
	}
	if !strings.Contains(stderr.String(), "409") {
		t.Errorf("stderr = %q, want the status named", stderr.String())
	}
	if !strings.Contains(stderr.String(), headSHA) {
		t.Errorf("stderr = %q, want the head_sha the prior evidence holds named", stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists (event 7)") {
		t.Errorf("stderr = %q, want the server's own words relayed", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: a refusal must not read as a stored event", stdout.String())
	}
	if server.hits() != 1 {
		t.Errorf("the refusal dialed the route %d time(s), want 1", server.hits())
	}
}

// Every other refused answer is exit 1 with the status named and the body relayed on stderr; the over-cap
// 422 is the case the route's own contract describes.
func TestEvidenceOtherRefusalsNameTheStatusAndRelayTheBody(t *testing.T) {
	for _, tc := range []struct {
		status   int
		payload  string
		wantBody string
	}{
		{http.StatusUnprocessableEntity, `{"detail":"numstat exceeds the 1048576 byte cap"}`, "numstat exceeds the 1048576 byte cap"},
		{http.StatusInternalServerError, `{"error":"boom"}`, "boom"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			server := newEvidenceServer(t)
			server.reply(tc.status, tc.payload)
			evidenceEnv(t, server.URL, "tok")
			withGit(t, standardGit())

			var stdout, stderr bytes.Buffer
			code := runEvidence(context.Background(), []string{"--task", "t", "--base", "main"}, &stdout, &stderr)
			if code != clientcmd.ExitRemote {
				t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
			}
			if !strings.Contains(stderr.String(), fmt.Sprintf("HTTP %d", tc.status)) {
				t.Errorf("stderr = %q, want the status named", stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantBody) {
				t.Errorf("stderr = %q, want the server's own words (%q) relayed", stderr.String(), tc.wantBody)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing on a refusal", stdout.String())
			}
		})
	}
}

// The base is the CALLER's error when it does not name a commit, and it costs no dialect, no test run and
// no dial.
func TestEvidenceUnresolvableBaseIsUsageAndNeverWorks(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, &fakeGit{fails: map[string]error{
		"rev-parse --verify --end-of-options nope^{commit}": errors.New("git rev-parse nope^{commit} failed: exit status 128: fatal: needed a single revision"),
	}})
	withTestCommand(t, testRun{startErr: errors.New("must not run")})

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "t", "--base", "nope", "--test", "go test ./..."}, &stdout, &stderr)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--base nope") {
		t.Errorf("stderr = %q, want the offending ref named", stderr.String())
	}
	if server.hits() != 0 {
		t.Errorf("an unresolvable base dialed the server %d time(s)", server.hits())
	}
}

// A test command that cannot be STARTED is not a test result: no exit code is invented and nothing is
// submitted.
func TestEvidenceUnstartableTestCommandIsRemoteAndNeverDials(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, standardGit())
	withTestCommand(t, testRun{startErr: errors.New(`exec: "nope": executable file not found in $PATH`)})

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "t", "--base", "main", "--test", "nope --run"}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not run") || !strings.Contains(stderr.String(), "executable file not found") {
		t.Errorf("stderr = %q, want the start failure named", stderr.String())
	}
	if server.hits() != 0 {
		t.Errorf("an unstartable test command dialed the server %d time(s)", server.hits())
	}
}

// A failing git diff is a remote-side failure that costs no submission.
func TestEvidenceDiffFailureIsRemoteAndNeverDials(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, &fakeGit{
		replies: map[string]string{
			"rev-parse --verify --end-of-options main^{commit}": baseSHA + "\n",
			"rev-parse --verify --end-of-options HEAD^{commit}": headSHA + "\n",
		},
		fails: map[string]error{
			"diff --numstat " + baseSHA + ".." + headSHA: errors.New("git diff failed: exit status 128"),
		},
	})

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "t", "--base", "main"}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
	}
	if !strings.Contains(stderr.String(), "git diff failed") {
		t.Errorf("stderr = %q, want git's failure named", stderr.String())
	}
	if server.hits() != 0 {
		t.Errorf("a failed diff dialed the server %d time(s)", server.hits())
	}
}

// --url overrides $AGENTHUB_URL and a trailing slash is trimmed before the route is appended.
func TestEvidenceURLOverrideTrimsTheTrailingSlash(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, "http://ignored.invalid", "tok")
	withGit(t, standardGit())

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{
		"--task", "task-9", "--base", "main", "--url", server.URL + "/",
	}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	if _, path, _, _, _ := server.seen(); path != "/api/v2/tasks/task-9/evidence" {
		t.Errorf("path = %q, want the evidence route without a doubled slash", path)
	}
}

// The failure names the REASON the transport gave, with no network - and the credential is not in it.
func TestEvidenceTransportFailureNamesTheReasonAndNeverTheToken(t *testing.T) {
	const reason = "dial tcp: connect: connection refused"
	withEvidenceTransport(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New(reason)
	})
	evidenceEnv(t, "http://evidence.invalid", "super-secret-token")
	withGit(t, standardGit())

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "t", "--base", "main"}, &stdout, &stderr)
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitRemote, stderr.String())
	}
	if !strings.Contains(stderr.String(), reason) {
		t.Errorf("stderr = %q, want the transport reason named", stderr.String())
	}
	if !strings.Contains(stderr.String(), "http://evidence.invalid") {
		t.Errorf("stderr = %q, want the URL named too", stderr.String())
	}
	if strings.Contains(stderr.String(), "super-secret-token") || strings.Contains(stdout.String(), "super-secret-token") {
		t.Errorf("the credential was printed: stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"go test ./...", []string{"go", "test", "./..."}},
		{"  go   test  ", []string{"go", "test"}},
		{`go test -run 'TestAlpha|TestBeta' ./...`, []string{"go", "test", "-run", "TestAlpha|TestBeta", "./..."}},
		{`python -m pytest -k "not slow" tests/`, []string{"python", "-m", "pytest", "-k", "not slow", "tests/"}},
		{`echo "  spaced  "`, []string{"echo", "  spaced  "}},
		{`echo a\ b`, []string{"echo", "a b"}},
		// A quoted line is a shell's own job, so the characters a shell acts on survive inside the quotes
		// and the caller that wants them names the shell.
		{`sh -c 'go test ./... (green at 1daa0e6d)'`, []string{"sh", "-c", "go test ./... (green at 1daa0e6d)"}},
		{"", nil},
	}
	for _, tc := range cases {
		got, err := splitCommand(tc.line)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", tc.line, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%q = %v, want %v", tc.line, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%q = %v, want %v", tc.line, got, tc.want)
				break
			}
		}
	}
	for _, line := range []string{"go test 'oops", `echo "oops`, `echo "oops'`} {
		if _, err := splitCommand(line); err == nil {
			t.Errorf("splitCommand(%q) accepted an unterminated quote", line)
		}
	}
	// Measured 2026-10-18: this exact line was passed to a completion gate, ran as `go ... (green`, exited
	// non-zero and was read as D3 "the test run failed" over a green tree. Each metacharacter is now a
	// refusal naming the splitting; the quoted forms above are how a line that needs one still runs.
	for _, line := range []string{
		"go test ./... (green at 1daa0e6d)",
		"go test ./... | grep FAIL",
		"go test ./...; echo done",
		"go test ./... > out.txt",
		"echo `date`",
	} {
		_, err := splitCommand(line)
		if err == nil {
			t.Errorf("splitCommand(%q) accepted an unquoted shell metacharacter", line)
			continue
		}
		if !strings.Contains(err.Error(), "argument list") {
			t.Errorf("splitCommand(%q) error = %q, want it to name the argument-list split", line, err)
		}
	}
}

// The extraction is heuristic, so its exact inputs and answers are pinned here: names only, in print
// order, once each, and nothing from a line that names no test.
func TestExtractFailedTestsIsHeuristicAndNamesOnly(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   []string
	}{
		{
			"go test -v",
			"=== RUN   TestAlpha\n--- FAIL: TestAlpha (0.00s)\n    x_test.go:1: nope\n--- FAIL: TestBeta/sub (0.01s)\nFAIL\n",
			[]string{"TestAlpha", "TestBeta/sub"},
		},
		{
			"pytest short summary",
			"FAILED tests/test_x.py::test_y - AssertionError: nope\n1 failed, 2 passed\n",
			[]string{"tests/test_x.py::test_y"},
		},
		{
			"a program's own line that merely starts with FAILED names nothing",
			"FAILED READ {\"status\":503,\"text\":\"the spec log line that stored READ as a test\"}\n",
			[]string{},
		},
		{
			"Playwright list reporter, measured on agenthub-frontend/playwright.config.ts",
			"  ✘  1 [chromium] › agenthub-frontend/e2e/x.spec.ts:6:5 › deliberate failure A (5ms)\n" +
				"  ✓  3 [chromium] › agenthub-frontend/e2e/x.spec.ts:17:5 › the passing control (4ms)\n" +
				"  ✘  2 [chromium] › agenthub-frontend/e2e/x.spec.ts:12:7 › a nested group › deliberate failure B (5ms)\n" +
				"\n  2 failed\n" +
				"    [chromium] › agenthub-frontend/e2e/x.spec.ts:6:5 › deliberate failure A ────────────\n" +
				"  1 passed (1.3s)\n",
			[]string{
				"agenthub-frontend/e2e/x.spec.ts:6:5 › deliberate failure A",
				"agenthub-frontend/e2e/x.spec.ts:12:7 › a nested group › deliberate failure B",
			},
		},
		{
			"TAP",
			"ok 1 - a passing one\nnot ok 2 - a failing one\n",
			[]string{"a failing one"},
		},
		{
			"a name printed twice is reported once",
			"--- FAIL: TestAlpha (0.00s)\n--- FAIL: TestAlpha (0.01s)\n",
			[]string{"TestAlpha"},
		},
		{
			"a package-level FAIL names no test",
			"FAIL\tgithub.com/x/y\t0.1s\nFAIL\n",
			[]string{},
		},
		{"no output", "", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFailedTests(tc.output)
			if len(got) != len(tc.want) {
				t.Fatalf("extractFailedTests = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("extractFailedTests = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

func TestCommandsRegistersTheEvidenceVerb(t *testing.T) {
	commands := Commands()
	if len(commands) != 1 {
		t.Fatalf("Commands() = %d entries, want 1", len(commands))
	}
	if commands[0].Name() != "evidence" {
		t.Errorf("Name() = %q, want evidence", commands[0].Name())
	}
}

// assertKeys checks the field set exactly: an extra field is a leak, a missing one breaks the contract.
func assertKeys(t *testing.T, what string, object map[string]any, want ...string) {
	t.Helper()
	if len(object) != len(want) {
		t.Fatalf("%s has %d fields %v, want exactly %v", what, len(object), object, want)
	}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			t.Errorf("%s is missing %q", what, key)
		}
	}
}

// A STORED submission prints the done report: the row it closes, read back from the board route, the size of
// the diff, the doc-drift line and the tests named. It is a report, not a gate - the stored event is still what
// the exit code answers for.
func TestAStoredSubmissionPrintsTheDoneReport(t *testing.T) {
	server := newEvidenceServer(t)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, standardGit())

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "task-1", "--base", "main"}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"done report for task task-1",
		"row: a row the report names [in_progress]",
		"diff " + baseSHA[:7] + ".." + headSHA[:7] + ": 2 file(s) changed",
		"doc drift: none (1 of 2 file(s) are markdown)",
		"tests: none named (the submission carries an empty test block)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	server.mu.Lock()
	read, path := server.reads, server.lastRead
	server.mu.Unlock()
	if read != 1 || path != "/api/v2/tasks/task-1" {
		t.Errorf("the report read %d time(s) at %q, want the task route once", read, path)
	}
}

// A REFUSED submission prints no report and reads no row: the refusal belongs on stderr, and a row read for a
// submission the server refused would be a second request paid for a report about nothing.
func TestARefusedSubmissionPrintsNoDoneReport(t *testing.T) {
	server := newEvidenceServer(t)
	server.reply(http.StatusConflict, `{"detail":"evidence for head_sha `+headSHA+` already exists"}`)
	evidenceEnv(t, server.URL, "tok")
	withGit(t, standardGit())

	var stdout, stderr bytes.Buffer
	code := runEvidence(context.Background(), []string{"--task", "task-1", "--base", "main"}, &stdout, &stderr)
	if code == clientcmd.ExitOK {
		t.Fatalf("a refused submission exited 0")
	}
	if strings.Contains(stdout.String(), "done report") {
		t.Errorf("a refused submission printed a done report: %q", stdout.String())
	}
	server.mu.Lock()
	read := server.reads
	server.mu.Unlock()
	if read != 0 {
		t.Errorf("a refused submission read the board row %d time(s)", read)
	}
}
