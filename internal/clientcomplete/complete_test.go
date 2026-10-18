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

// jevServer answers every Jev call with reply, counts them, and records the criteria the judge was asked
// about: the register gate's whole effect is WHICH criteria reach the judge, so a count alone would pass on
// a gate that silently dropped one.
func jevServer(t *testing.T, reply string) (clientjev.Config, *int, *[]string) {
	t.Helper()
	hits := 0
	asked := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Questions map[string]struct {
				Instructions string `json:"instructions"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(raw, &payload); err == nil {
			for _, q := range payload.Questions {
				asked = append(asked, q.Instructions)
			}
		}
		w.WriteHeader(http.StatusOK)
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
	return cfg, &hits, &asked
}

// measured is the bundle of the lead's calibration run 3 (hub 283fb358 Progress 1): the command that was
// run, its exit code, the four paths the numstat names, and the base the criteria cite.
func measured() clientevidence.Body {
	body := clientevidence.Body{BaseSHA: "b32369c1f4a2b3c4d5e6f708192a3b4c5d6e7f80",
		HeadSHA: "d5c9b5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8",
		Numstat: "12\t3\tinternal/roombudget/roombudget.go\n8\t2\tinternal/seatpark/park.go\n" +
			"5\t1\trust/agenthub-runtime/assets/seat-guard.ts\n20\t0\trust/agenthub-runtime/tests/seat_guard.rs\n"}
	body.Test.Command = "go test -count=1 ./..."
	return body
}

// measuredCriteria are the five clauses that answered ACCEPT at 0.98/0.96/0.96/0.97/0.97 in that run: one
// exit-code clause and four numstat-path clauses. They are the register's own shape, so the gate must judge
// them exactly as it did before the register existed.
var measuredCriteria = []string{
	"the acceptance command `go test -count=1 ./...` exits 0",
	"measured from base b32369c, the numstat names internal/roombudget/roombudget.go",
	"measured from base b32369c, the numstat names internal/seatpark/park.go",
	"measured from base b32369c, the numstat names rust/agenthub-runtime/assets/seat-guard.ts",
	"measured from base b32369c, the numstat names rust/agenthub-runtime/tests/seat_guard.rs",
}

// outcomeOneCriteria are task c916e113's criteria as the task holds them (the lead's re-worded set, Progress
// 4). Every one is a production-behaviour claim and none names a fact this bundle carries, which is the case
// the gate must REFUSE rather than score: the same criteria scored 0.29/0.24/0.39.
var outcomeOneCriteria = []string{
	"The served production bundle no longer references /api/v2/openrig: the served bytes contain openrig 0 times " +
		"and /api/v2/rooms/ 21 times, and the panel reads /api/v2/rooms/<room>/decisions",
	"The panel's failed decisions read names its HTTP status on production: the e2e spec injects a 503 in the " +
		"browser, asserts the status token, and passes on production",
	"The panel's pending-row half is settled: the pending row with its attention animation is proven against an " +
		"injected payload (evidence files on disk), and production holds no pending decision to render, which the " +
		"owner accepted as a stated limit on 2026-10-16T22:31Z",
}

func TestTheMeasuredRegisterIsJudgedAndAccepted(t *testing.T) {
	cfg, hits, asked := jevServer(t, `{"answers":{"criterion_1":{"noul":0.98},"criterion_2":{"noul":0.96},`+
		`"criterion_3":{"noul":0.96},"criterion_4":{"noul":0.97},"criterion_5":{"noul":0.97}}}`)
	got := evaluate(context.Background(), cfg, measuredCriteria, measured())
	if got.Verdict != clientjev.Accept || got.Evaluator != "jev" {
		t.Fatalf("the measured register must still be judged as it was: got %+v", got)
	}
	if *hits != 1 {
		t.Errorf("Jev was called %d times, want once: every criterion of the register is judged", *hits)
	}
	if len(*asked) != len(measuredCriteria) {
		t.Errorf("the judge was asked %d criteria, want %d: %v", len(*asked), len(measuredCriteria), *asked)
	}
}

func TestOutcomeOnesCriteriaAreUnjudgeableWithNoJudgeCall(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99},"criterion_2":{"noul":0.99},`+
		`"criterion_3":{"noul":0.99}}}`)
	got := evaluate(context.Background(), cfg, outcomeOneCriteria, passing())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" {
		t.Fatalf("criteria the bundle cannot carry must be refused, not scored: got %+v", got)
	}
	if *hits != 0 {
		t.Errorf("Jev was called %d times for criteria the bundle carries no fact about", *hits)
	}
	for _, want := range []string{"openrig 0 times", "HTTP status on production", "pending-row half"} {
		if !strings.Contains(got.Reasons, want) {
			t.Errorf("the refusal must name the criterion it refused (%q):\n%s", want, got.Reasons)
		}
	}
	for _, want := range []string{`"go test ./..."`, "src/a.go"} {
		if !strings.Contains(got.Reasons, want) {
			t.Errorf("the refusal must print the register the bundle does carry (%q):\n%s", want, got.Reasons)
		}
	}
}

func TestACriterionIsJudgedOnlyWhenTheBundleCarriesItsFact(t *testing.T) {
	for _, tc := range []struct {
		name      string
		criterion string
		refused   bool
	}{
		{"the submitted command", "the acceptance command `go test -count=1 ./...` exits 0", false},
		{"this run's own exit code", "the run exits 0 with nothing failed", false},
		{"a path the numstat lists", "the numstat names internal/seatpark/park.go", false},
		{"a path the numstat does not list", "the numstat names internal/nowhere/absent.go", true},
		{"production behaviour", "the panel renders the pending row on production", true},
	} {
		cfg, hits, asked := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
		got := evaluate(context.Background(), cfg, []string{tc.criterion}, measured())
		if tc.refused {
			if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" || *hits != 0 {
				t.Errorf("%s: got %+v, %d judge calls; want a deterministic refusal and no call: %v",
					tc.name, got, *hits, *asked)
			}
			continue
		}
		if got.Verdict != clientjev.Accept || got.Evaluator != "jev" || *hits != 1 {
			t.Errorf("%s: got %+v, %d judge calls; want it judged and accepted", tc.name, got, *hits)
		}
	}
}

func TestABaseShaPrefixAloneIsJudged(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	got := evaluate(context.Background(), cfg, []string{"the change is built on base b32369c"}, measured())
	if got.Verdict != clientjev.Accept || got.Evaluator != "jev" || *hits != 1 {
		t.Errorf("a sha prefix IS a bundle fact, so the criterion is judged: got %+v, %d judge calls", got, *hits)
	}
	cfg, hits, _ = jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	got = evaluate(context.Background(), cfg, []string{"the change is built on base 0123456"}, measured())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" || *hits != 0 {
		t.Errorf("a commit this bundle does not name is not a fact it carries: got %+v, %d judge calls", got, *hits)
	}
}

func TestAPathOnlyCriterionIsWarnedAndStillJudged(t *testing.T) {
	cfg, hits, asked := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99},"criterion_2":{"noul":0.97}}}`)
	criteria := []string{
		"the file internal/roombudget/roombudget.go is fixed",
		"the acceptance command `go test -count=1 ./...` exits 0, measured from base b32369c, and the numstat " +
			"names rust/agenthub-runtime/tests/seat_guard.rs",
	}
	got := evaluate(context.Background(), cfg, criteria, measured())
	if got.Verdict != clientjev.Accept || got.Evaluator != "jev" || *hits != 1 {
		t.Fatalf("the warning is not a refusal: got %+v, %d judge calls", got, *hits)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "criterion_1") {
		t.Fatalf("the path-only criterion is the one warned, got %q", got.Warnings)
	}
	if strings.Contains(got.Warnings[0], "criterion_2") {
		t.Errorf("criterion_2 names the command, its exit code and a sha, so it must not be warned: %q", got.Warnings[0])
	}
	for _, want := range []string{"test command", "exit code", "sha", "counts"} {
		if !strings.Contains(got.Warnings[0], want) {
			t.Errorf("the warning must name the admissible form (%q is missing): %s", want, got.Warnings[0])
		}
	}
	// The judged text went out with its only fact redacted: that is what the warning is about.
	shaped := ""
	for _, instruction := range *asked {
		if strings.Contains(instruction, "is fixed") {
			shaped = instruction
		}
	}
	if !strings.Contains(shaped, "<path>") {
		t.Errorf("the path-only criterion reached the judge unredacted (%q): the warning's premise is the wire", shaped)
	}
}

func TestAShaAndCountCriterionIsNotWarned(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.97}}}`)
	got := evaluate(context.Background(), cfg, []string{"measured from base b32369c, the numstat names 2 files"}, measured())
	if got.Verdict != clientjev.Accept || *hits != 1 || len(got.Warnings) != 0 {
		t.Errorf("a sha+count criterion is readable, so no warning: got %+v, %d judge calls", got, *hits)
	}
}

// stateServer is jevServer that also records the `state` object of every request, so a fixture can pin the
// counts the judge is shown for its diff rather than trusting the counting code on another body.
func stateServer(t *testing.T, reply string) (clientjev.Config, *int, *[]map[string]any) {
	t.Helper()
	hits := 0
	states := []map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		var payload map[string]json.RawMessage
		var state map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		if raw, ok := payload["state"]; ok {
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Errorf("state is not an object: %v", err)
			}
			states = append(states, state)
		}
		w.WriteHeader(http.StatusOK)
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
	return cfg, &hits, &states
}

// The two closures review 2 line 1 names, settled as fixtures: a merge commit whose only change is a one-line
// gitlink pin (the review's 634ac5c9 shape) and an ordinary two-file docs diff (the review's a669935d shape).
// Neither commit is in a repository on this machine, so the shas are the review's own prefixes and the counts
// are the shapes it measured; what the test pins is that the sha+counts criterion is ADMITTED, that the
// counts the criterion names are the counts the judge is shown, and that the measured band's answer accepts.
func TestAMergeGitlinkDiffAndATwoFileDocsDiffAreAdmitted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      clientevidence.Body
		command   string
		criterion string
		files     float64
		lines     float64
	}{
		{
			name: "a merge commit's gitlink pin",
			body: clientevidence.Body{
				BaseSHA: "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
				HeadSHA: "634ac5c9e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8",
				Numstat: "1\t1\tagenthub_client\n",
			},
			command:   "go test ./...",
			criterion: "the merge 634ac5c9 changes 1 file and 2 lines, the agenthub_client pin, and its test run exits 0",
			files:     1,
			lines:     2,
		},
		{
			name: "a two-file docs diff",
			body: clientevidence.Body{
				BaseSHA: "216a89d1f0e9d8c7b6a5f4e3d2c1b0a9f8e7d6c5",
				HeadSHA: "a669935d4c3b2a1f0e9d8c7b6a5f4e3d2c1b0a9f",
				Numstat: "14\t0\tai_docs/agent-system/task-workflow-and-reporting.md\n7\t0\tai_docs/agent-system/seat-model-and-mcp-surface.md\n",
			},
			command:   "go test ./...",
			criterion: "the branch a669935d changes 2 files and 21 lines of ai_docs",
			files:     2,
			lines:     21,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			body.Test.Command = tc.command
			cfg, hits, states := stateServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
			got := evaluate(context.Background(), cfg, []string{tc.criterion}, body)
			if got.Verdict != clientjev.Accept || got.Evaluator != "jev" || *hits != 1 {
				t.Fatalf("a sha+counts criterion on this diff is a bundle fact, so it is judged: got %+v, %d calls",
					got, *hits)
			}
			if len(got.Warnings) != 0 {
				t.Errorf("the criterion names a sha, so it is not judged blind: %q", got.Warnings)
			}
			if len(*states) != 1 {
				t.Fatalf("the judge was shown %d states, want the one counts summary", len(*states))
			}
			state := (*states)[0]
			if state["files_changed"] != tc.files || state["lines_changed"] != tc.lines {
				t.Errorf("state counts = %v files / %v lines, want %v / %v: the criterion names numbers the judge "+
					"must read", state["files_changed"], state["lines_changed"], tc.files, tc.lines)
			}
		})
	}
}

// The review's other half: widening the vocabulary must not widen the verdict, so a criterion that names only
// a count - no command, no exit code, no path, no sha - is still refused at entry and never reaches the judge.
func TestACountOnlyCriterionIsRefusedAtEntry(t *testing.T) {
	cfg, hits, asked := stateServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	got := evaluate(context.Background(), cfg, []string{"the diff is fewer than 80 lines"}, measured())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" {
		t.Fatalf("a count alone is not a fact the bundle carries: got %+v, %d calls", got, *hits)
	}
	if *hits != 0 {
		t.Errorf("Jev was called for a count-only criterion: %v", *asked)
	}
	if !strings.Contains(got.Reasons, "fewer than 80 lines") {
		t.Errorf("the refusal must name the criterion it refused:\n%s", got.Reasons)
	}
}

func TestOneRefusedCriterionRefusesTheCompletionEvenWhenTheOthersWouldAccept(t *testing.T) {
	cfg, hits, asked := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99},"criterion_3":{"noul":0.99}}}`)
	criteria := []string{
		"the acceptance command `go test -count=1 ./...` exits 0",
		"the panel renders the pending row on production",
		"the numstat names internal/seatpark/park.go",
	}
	got := evaluate(context.Background(), cfg, criteria, measured())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" {
		t.Fatalf("one refused criterion must refuse the task: got %+v", got)
	}
	if *hits != 0 {
		t.Errorf("Jev was called %d times for %v: a refused criterion makes the completion unjudgeable, it does "+
			"not leave the rest to be scored", *hits, *asked)
	}
	for _, want := range []string{`criterion_2 "the panel renders the pending row on production"`, "criterion_1", "criterion_3"} {
		if !strings.Contains(got.Reasons, want) {
			t.Errorf("the refusal must name both sets (%q is missing):\n%s", want, got.Reasons)
		}
	}
}

func TestCriteriaVerdicts(t *testing.T) {
	// The judge's verdict mapping is this test's subject, so the criteria are the register's own shape: a
	// criterion the bundle carries no fact about never reaches the judge (TestACriterionIsJudgedOnly…).
	judged := []string{"the numstat names src/a.go", "the run exits 0"}
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
		if got := evaluate(context.Background(), cfg, judged, passing()); got.Verdict != tc.want || got.Evaluator != "jev" {
			t.Errorf("%s: got %+v, want %s", tc.reply, got, tc.want)
		}
	}
	cfg, _ := jevConfig(t, 500, "boom")
	if got := evaluate(context.Background(), cfg, judged[:1], passing()); got.Verdict != clientjev.Uncertain {
		t.Errorf("an evaluator error must be uncertain, got %+v", got)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	cfg, _ = clientjev.FromEnv()
	if got := evaluate(context.Background(), cfg, judged[:1], passing()); got.Verdict != clientjev.Uncertain {
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
	// Faults the consequence tests inject; a zero status means the normal answer. They are set on the
	// returned record before run starts, and the handler reads them under mu.
	evidenceStatus int
	completeStatus int
	taskStatus     string // the task read's status; empty answers in_progress
	readBackFails  bool   // the GET after the complete POST answers 500: the state cannot be read
	completeAsked  bool
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
			if rec.completeAsked && rec.readBackFails {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"detail":"state unavailable"}`)
				return
			}
			status := rec.taskStatus
			if status == "" {
				status = "in_progress"
			}
			_, _ = io.WriteString(w, `{"success":true,"task":{"status":"`+status+`","acceptance_criteria":`+criteria+`}}`)
		case strings.HasSuffix(r.URL.Path, "/evidence"):
			if rec.evidenceStatus != 0 {
				w.WriteHeader(rec.evidenceStatus)
				_, _ = io.WriteString(w, `{"detail":"injected evidence fault"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"event":"stored"}`)
		default:
			rec.completeAsked = true
			if rec.completeStatus != 0 {
				w.WriteHeader(rec.completeStatus)
				_, _ = io.WriteString(w, `{"detail":"injected completion fault"}`)
				return
			}
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
	rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "true")
	code, out, errs := run("--task", "t1", "--base", base, "--summary", "done it", "--notes", "ran tests", "--test", "git --version")
	if code != clientcmd.ExitOK || !strings.Contains(out, `"completed":true`) {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	// FOUR calls, and the third is the done report's row read: a STORED submission prints the row it closes,
	// so the evidence step reads it back before complete is asked.
	want := []string{"GET /api/v2/tasks/t1", "POST /api/v2/tasks/t1/evidence", "GET /api/v2/tasks/t1", "POST /api/v2/tasks/t1/complete"}
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
	rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
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
	rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.1}}}`)
	t.Setenv(enforcementEnv, "")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	// FOUR calls: the task read, the evidence, the done report's row read, and complete.
	if code != clientcmd.ExitOK || !strings.Contains(errs, "gate (shadow) reject") || len(rec.calls) != 4 {
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

func TestPathOnlyCriterionWarnsOnStderrAndStillCompletes(t *testing.T) {
	base := repo(t)
	taskServer(t, `["the file a.txt is changed"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitOK || !strings.Contains(errs, "warning: criterion_1") || !strings.Contains(errs, "judged blind") {
		t.Errorf("the warning is a preflight of the verdict, not a refusal: exit %d err %q", code, errs)
	}
	if !strings.Contains(errs, "gate (shadow) accept") {
		t.Errorf("the criterion was still judged: err %q", errs)
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

// The register's exit-code fact is a whole number, not a substring, and the numbers a criterion is likely to
// carry next to it are decimals: 0 must not be found inside 10 or inside 0.98, or a criterion could pass its
// register check on a number that is not this run's exit code.
func TestTheRegisterFindsAWholeNumberAndNotASubstring(t *testing.T) {
	cases := []struct {
		text, n string
		want    bool
	}{
		{"the run exits 0", "0", true},
		{"exit code 0.", "0", true},
		{"the run exits 10", "10", true},
		{"the run exits 100", "10", false},
		{"the run exits 10", "0", false},
		{"the score is 0.98", "0", false},
		{"the run exits 0.0", "0", false},
	}
	for _, c := range cases {
		if got := containsInteger(c.text, c.n); got != c.want {
			t.Errorf("containsInteger(%q, %q) = %v, want %v", c.text, c.n, got, c.want)
		}
	}
}

// measured() carries 4 files, 45 added and 6 deleted. A count is a fact only when the criterion spells one
// of the four wire keys AND the number in that key's own clause is the bundle's number for it; anything else
// is still a claim the register cannot see.
func TestACountKeyWithTheBundlesNumberIsAFact(t *testing.T) {
	for _, tc := range []struct {
		criterion string
		admitted  bool
	}{
		{"files_changed is 4", true},
		{"FILES_CHANGED is 4", true},
		{"files changed is 4", true},
		{"files_changed across the whole change is 4", true},
		{"lines_changed is 51", true},
		{"lines_added is 45 and lines_deleted is 6", true},
		{"between base b32369c and the tip, files_changed is 4", true},
		{"files_changed is 3", false},
		{"lines added is 46", false},
		{"files_changed is 51", false},
		{"the numstat names 2 files", false},
		{"the diff is fewer than 80 lines", false},
	} {
		t.Run(tc.criterion, func(t *testing.T) {
			cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
			got := evaluate(context.Background(), cfg, []string{tc.criterion}, measured())
			if tc.admitted {
				if got.Verdict != clientjev.Accept || got.Evaluator != "jev" || *hits != 1 {
					t.Fatalf("a count key with this bundle's value is a fact: got %+v, %d calls", got, *hits)
				}
				return
			}
			if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" || *hits != 0 {
				t.Fatalf("a count this bundle does not carry is refused, not judged: got %+v, %d calls", got, *hits)
			}
			if !strings.Contains(got.Reasons, "files_changed") {
				t.Errorf("the refusal must name the count keys the bundle carries:\n%s", got.Reasons)
			}
		})
	}
}

// The measured collision (board Progress 5): the sha tokenizer splits "2c34515" into 2 and 34515, so a match
// anywhere in the text would settle "files_changed is 2" on a three-file change. The key's own clause must
// bind its own number. The fragment must NOT be one of the bundle's shas - a criterion citing a real sha is
// admitted by that fact, as it always was, and the wrong count is then the judge's to read.
func TestAShaTokenCannotSettleACount(t *testing.T) {
	body := clientevidence.Body{
		BaseSHA: "b32369c1f4a2b3c4d5e6f708192a3b4c5d6e7f80",
		HeadSHA: "d5c9b5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8",
		Numstat: "1\t0\ta.go\n1\t0\tb.go\n1\t0\tc.go\n",
	}
	body.Test.Command = "go test ./..."
	for criterion, admitted := range map[string]bool{
		"between 2c34515 and 216a89d, files_changed is 2": false,
		"between 2c34515 and 216a89d, files_changed is 3": true,
	} {
		cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
		got := evaluate(context.Background(), cfg, []string{criterion}, body)
		if admitted != (got.Verdict == clientjev.Accept) || *hits != map[bool]int{true: 1, false: 0}[admitted] {
			t.Errorf("%q: got %+v, %d judge calls; admitted=%v", criterion, got, *hits, admitted)
		}
	}
	// The facts stand on their own: the same wrong count, with the bundle's real sha cited, is admitted by
	// the sha (as it was before counts existed) and the judge is the one who reads the number.
	realSha := body
	realSha.BaseSHA = "2c34515a1b2c3d4e5f60718293a4b5c6d7e8f9a0"
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	if got := evaluate(context.Background(), cfg,
		[]string{"between 2c34515 and the tip, files_changed is 2"}, realSha); got.Verdict != clientjev.Accept || *hits != 1 {
		t.Errorf("a real sha is a fact on its own: got %+v, %d judge calls", got, *hits)
	}
}

// Zero is a carried value, not an empty count: a binary-only change moves 0 lines, and the judge must be
// able to read that.
func TestAZeroCountIsACarriedValue(t *testing.T) {
	body := clientevidence.Body{Numstat: "-\t-\tlogo.png\n"}
	body.Test.Command = "go build ./..."
	cfg, hits, states := stateServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	got := evaluate(context.Background(), cfg, []string{"lines_changed 0 and files_changed is 1"}, body)
	if got.Verdict != clientjev.Accept || *hits != 1 {
		t.Fatalf("zero is a carried count: got %+v", got)
	}
	if state := (*states)[0]; state["lines_changed"] != float64(0) || state["files_changed"] != float64(1) {
		t.Errorf("state = %v, want lines_changed 0 and files_changed 1", state)
	}
}

// The warning's premise is that a path is the criterion's ONLY readable fact. A criterion that also states a
// count now has one, so it is not judged blind and must not be warned.
func TestAPathPlusCountCriterionIsNotWarned(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	got := evaluate(context.Background(), cfg,
		[]string{"the file internal/seatpark/park.go changed and files_changed is 4"}, measured())
	if got.Verdict != clientjev.Accept || *hits != 1 || len(got.Warnings) != 0 {
		t.Errorf("a path plus this bundle's count is readable, so no warning: got %+v, %d calls", got, *hits)
	}
}

// TestEvidenceNotStoredNamesTheTaskUnchanged is the consequence line for a refused submission: the
// gate verdict is already on stderr, and the seat must be told the task is still open WITHOUT any
// completion call. The status and the hub body stay on the `4genteam evidence:` lines only.
func TestEvidenceNotStoredNamesTheTaskUnchanged(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
	rec.evidenceStatus = http.StatusInternalServerError
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "true")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d; stderr %q", code, clientcmd.ExitRemote, errs)
	}
	if !strings.Contains(errs, "the task was not completed: the evidence was not stored; task t1 is unchanged") {
		t.Fatalf("the seat is not told the consequence: %q", errs)
	}
	for _, call := range rec.calls {
		if strings.HasSuffix(call, "/complete") {
			t.Fatalf("a completion was asked after the evidence was refused: calls %v", rec.calls)
		}
	}
	if !strings.Contains(errs, "HTTP 500") {
		t.Fatalf("the evidence status must stay on its own line: %q", errs)
	}
}

// TestEvidenceConflictStopsTheCompletion is the 409's meaning after the hub made an identical replay a
// 200: a 409 means the task's latest evidence for this head_sha is a DIFFERENT account of it, so the
// completion must not run - the judge would read evidence this run never computed its verdict on.
func TestEvidenceConflictStopsTheCompletion(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
	rec.evidenceStatus = http.StatusConflict
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "true")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d; stderr %q", code, clientcmd.ExitRemote, errs)
	}
	if !strings.Contains(errs, "the task was not completed: the evidence was not stored; task t1 is unchanged") {
		t.Fatalf("the consequence is not named: %q", errs)
	}
	for _, call := range rec.calls {
		if strings.HasSuffix(call, "/complete") {
			t.Fatalf("a 409 must not reach the completion: %v", rec.calls)
		}
	}
}

// TestCompletionFailureReadsTheStateBack names the case a seat cannot resolve from the exit code: the
// POST may or may not have written, so the verb reads the task once and says what it reads.
func TestCompletionFailureReadsTheStateBack(t *testing.T) {
	cases := map[string]struct {
		taskStatus string
		want       string
	}{
		"still open":   {"in_progress", "the task was not completed: the completion request failed and task t1 reads in_progress"},
		"already done": {"done", "the completion request failed, but task t1 reads done: it completed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			base := repo(t)
			rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
			rec.completeStatus = http.StatusInternalServerError
			rec.taskStatus = c.taskStatus
			jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
			t.Setenv(enforcementEnv, "true")
			code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
			if code != clientcmd.ExitRemote {
				t.Fatalf("exit %d, want %d; stderr %q", code, clientcmd.ExitRemote, errs)
			}
			if !strings.Contains(errs, c.want) {
				t.Fatalf("stderr %q does not carry %q", errs, c.want)
			}
			if last := rec.calls[len(rec.calls)-1]; last != "GET /api/v2/tasks/t1" {
				t.Fatalf("last call = %q, want the read-back GET after the failed completion: %v", last, rec.calls)
			}
		})
	}
}

// TestCompletionFailureWithAnUnreadableStateNamesTheError: the read-back's own failure is reported as
// such, so "the state could not be read" is never mistaken for "the task is open".
func TestCompletionFailureWithAnUnreadableStateNamesTheError(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
	rec.completeStatus = http.StatusInternalServerError
	rec.readBackFails = true
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "true")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit %d, want %d; stderr %q", code, clientcmd.ExitRemote, errs)
	}
	if !strings.Contains(errs, "the task was not completed: the completion request failed and the state could not be read: reading the task: the server answered HTTP 500") {
		t.Fatalf("stderr %q does not name the unreadable state", errs)
	}
}
