package clientdecide

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
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

const questions = `[{"id":"q1","question":"Which db?","options":[{"id":"pg","label":"PostgreSQL","recommended":true},{"id":"lite","label":"SQLite"}]}]`

type recorded struct {
	hits int
	path string
	auth string
	body map[string]any
}

// jev stands in for TypeSafe System One and points the client at it; the log goes to a temp file.
func jev(t *testing.T, status int, reply string) (*recorded, string) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.hits++
		rec.path, rec.auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.Unmarshal(raw, &rec.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	logPath := filepath.Join(t.TempDir(), "jev_decisions.log")
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	t.Setenv("TYPESAFE_API_KEY", "key-secret")
	t.Setenv("JEV_DECISION_LOG_PATH", logPath)
	t.Setenv("JEV_DECISION_THRESHOLD", "")
	t.Setenv("DECISION_OWNER_ONLY_CATEGORIES", "")
	return rec, logPath
}

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := decideCommand{}.Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no log: %v", err)
	}
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestConfidentAnswerDecidesLocallyAndIsLogged(t *testing.T) {
	rec, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"pg","confidence":0.93}}}`)
	code, out, errs := run("--tier", "seat", "--category", "General", "--summary", "db", "--detail", "pick", "--questions", questions)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if rec.path != "/systemone" || rec.auth != "Bearer key-secret" || rec.body["model"] != "jev-1.13.0" {
		t.Errorf("request = %s auth %q body %v", rec.path, rec.auth, rec.body)
	}
	q1 := rec.body["questions"].(map[string]any)["q1"].(map[string]any)
	if q1["type"] != "choice" || q1["instructions"] != "Which db?" || q1["criteria"].(map[string]any)["pg"] != "PostgreSQL" {
		t.Errorf("question sent = %v", q1)
	}
	var got outcome
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.Decided || got.Answers["q1"] != "pg" || got.Tier != "seat" || got.Questions[0].Recommended != "pg" {
		t.Errorf("stdout %q (%v)", out, err)
	}
	if strings.Contains(out+errs, "key-secret") {
		t.Error("the key leaked")
	}
	entries := readLog(t, logPath)
	if len(entries) != 1 || entries[0]["outcome"] != "decided" || entries[0]["tier"] != "seat" || entries[0]["model"] != "jev-1.13.0" {
		t.Errorf("log = %v", entries)
	}
}

func TestLowConfidenceEscalatesOneTierAndCarriesTheRecommendation(t *testing.T) {
	_, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"lite","confidence":0.5}}}`)
	code, out, _ := run("--tier", "seat", "--summary", "db", "--questions", questions)
	var got outcome
	_ = json.Unmarshal([]byte(out), &got)
	if code != ExitEscalated || got.EscalateTo != "lead" || got.Decided || got.Questions[0].Recommended != "pg" || !strings.Contains(got.Reason, "0.80") {
		t.Errorf("exit %d out %q", code, out)
	}
	code, out, _ = run("--tier", "lead", "--summary", "db", "--questions", questions)
	if code != ExitEscalated || !strings.Contains(out, `"escalate_to":"owner"`) {
		t.Errorf("lead: exit %d out %q", code, out)
	}
	if entries := readLog(t, logPath); len(entries) != 2 || entries[0]["outcome"] != "escalated-to-lead" {
		t.Errorf("log = %v", entries)
	}
}

func TestOwnerOnlyCategoryNeverCallsJev(t *testing.T) {
	rec, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"pg","confidence":0.99}}}`)
	t.Setenv("DECISION_OWNER_ONLY_CATEGORIES", "omp_cap")
	for _, category := range []string{"spend", "Credentials", "omp_cap"} {
		code, out, _ := run("--tier", "seat", "--category", category, "--summary", "s", "--questions", questions)
		if code != ExitEscalated || !strings.Contains(out, `"escalate_to":"owner"`) {
			t.Errorf("%s: exit %d out %q", category, code, out)
		}
	}
	if rec.hits != 0 {
		t.Errorf("Jev was called %d times for owner-only categories", rec.hits)
	}
	if entries := readLog(t, logPath); len(entries) != 3 {
		t.Errorf("log = %v", entries)
	}
}

func TestEvaluatorErrorEscalatesAndIsLogged(t *testing.T) {
	_, logPath := jev(t, 500, `boom`)
	code, out, _ := run("--tier", "seat", "--summary", "s", "--questions", questions)
	if code != ExitEscalated || !strings.Contains(out, "evaluator error") || !strings.Contains(out, `"escalate_to":"lead"`) {
		t.Errorf("exit %d out %q", code, out)
	}
	if entries := readLog(t, logPath); len(entries) != 1 || entries[0]["error"] == nil {
		t.Errorf("log = %v", entries)
	}
}

func TestAnUnansweredQuestionEscalatesTheWholeAsk(t *testing.T) {
	jev(t, 200, `{"answers":{}}`)
	if code, _, _ := run("--tier", "seat", "--summary", "s", "--questions", questions); code != ExitEscalated {
		t.Errorf("exit %d", code)
	}
}

func TestUsageRefusalsCallNothingAndLogNothing(t *testing.T) {
	rec, logPath := jev(t, 200, `{}`)
	for _, args := range [][]string{
		{"--tier", "owner", "--summary", "s", "--questions", questions},
		{"--tier", "seat", "--questions", questions},
		{"--tier", "seat", "--summary", "s"},
		{"--tier", "seat", "--summary", "s", "--questions", "{nope"},
		{"--tier", "seat", "--summary", "s", "--questions", `[{"id":"q1","question":"x","options":[{"id":"a","label":"A"}]}]`},
		{"--tier", "seat", "--summary", "s", "--questions", questions, "--questions-file", "x"},
		{"--tier", "seat", "--summary", "token ghp_abcdefghijklmnopqrstuvwxyz0123456789", "--questions", questions},
		{"--bogus"},
	} {
		if code, _, _ := run(args...); code != clientcmd.ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	if code, _, errs := run("--tier", "seat", "--summary", "s", "--questions", questions); code != clientcmd.ExitUsage || !strings.Contains(errs, "TYPESAFE_API_KEY") {
		t.Errorf("no key: exit %d %q", code, errs)
	}
	t.Setenv("JEV_DECISION_THRESHOLD", "high")
	t.Setenv("TYPESAFE_API_KEY", "k")
	if code, _, errs := run("--tier", "seat", "--summary", "s", "--questions", questions); code != clientcmd.ExitUsage || !strings.Contains(errs, "JEV_DECISION_THRESHOLD") {
		t.Errorf("bad threshold: exit %d %q", code, errs)
	}
	if rec.hits != 0 {
		t.Errorf("refusals called Jev %d times", rec.hits)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("a refusal wrote a log line")
	}
}

func TestQuestionsFileIsRead(t *testing.T) {
	jev(t, 200, `{"answers":{"q1":{"choice":"pg","confidence":0.9}}}`)
	path := filepath.Join(t.TempDir(), "q.json")
	if err := os.WriteFile(path, []byte(questions), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("--tier", "lead", "--summary", "s", "--questions-file", path); code != clientcmd.ExitOK {
		t.Errorf("exit %d stderr %q", code, errs)
	}
}

func TestHelp(t *testing.T) {
	if code, out, _ := run("--help"); code != clientcmd.ExitOK || !strings.Contains(out, "4genteam decide") {
		t.Errorf("exit %d out %q", code, out)
	}
}
