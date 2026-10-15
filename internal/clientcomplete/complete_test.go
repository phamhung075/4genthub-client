package clientcomplete

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientevidence"
	"github.com/phamhung075/4genthub-client/internal/clientjev"
)

func jevConfig(t *testing.T, status int, reply string) (clientjev.Config, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	t.Setenv("TYPESAFE_API_KEY", "key-secret")
	t.Setenv("JEV_DECISION_LOG_PATH", filepath.Join(t.TempDir(), "jev.log"))
	cfg, err := clientjev.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, &hits
}

func passing() clientevidence.Body {
	body := clientevidence.Body{Numstat: "3\t1\tsrc/a.go\n-\t-\tlogo.png\n"}
	body.Test.Command = "go test ./..."
	return body
}

func TestDeterministicChecksRejectWithoutCallingJev(t *testing.T) {
	cfg, hits := jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	cases := map[string]func(*clientevidence.Body){
		"D3: the test run failed":   func(b *clientevidence.Body) { b.Test.ExitCode = 1 },
		"D3: no test command":       func(b *clientevidence.Body) { b.Test.Command = "" },
		"D4: .claude/x.md":          func(b *clientevidence.Body) { b.Numstat = "1\t1\t.claude/x.md\n" },
		"D4: agenthub_go/seatcheck": func(b *clientevidence.Body) { b.Numstat = "1\t1\tagenthub_go/seatcheck/a.go\n" },
		"D4: sub/.env":              func(b *clientevidence.Body) { b.Numstat = "1\t1\tsub/.env\n" },
		"D5: the change touches no": func(b *clientevidence.Body) { b.Numstat = "" },
		"D5: the change is 30000":   func(b *clientevidence.Body) { b.Numstat = "30000\t0\ta.go\n" },
		"D5: numstat line":          func(b *clientevidence.Body) { b.Numstat = "garbage\n" },
	}
	for want, mutate := range cases {
		body := passing()
		mutate(&body)
		got := evaluate(context.Background(), cfg, []string{"it works"}, body)
		if got.Verdict != clientjev.Reject || got.Evaluator != "deterministic" || !strings.Contains(got.Reasons, want) {
			t.Errorf("%s: got %+v", want, got)
		}
	}
	if *hits != 0 {
		t.Errorf("Jev was called %d times for deterministic rejections", *hits)
	}
}

func TestKeepOutDoesNotHitSimilarNames(t *testing.T) {
	for _, path := range []string{".env.sample", "agenthub_client/.env.sample", "docs/claude/notes.md", "src/seatchecker.go"} {
		if pattern, hit := keptOut(path); hit {
			t.Errorf("%s was kept out by %s", path, pattern)
		}
	}
}

func TestCriteriaVerdicts(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  clientjev.Verdict
	}{
		{`{"answers":{"criterion_1":{"noul":0.95},"criterion_2":{"noul":0.85}}}`, clientjev.Accept},
		{`{"answers":{"criterion_1":{"noul":0.95},"criterion_2":{"noul":0.2}}}`, clientjev.Reject},
		{`{"answers":{"criterion_1":{"noul":0.95},"criterion_2":{"noul":0.6}}}`, clientjev.Uncertain},
		{`{"answers":{"criterion_1":{"noul":0.95}}}`, clientjev.Uncertain},
	} {
		cfg, _ := jevConfig(t, 200, tc.reply)
		if got := evaluate(context.Background(), cfg, []string{"a", "b"}, passing()); got.Verdict != tc.want || got.Evaluator != "jev" {
			t.Errorf("%s: got %+v, want %s", tc.reply, got, tc.want)
		}
	}
	cfg, _ := jevConfig(t, 500, "boom")
	if got := evaluate(context.Background(), cfg, []string{"a"}, passing()); got.Verdict != clientjev.Uncertain {
		t.Errorf("an evaluator error must be uncertain, got %+v", got)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	cfg, _ = clientjev.FromEnv()
	if got := evaluate(context.Background(), cfg, []string{"a"}, passing()); got.Verdict != clientjev.Uncertain {
		t.Errorf("no key must be uncertain, got %+v", got)
	}
	if got := evaluate(context.Background(), cfg, nil, passing()); got.Verdict != clientjev.Uncertain {
		t.Errorf("no criteria and clean checks must be uncertain, got %+v", got)
	}
}

// repo makes a git repository with a base commit and one change on top, and returns the base SHA.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "change")
	t.Chdir(dir)
	return base
}

type serverLog struct {
	mu    sync.Mutex
	calls []string
	query string
}

func taskServer(t *testing.T, criteria string) *serverLog {
	t.Helper()
	rec := &serverLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.calls = append(rec.calls, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/complete") {
			rec.query = r.URL.RawQuery
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"success":true,"task":{"acceptance_criteria":`+criteria+`}}`)
		case strings.HasSuffix(r.URL.Path, "/evidence"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"event":"stored"}`)
		default:
			_, _ = io.WriteString(w, `{"success":true,"completed":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENTHUB_URL", srv.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok")
	return rec
}

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := completeCommand{}.Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestAcceptedGateStoresEvidenceThenCompletes(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, `["it works"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "true")
	code, out, errs := run("--task", "t1", "--base", base, "--summary", "done it", "--notes", "ran tests", "--test", "git --version")
	if code != clientcmd.ExitOK || !strings.Contains(out, `"completed":true`) {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	want := []string{"GET /api/v2/tasks/t1", "POST /api/v2/tasks/t1/evidence", "POST /api/v2/tasks/t1/complete"}
	if strings.Join(rec.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v", rec.calls)
	}
	if !strings.Contains(rec.query, "completion_summary=done+it") || !strings.Contains(rec.query, "testing_notes=ran+tests") {
		t.Errorf("complete query = %q", rec.query)
	}
	if strings.Contains(out+errs, "key-secret") {
		t.Error("a credential leaked")
	}
}

func TestEnforcedGateRefusesBeforeAnyServerWrite(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, `["it works"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.1}}}`)
	t.Setenv(enforcementEnv, "true")
	code, out, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	var verdict result
	if err := json.Unmarshal([]byte(out), &verdict); err != nil || code != ExitGateRefused || verdict.Verdict != clientjev.Reject {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "GET /api/v2/tasks/t1" {
		t.Errorf("calls = %v, want only the task read", rec.calls)
	}
}

func TestShadowGateRecordsTheVerdictAndStillCompletes(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, `["it works"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.1}}}`)
	t.Setenv(enforcementEnv, "")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitOK || !strings.Contains(errs, "gate (shadow) reject") || len(rec.calls) != 3 {
		t.Errorf("exit %d err %q calls %v", code, errs, rec.calls)
	}
}

func TestFailingTestRunInShadowCompletesWithExitFive(t *testing.T) {
	base := repo(t)
	taskServer(t, `[]`)
	jevConfig(t, 200, `{}`)
	// SHADOW means `VALIDATION_GATE_ENFORCEMENT` unset, and the test must say so rather than read the
	// machine it happens to run on: the state root's `.env` sets it `true` for every seat's shell, so this
	// test exited `ExitGateRefused` (6) here while asking for the shadow path's 5 (lead ruling, 2026-10-15).
	t.Setenv(enforcementEnv, "")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git no-such-subcommand")
	if code != 5 || !strings.Contains(errs, "gate (shadow) reject") {
		t.Errorf("exit %d err %q", code, errs)
	}
}

func TestUsageRefusalsCallNothing(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, `[]`)
	for _, args := range [][]string{
		{"--base", base, "--summary", "s"},
		{"--task", "t1", "--summary", "s"},
		{"--task", "t1", "--base", base},
		{"--task", "t1", "--base", "nope-ref", "--summary", "s"},
		{"--bogus"},
	} {
		if code, _, _ := run(args...); code != clientcmd.ExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	t.Setenv("AGENTHUB_TOKEN", "")
	if code, _, errs := run("--task", "t1", "--base", base, "--summary", "s"); code != clientcmd.ExitUsage || !strings.Contains(errs, "AGENTHUB_TOKEN") {
		t.Errorf("no token: exit %d %q", code, errs)
	}
	if len(rec.calls) != 0 {
		t.Errorf("refusals called the server: %v", rec.calls)
	}
}

func TestHelp(t *testing.T) {
	if code, out, _ := run("--help"); code != clientcmd.ExitOK || !strings.Contains(out, "4genteam complete") {
		t.Errorf("exit %d out %q", code, out)
	}
}
