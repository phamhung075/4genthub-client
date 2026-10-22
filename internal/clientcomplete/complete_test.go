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
		{"a path the numstat lists", "the numstat names internal/seatpark/park.go", true},
		{"a path plus this run's exit code", "the run exits 0 and internal/seatpark/park.go carries the change", false},
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

// A path is the one fact the wire takes away, so a criterion whose only fact is a path is REFUSED here, before
// the judge is asked: the same refusal the verb used to print as advice, with the vocabulary the wire does
// carry. The sibling criterion names the command, its exit code and a sha - it keeps a fact after redaction and
// is judged.
func TestAPathOnlyCriterionIsRefusedBeforeTheJudge(t *testing.T) {
	cfg, hits, asked := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99},"criterion_2":{"noul":0.97}}}`)
	criteria := []string{
		"the file internal/roombudget/roombudget.go is fixed",
		"the acceptance command `go test -count=1 ./...` exits 0, measured from base b32369c, and the numstat " +
			"names rust/agenthub-runtime/tests/seat_guard.rs",
	}
	got := evaluate(context.Background(), cfg, criteria, measured())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" {
		t.Fatalf("a path-only criterion is refused, not judged: got %+v", got)
	}
	if *hits != 0 || len(*asked) != 0 {
		t.Fatalf("Jev was called %d times (%v) for a criterion the gate knows is judged blind", *hits, *asked)
	}
	if !strings.Contains(got.Reasons, `criterion_1 "the file internal/roombudget/roombudget.go is fixed"`) {
		t.Errorf("the refusal must name the blind criterion by number:\n%s", got.Reasons)
	}
	if strings.Contains(got.Reasons, `criterion_2 "`) {
		t.Errorf("criterion_2 names the command, its exit code and a sha, so it must not be refused:\n%s", got.Reasons)
	}
	for _, want := range []string{"test command", "exit code", "sha", "counts", "judgeable: criterion_2"} {
		if !strings.Contains(got.Reasons, want) {
			t.Errorf("the refusal must name the admissible form (%q is missing):\n%s", want, got.Reasons)
		}
	}
	// The measured evidence stays in the message: the register's own paths and counts.
	for _, want := range []string{"internal/seatpark/park.go", "files_changed 4", "lines_added 45"} {
		if !strings.Contains(got.Reasons, want) {
			t.Errorf("the refusal must print the measured register (%q is missing):\n%s", want, got.Reasons)
		}
	}
}

func TestAShaAndCountCriterionIsJudged(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.97}}}`)
	got := evaluate(context.Background(), cfg, []string{"measured from base b32369c, the numstat names 2 files"}, measured())
	if got.Verdict != clientjev.Accept || *hits != 1 {
		t.Errorf("a sha+count criterion is readable, so it is judged: got %+v, %d judge calls", got, *hits)
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
	judged := []string{"the numstat names src/a.go and lines_changed is 4", "the run exits 0"}
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

// canonicalTaskID is the id the fake hub answers for any task read, as the real one does: a caller may
// name a prefix, and every route that WRITES takes the row's own uuid.
const canonicalTaskID = "403d16ca-1a2b-4c3d-8e4f-5a6b7c8d9e0f"

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
			_, _ = io.WriteString(w, `{"success":true,"task":{"id":"`+canonicalTaskID+`","status":"`+status+`","acceptance_criteria":`+criteria+`}}`)
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
	want := []string{"GET /api/v2/tasks/t1", "POST /api/v2/tasks/" + canonicalTaskID + "/evidence",
		"GET /api/v2/tasks/" + canonicalTaskID, "POST /api/v2/tasks/" + canonicalTaskID + "/complete"}
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

// A path-only criterion is refused by the verb itself: enforced, it stops before any server write; in shadow
// it records the verdict and completes, exactly as any other non-accept verdict does.
func TestPathOnlyCriterionRefusesTheCompletion(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, `["the file a.txt is changed"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "true")
	code, out, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	var verdict result
	if err := json.Unmarshal([]byte(out), &verdict); err != nil || code != ExitGateRefused ||
		verdict.Verdict != clientjev.Unjudgeable {
		t.Fatalf("a path-only criterion must stop the completion: exit %d out %q err %q", code, out, errs)
	}
	if !strings.Contains(errs, `criterion_1 "the file a.txt is changed"`) ||
		!strings.Contains(errs, "a carried path says only that the file is in the diff") ||
		!strings.Contains(errs, "pair it with its count") {
		t.Errorf("the refusal must name the criterion and the fact it needs: err %q", errs)
	}
	if !strings.Contains(errs, "the task was not completed") {
		t.Errorf("the enforced refusal must say the task was not completed: err %q", errs)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "GET /api/v2/tasks/t1" {
		t.Errorf("calls = %v, want only the task read", rec.calls)
	}
}

func TestPathOnlyCriterionInShadowRecordsAndCompletes(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, `["the file a.txt is changed"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitOK || !strings.Contains(errs, "gate (shadow) unjudgeable") || len(rec.calls) != 4 {
		t.Errorf("exit %d err %q calls %v", code, errs, rec.calls)
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

// The parser used to keep the last of a repeated flag and drop the first silently: the dropped --test then made
// every criterion quoting it unjudgeable with "names no fact this bundle carries" - a refusal about a command the
// seat did pass. A second occurrence of any flag is a mistake the verb can see, so it is a usage error and no
// request leaves.
func TestARepeatedFlagIsRefusedBeforeAnyRequest(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, `[]`)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--task", "t1", "--base", base, "--summary", "s", "--test", "go test ./a", "--test", "go test ./b"},
			"--test given twice"},
		{[]string{"--task", "t1", "--task", "t2", "--base", base, "--summary", "s"}, "--task given twice"},
	} {
		code, _, errs := run(tc.args...)
		if code != clientcmd.ExitUsage || !strings.Contains(errs, tc.want) {
			t.Errorf("%v: exit %d stderr %q, want a usage error naming %q", tc.args, code, errs, tc.want)
		}
	}
	if len(rec.calls) != 0 {
		t.Errorf("a repeated flag reached the server: %v", rec.calls)
	}
}

// The rule is about the SECOND occurrence: one of each flag still parses as before.
func TestEachFlagOnceStillParses(t *testing.T) {
	opts, help, err := parse([]string{"--task", "t1", "--base", "b", "--summary", "s", "--test", "go test ./...",
		"--notes", "n", "--url", "http://x"})
	if err != nil || help {
		t.Fatalf("one of each flag must parse: help %v, err %v", help, err)
	}
	if want := (options{task: "t1", base: "b", summary: "s", test: "go test ./...", notes: "n", url: "http://x"}); opts != want {
		t.Errorf("got %+v, want %+v", opts, want)
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

// A path plus a readable fact is admitted: the count survives the wire, so the claim is not judged blind.
func TestAPathPlusCountCriterionIsJudged(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	got := evaluate(context.Background(), cfg,
		[]string{"the file internal/seatpark/park.go changed and files_changed is 4"}, measured())
	if got.Verdict != clientjev.Accept || *hits != 1 {
		t.Errorf("a path plus this bundle's count is readable, so it is judged: got %+v, %d calls", got, *hits)
	}
}

// A criterion naming a carried path reaches the judge as path#N - the row's position in WireRows order, the
// same sequence changes[] is in - with that row's counts. The file itself does not cross (O9a stands).
func TestACarriedPathReachesTheJudgeAsItsRowNumber(t *testing.T) {
	cfg, hits, states := stateServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	// measured()'s rows in WireRows order: seat_guard.rs 20/0, roombudget.go 12/3, park.go 8/2, seat-guard.ts 5/1.
	got := evaluate(context.Background(), cfg,
		[]string{"the numstat names internal/roombudget/roombudget.go and lines_changed is 51"}, measured())
	if got.Verdict != clientjev.Accept || *hits != 1 {
		t.Fatalf("a path plus a count is judgeable: got %+v, %d calls", got, *hits)
	}
	if len(*states) != 1 {
		t.Fatalf("the judge was shown %d states, want 1", len(*states))
	}
	resolved, ok := (*states)[0]["resolved"].(map[string]any)
	if !ok {
		t.Fatalf("state has no resolved map: %v", (*states)[0])
	}
	entry, _ := resolved["criterion_1"].([]any)
	want := []string{"path#2 (added 12, deleted 3)", "lines_changed 51"}
	if len(entry) != len(want) {
		t.Fatalf("resolved = %v, want %v", entry, want)
	}
	for i, w := range want {
		if entry[i] != w {
			t.Errorf("resolved[%d] = %v, want %q", i, entry[i], w)
		}
	}
}

// The privacy half of the ruling: the encoded request carries the row number and the counts and NONE of the
// carried paths, so a judge settling a file criterion cannot read the file's name.
func TestTheGateWireCarriesNoCarriedPath(t *testing.T) {
	cfg, bodies := rawServer(t, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	got := evaluate(context.Background(), cfg,
		[]string{"the numstat names internal/roombudget/roombudget.go and lines_changed is 51"}, measured())
	if got.Verdict != clientjev.Accept || len(*bodies) != 1 {
		t.Fatalf("got %+v, %d request bodies", got, len(*bodies))
	}
	body := (*bodies)[0]
	for _, path := range []string{"roombudget", "seatpark", "seat_guard", "seat-guard"} {
		if strings.Contains(body, path) {
			t.Errorf("the request carries %q:\n%s", path, body)
		}
	}
	if !strings.Contains(body, "path#2 (added 12, deleted 3)") {
		t.Errorf("the request does not name the resolved row:\n%s", body)
	}
}

// A path-only criterion keeps its verdict - a path alone says only that the file is in the diff - and the
// refusal now teaches the counts that settle it instead of the redaction that used to hide it.
func TestAPathOnlyCriterionIsRefusedWithTheCountsThatSettleIt(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	got := evaluate(context.Background(), cfg,
		[]string{"the file internal/roombudget/roombudget.go is fixed"}, measured())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" || *hits != 0 {
		t.Fatalf("got %+v, %d judge calls; want a deterministic refusal", got, *hits)
	}
	for _, want := range []string{"a carried path says only that the file is in the diff",
		"pair it with its count (lines_changed 51, lines_added 45, lines_deleted 6)",
		"the command's exit code 0", "a sha prefix"} {
		if !strings.Contains(got.Reasons, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, got.Reasons)
		}
	}
}

// A path this bundle does not carry stays SILENT: the key resolves only carried rows, so a criterion
// inventing a file cannot become judgeable through it.
func TestACriterionNamingAnUncarriedPathIsStillSilent(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	got := evaluate(context.Background(), cfg,
		[]string{"the file internal/nowhere/missing.go is fixed"}, measured())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" || *hits != 0 {
		t.Fatalf("an uncarried path is not a fact: got %+v, %d judge calls", got, *hits)
	}
	if !strings.Contains(got.Reasons, `criterion_1 "the file internal/nowhere/missing.go is fixed" names no fact this bundle carries`) {
		t.Errorf("the silent claim must be named as silent:\n%s", got.Reasons)
	}
	if strings.Contains(got.Reasons, "names only a carried path") {
		t.Errorf("an uncarried path is SILENT, not a blind carried one:\n%s", got.Reasons)
	}
}

// rawServer is stateServer that keeps each request's bytes, for the assertions whose subject is the encoded
// payload rather than its parse.
func rawServer(t *testing.T, reply string) (clientjev.Config, *[]string) {
	t.Helper()
	bodies := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
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
	return cfg, &bodies
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
	if !strings.Contains(errs, "the task was not completed: the evidence was not stored; task "+canonicalTaskID+" is unchanged") {
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

// A prefix id is resolved by the READ route only: the row answers its own uuid, and every route that
// WRITES must name that uuid, because the evidence route casts the id as a uuid and answered HTTP 500
// for an 8-hex one - after the gate had already run the whole test command (measured 2026-10-22,
// fe-dev's row qitem-20261022194113: GET /tasks/403d16ca/events and POST /tasks/403d16ca/evidence 500,
// SQLSTATE 22P02).
func TestAPrefixIdWritesUnderTheRowsOwnUuid(t *testing.T) {
	base := repo(t)
	rec := taskServer(t, "[\"the acceptance command `git --version` exits 0\"]")
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "true")
	code, out, errs := run("--task", "403d16ca", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitOK || !strings.Contains(out, `"completed":true`) {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	// The read names what the caller passed; every later call names the row's own id.
	want := []string{"GET /api/v2/tasks/403d16ca", "POST /api/v2/tasks/" + canonicalTaskID + "/evidence",
		"GET /api/v2/tasks/" + canonicalTaskID, "POST /api/v2/tasks/" + canonicalTaskID + "/complete"}
	if strings.Join(rec.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v\nwant  %v", rec.calls, want)
	}
}

// The event a bad id must never produce: a write under the caller's prefix. It is the same assertion as
// above seen from the other side, and it fails at 71ef6aab, where the evidence POST carried the prefix.

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
	if !strings.Contains(errs, "the task was not completed: the evidence was not stored; task "+canonicalTaskID+" is unchanged") {
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
		"still open":   {"in_progress", "the task was not completed: the completion request failed and task " + canonicalTaskID + " reads in_progress"},
		"already done": {"done", "the completion request failed, but task " + canonicalTaskID + " reads done: it completed"},
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
			if last := rec.calls[len(rec.calls)-1]; last != "GET /api/v2/tasks/"+canonicalTaskID {
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

// commitHere records an empty commit on the repository repo(t) made current, and returns its sha: a commit
// the evidence bundle will NOT carry as base or head, which is what a mismatched criterion names.
func commitHere(t *testing.T, message string) string {
	t.Helper()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("commit", "-q", "--allow-empty", "-m", message)
	return git("rev-parse", "HEAD")
}

// TestACriterionNamingAnotherBaseWarnsBeforeTheGate is the lead's measured failure (task c898d908): a
// criterion naming the branch commit's parent while --base named the parent's parent. The gate refuses it
// unjudgeable AFTER the reviewer passed; the verb knows --base when it reads it, so it says so first, one
// line per criterion, and the exit code is unchanged.
func TestACriterionNamingAnotherBaseWarnsBeforeTheGate(t *testing.T) {
	base := repo(t)                   // the base commit, and HEAD one change on top
	foreign := commitHere(t, "named") // a commit the bundle carries neither as base nor as head
	head := commitHere(t, "the head") // what --base..HEAD will actually measure
	_ = head
	taskServer(t, `["the acceptance command `+"`git --version`"+` exits 0; see `+foreign+`",
		"the acceptance command `+"`git --version`"+` exits 0"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95},"criterion_2":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitOK {
		t.Fatalf("the warning must not change the exit code: exit %d, stderr %q", code, errs)
	}
	want := "warning: criterion_1 names " + foreign + ", --base is " + base + "; the gate measures " + base + "..HEAD"
	if !strings.Contains(errs, want) {
		t.Fatalf("stderr %q does not carry %q", errs, want)
	}
	if strings.Contains(errs, "warning: criterion_2 names") {
		t.Errorf("only the criterion naming another base is warned: stderr %q", errs)
	}
}

// TestACriterionNamingTheBaseOrTheHeadIsNotWarned: both shas are diff facts the bundle carries, so a
// criterion citing either is measuring what the gate measures.
func TestACriterionNamingTheBaseOrTheHeadIsNotWarned(t *testing.T) {
	base := repo(t)
	head := strings.TrimSpace(func() string {
		out, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}())
	for _, sha := range []string{base, head} {
		t.Run(sha[:7], func(t *testing.T) {
			taskServer(t, `["the acceptance command `+"`git --version`"+` exits 0, base `+sha+`"]`)
			jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
			t.Setenv(enforcementEnv, "")
			code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
			if code != clientcmd.ExitOK {
				t.Fatalf("exit %d, stderr %q", code, errs)
			}
			if strings.Contains(errs, "names "+sha) {
				t.Errorf("a criterion naming %s is not warned: stderr %q", sha[:7], errs)
			}
		})
	}
}

// TestAHexWordThatNamesNoCommitIsNotABase: a 7+ hex run is only a candidate; a token this repository
// cannot resolve to a commit names no base, so there is nothing to warn about.
func TestAHexWordThatNamesNoCommitIsNotABase(t *testing.T) {
	base := repo(t)
	taskServer(t, `["the acceptance command `+"`git --version`"+` exits 0 at deadbeef1"]`)
	jevConfig(t, 200, `{"answers":{"criterion_1":{"noul":0.95}}}`)
	t.Setenv(enforcementEnv, "")
	code, _, errs := run("--task", "t1", "--base", base, "--summary", "s", "--test", "git --version")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if strings.Contains(errs, "names ") {
		t.Errorf("a token that resolves to no commit is not warned: stderr %q", errs)
	}
}

// A silent criterion that names a test is the case the wire cannot carry: the gate names the replacement -
// the narrow command to pass as --test - instead of leaving the seat to invent it.
func TestATestNameCriterionPrintsTheCommandThatReplacesIt(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	got := evaluate(context.Background(), cfg, []string{"TestListenerStartsForEverySeat passes"}, passing())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" || *hits != 0 {
		t.Fatalf("a test name is not a fact the bundle carries: got %+v, %d judge calls", got, *hits)
	}
	for _, want := range []string{
		`criterion_1 "TestListenerStartsForEverySeat passes"`,
		"a test name reaches the judge as <test>",
		"pass that same command as --test",
		`go test <pkg> -run '^TestListenerStartsForEverySeat$' -count=1`,
	} {
		if !strings.Contains(got.Reasons, want) {
			t.Errorf("the refusal must print the form that replaces a test name (%q is missing):\n%s", want, got.Reasons)
		}
	}
}

// The advice must print a selector that RUNS something. A package-qualified name and a comma-separated list
// both fail as a -run pattern (nothing matches, go test exits 0), so the printed command must carry the bare
// name: -run '^a.TestFoo$' and -run '^TestFoo,TestBar$' are the shapes this refuses to teach.
func TestTheAdvicePrintsTheNameAndNotTheField(t *testing.T) {
	for _, tc := range []struct {
		criterion string
		want      string
		notWant   string
	}{
		{"a.TestFoo passes", `-run '^TestFoo$'`, `-run '^a.TestFoo$'`},
		{"TestFoo,TestBar pass", `-run '^TestFoo$'`, `'^TestFoo,TestBar$'`},
		{"TestFoo-bar is covered", `-run '^TestFoo$'`, `'^TestFoo-bar$'`},
	} {
		cfg, _, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
		got := evaluate(context.Background(), cfg, []string{tc.criterion}, passing())
		if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" {
			t.Fatalf("%q is a silent criterion: got %+v", tc.criterion, got)
		}
		if !strings.Contains(got.Reasons, tc.want) {
			t.Errorf("%q must advise a command that runs (%q is missing):\n%s", tc.criterion, tc.want, got.Reasons)
		}
		if strings.Contains(got.Reasons, tc.notWant) {
			t.Errorf("%q must not advise a selector that matches nothing (%q printed):\n%s",
				tc.criterion, tc.notWant, got.Reasons)
		}
	}
}

// A criterion naming a file whose stem looks like a test name is a <path> on the wire, not a <test>, and the
// stem runs no test: the advice names the file token that CONTAINS the match and never teaches a selector
// built from that stem, which would match nothing and exit 0 - the defect this advice exists to prevent.
func TestTheAdviceNamesTheFileTokenAndTeachesNoSelectorFromIt(t *testing.T) {
	for _, criterion := range []string{"TestHelper.go is fixed", "docs/BenchmarkHelper.go is deleted"} {
		cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
		got := evaluate(context.Background(), cfg, []string{criterion}, passing())
		if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" || *hits != 0 {
			t.Fatalf("%q is a silent criterion: got %+v, %d judge calls", criterion, got, *hits)
		}
		file := strings.Fields(criterion)[0]
		for _, want := range []string{
			file + " is a file name",
			"a file name reaches the judge as <path>",
			"names no fact this bundle carries",
			"`go test <pkg> -count=1` exits 0",
		} {
			if !strings.Contains(got.Reasons, want) {
				t.Errorf("%q: the advice must name the file shape and a command that runs (%q is missing):\n%s",
					criterion, want, got.Reasons)
			}
		}
		for _, notWant := range []string{"-run '^", "-bench '^", "a test name reaches the judge as <test>"} {
			if strings.Contains(got.Reasons, notWant) {
				t.Errorf("%q must not advise a selector built from a file's stem (%q printed):\n%s",
					criterion, notWant, got.Reasons)
			}
		}
	}
}

// The placeholder is decided by the token the advice quotes, not by any file name elsewhere in the criterion:
// a test name beside a path is a <test>, and the selector built from it runs that test.
//
// THE THIRD CASE IS THE POSITIONAL ONE (reviewer's counterexample, 2026-10-22): the sentence names a test AND a
// file that carries the same word in its own name. Asking whether any file token CONTAINS "TestFoo" answered with
// TestFoo_test.go and taught no selector, although the first TestFoo stands free in the sentence and `go test
// -run '^TestFoo$'` measures it - so containment is read off the match's own position.
func TestAPathElsewhereDoesNotMakeATestNameAFile(t *testing.T) {
	for _, criterion := range []string{
		"TestFoo passes; internal/clientjev/jev.go is fixed",
		"TestFoo passes; countsonly.go is fixed",
		"TestFoo passes; TestFoo_test.go is created",
	} {
		cfg, _, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
		got := evaluate(context.Background(), cfg, []string{criterion}, passing())
		if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" {
			t.Fatalf("%q is a silent criterion: got %+v", criterion, got)
		}
		for _, want := range []string{"a test name reaches the judge as <test>", `-run '^TestFoo$'`} {
			if !strings.Contains(got.Reasons, want) {
				t.Errorf("%q quotes the test token, so the advice must say so (%q is missing):\n%s",
					criterion, want, got.Reasons)
			}
		}
		if strings.Contains(got.Reasons, "a file name reaches the judge as <path>") {
			t.Errorf("%q names a path elsewhere, but the quoted token is a test name:\n%s", criterion, got.Reasons)
		}
	}
}

// A benchmark does not run under -run: the advice must print -bench, because `-run '^BenchmarkX$'` matches
// nothing and exits 0, which is the failure this advice exists to prevent.
func TestTheAdviceRunsABenchmarkUnderBench(t *testing.T) {
	cfg, _, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.99}}}`)
	got := evaluate(context.Background(), cfg, []string{"BenchmarkParseIsFaster passes"}, passing())
	if got.Verdict != clientjev.Unjudgeable || got.Evaluator != "deterministic" {
		t.Fatalf("a benchmark name is a silent criterion: got %+v", got)
	}
	if want := `go test <pkg> -bench '^BenchmarkParseIsFaster$' -count=1`; !strings.Contains(got.Reasons, want) {
		t.Errorf("a benchmark needs -bench (%q is missing):\n%s", want, got.Reasons)
	}
	if strings.Contains(got.Reasons, `-run '^BenchmarkParseIsFaster$'`) {
		t.Errorf("a benchmark under -run matches nothing:\n%s", got.Reasons)
	}
}

// The command the advice teaches is itself a register fact: a criterion quoting the narrow command is judged,
// and the advice it replaces is not printed.
func TestACriterionQuotingTheNarrowCommandIsJudged(t *testing.T) {
	cfg, hits, _ := jevServer(t, `{"answers":{"criterion_1":{"noul":0.97}}}`)
	body := passing()
	body.Test.Command = "go test ./internal/clientllm/ -run '^TestListenerStartsForEverySeat$' -count=1"
	got := evaluate(context.Background(), cfg, []string{"`" + body.Test.Command + "` exits 0"}, body)
	if got.Verdict != clientjev.Accept || got.Evaluator != "jev" || *hits != 1 {
		t.Fatalf("a criterion naming the submitted command is judged: got %+v, %d judge calls", got, *hits)
	}
	if strings.Contains(got.Reasons, "a test name reaches the judge as <test>") {
		t.Errorf("a judged criterion must not carry the refusal's advice:\n%s", got.Reasons)
	}
}
