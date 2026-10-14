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
	hits         int
	method, path string
	auth         string
	body         map[string]any
}

func server(t *testing.T, status int, reply string) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.hits++
		rec.method, rec.path, rec.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_ = json.Unmarshal(raw, &rec.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENTHUB_URL", srv.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok-secret")
	return srv, rec
}

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := decideCommand{}.Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestDecidedPostsTheLadderStepAndExitsZero(t *testing.T) {
	_, rec := server(t, 200, `{"tier":"seat","decided":true,"answers":{"q1":"pg"}}`)
	code, out, errs := run("--tier", "seat", "--category", "General", "--summary", "db", "--detail", "pick", "--questions", questions)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if rec.method != "POST" || rec.path != evaluatePath || rec.auth != "Bearer tok-secret" {
		t.Errorf("request = %s %s auth %q", rec.method, rec.path, rec.auth)
	}
	if rec.body["tier"] != "seat" || rec.body["summary"] != "db" || rec.body["category"] != "General" || rec.body["detail"] != "pick" {
		t.Errorf("body = %v", rec.body)
	}
	if qs, ok := rec.body["questions"].([]any); !ok || len(qs) != 1 {
		t.Errorf("questions = %v", rec.body["questions"])
	}
	if !strings.Contains(out, `"pg"`) || strings.Contains(out+errs, "tok-secret") {
		t.Errorf("stdout %q stderr %q", out, errs)
	}
}

func TestEscalationExitsFiveAndRelaysTheNextTier(t *testing.T) {
	server(t, 200, `{"tier":"seat","decided":false,"escalate_to":"owner","reason":"owner-only category: omp_cap"}`)
	code, out, _ := run("--tier", "seat", "--category", "omp_cap", "--summary", "cap", "--questions", questions)
	if code != ExitEscalated || !strings.Contains(out, `"escalate_to":"owner"`) {
		t.Errorf("exit %d out %q", code, out)
	}
}

func TestQuestionsFileAndServerRefusal(t *testing.T) {
	_, rec := server(t, 400, `{"detail":"bad"}`)
	path := filepath.Join(t.TempDir(), "q.json")
	if err := os.WriteFile(path, []byte(questions), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("--tier", "lead", "--summary", "s", "--questions-file", path)
	if code != clientcmd.ExitRemote || !strings.Contains(errs, "HTTP 400") || rec.body["tier"] != "lead" {
		t.Errorf("exit %d stderr %q body %v", code, errs, rec.body)
	}
}

func TestUsageRefusalsDialNothing(t *testing.T) {
	_, rec := server(t, 200, `{}`)
	for _, args := range [][]string{
		{"--tier", "owner", "--summary", "s", "--questions", questions},
		{"--tier", "seat", "--questions", questions},
		{"--tier", "seat", "--summary", "s"},
		{"--tier", "seat", "--summary", "s", "--questions", "{nope"},
		{"--tier", "seat", "--summary", "s", "--questions", questions, "--questions-file", "x"},
		{"--bogus"},
	} {
		if code, _, _ := run(args...); code != clientcmd.ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	t.Setenv("AGENTHUB_TOKEN", "")
	if code, _, errs := run("--tier", "seat", "--summary", "s", "--questions", questions); code != clientcmd.ExitUsage || !strings.Contains(errs, "AGENTHUB_TOKEN") {
		t.Errorf("no token: exit %d %q", code, errs)
	}
	if rec.hits != 0 {
		t.Errorf("refusals dialed %d times", rec.hits)
	}
}

func TestHelp(t *testing.T) {
	if code, out, _ := run("--help"); code != clientcmd.ExitOK || !strings.Contains(out, "4genteam decide") {
		t.Errorf("exit %d out %q", code, out)
	}
}
