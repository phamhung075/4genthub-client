package clientjev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientevidence"
)

func TestFromEnvDefaultsAndOwnerOnly(t *testing.T) {
	for _, name := range []string{"TYPESAFE_API_KEY", "JEV_API_KEY", "JEV_TOKEN", "TYPESAFE_BASE_URL", "JEV_DECISION_MODEL", "JEV_DECISION_THRESHOLD"} {
		t.Setenv(name, "")
	}
	t.Setenv("JEV_DECISION_LOG_PATH", "/tmp/x/jev.log")
	t.Setenv("DECISION_OWNER_ONLY_CATEGORIES", " Omp_Cap , ,deploy")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "jev-1.13.0" || cfg.Threshold != 0.8 || cfg.BaseURL != "https://api.typesafe.ai/v1" || cfg.LogPath != "/tmp/x/jev.log" {
		t.Errorf("defaults = %+v", cfg)
	}
	for _, name := range []string{"credentials", "SPEND", "omp_cap", "deploy"} {
		if !cfg.OwnerOnly(name) {
			t.Errorf("%s should be owner-only", name)
		}
	}
	if cfg.OwnerOnly("general") || cfg.OwnerOnly("") {
		t.Error("general must not be owner-only")
	}
}

func TestFromEnvRefusesABadThreshold(t *testing.T) {
	for _, bad := range []string{"high", "0", "1.5", "-1"} {
		t.Setenv("JEV_DECISION_THRESHOLD", bad)
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "JEV_DECISION_THRESHOLD") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestParseQuestionsLimits(t *testing.T) {
	ok := `[{"id":"q1","question":"?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B"}]}]`
	qs, err := ParseQuestions([]byte(ok))
	if err != nil || len(qs) != 1 || qs[0].Recommended() != "a" {
		t.Fatalf("ok: %v %v", qs, err)
	}
	for name, bad := range map[string]string{
		"not an array":    `{}`,
		"empty":           `[]`,
		"bad id":          `[{"id":"a b","question":"?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]`,
		"one option":      `[{"id":"q","question":"?","options":[{"id":"a","label":"A"}]}]`,
		"two recommended": `[{"id":"q","question":"?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B","recommended":true}]}]`,
		"long label":      `[{"id":"q","question":"?","options":[{"id":"a","label":"` + strings.Repeat("x", 76) + `"},{"id":"b","label":"B"}]}]`,
		"duplicate id":    `[{"id":"q","question":"?","options":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}]`,
	} {
		if _, err := ParseQuestions([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The criteria check accepts at the CONFIGURED threshold, not at a number of its own.
func TestEvaluateCriteriaFollowsTheConfiguredThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"criterion_1":{"noul":0.77}}}`))
	}))
	defer server.Close()
	for threshold, want := range map[float64]Verdict{0.75: Accept, 0.9: Uncertain} {
		cfg := Config{APIKey: "k", Model: "m", BaseURL: server.URL, Threshold: threshold}
		got, reason, err := cfg.EvaluateCriteria(context.Background(), []string{"c"}, nil, nil)
		if err != nil || got != want {
			t.Fatalf("threshold %v: verdict %q (%s), err %v, want %q", threshold, got, reason, err, want)
		}
	}
}

// keysOf is a JSON object's key set, sorted: a payload's SHAPE, compared without depending on field order.
func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s is not a JSON object: %v", raw, err)
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// rawOf is one field of a JSON object, un-decoded, so a nested shape can be inspected in place.
func rawOf(t *testing.T, raw json.RawMessage, name string) json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s is not a JSON object: %v", raw, err)
	}
	value, ok := obj[name]
	if !ok {
		t.Fatalf("%s carries no %q", raw, name)
	}
	return value
}

// THE CRITERIA REQUEST IS THE CONTRACT WITH SYSTEM ONE, and this is the only test that reads it: three
// top-level keys, a COUNTS-ONLY state, and one `noul` question per criterion whose text keeps its shape
// with every file path and test name replaced (owner ruling O9a, 2026-10-16: "No file paths or test
// names to Jev/TypeSafe: counts only"). Nothing else travels, because nothing else is shown to the
// judge - so a later move to a richer payload has to break this test rather than pass unnoticed. The
// threshold case above reads only the answers; this one reads the request.
func TestTheCriteriaRequestCarriesTheEvidenceAndNothingElse(t *testing.T) {
	var request json.RawMessage
	var path, contentType, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, contentType, authorization = r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		_, _ = w.Write([]byte(`{"answers":{"criterion_1":{"noul":0.99},"criterion_2":{"noul":0.99}}}`))
	}))
	defer server.Close()

	body := clientevidence.Body{BaseSHA: "99cdc0ef", HeadSHA: "0c53c313", Numstat: "3\t1\ta.go\n-\t-\tlogo.png\n"}
	body.Test.Command = "go test -count=1 ./fastmcp/..."
	body.Test.Failed = []string{}
	criteria := []string{
		"The acceptance command `go test -count=1 ./fastmcp/...` exits 0",
		"The numstat names exactly one path under CHANGELOG/",
	}
	cfg := Config{APIKey: "k", Model: "m", BaseURL: server.URL, Threshold: 0.8}
	if verdict, reason, err := cfg.EvaluateCriteria(context.Background(), criteria, nil, body); err != nil || verdict != Accept {
		t.Fatalf("verdict %q (%s), err %v", verdict, reason, err)
	}
	if path != "/systemone" || contentType != "application/json" || authorization != "Bearer k" {
		t.Errorf("posted to %s as %s with %q", path, contentType, authorization)
	}
	if got, want := keysOf(t, request), []string{"model", "questions", "state"}; !slices.Equal(got, want) {
		t.Errorf("top-level keys = %v, want %v", got, want)
	}
	// THE PROBE THE RULING ASKS FOR: a criterion naming a file path and a test name went out, and the
	// bytes carry neither. The state is the counts summary, not the evidence body.
	raw, _ := json.Marshal(request)
	for _, forbidden := range []string{"./fastmcp", "CHANGELOG/", "a.go", "logo.png"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("the wire carries %q, and the ruling forbids paths: %s", forbidden, raw)
		}
	}
	var state map[string]any
	if err := json.Unmarshal(rawOf(t, request, "state"), &state); err != nil {
		t.Fatal(err)
	}
	wantState := map[string]any{
		"test_command_ran": true, "test_exit_code": 0, "tests_failed": 0,
		"files_changed": 2, "lines_changed": 4, "lines_added": 3, "lines_deleted": 1,
		// The per-path counts, in WireRows order (largest total first, binary last): a missing row or a
		// missing total fails here, which is the board's acceptance criterion 1.
		"changes":  []any{map[string]any{"added": 3, "deleted": 1}, map[string]any{"binary": true}},
		"base_sha": "99cdc0ef", "head_sha": "0c53c313",
	}
	if got := fmt.Sprint(state); got != fmt.Sprint(wantState) {
		t.Errorf("state = %v, want the counts %v", state, wantState)
	}
	// One `noul` question per criterion, the criterion's own text verbatim, and nothing else per question.
	var questions map[string]json.RawMessage
	if err := json.Unmarshal(rawOf(t, request, "questions"), &questions); err != nil {
		t.Fatalf("questions is not an object of objects: %v", err)
	}
	if len(questions) != len(criteria) {
		t.Errorf("questions = %d, want %d", len(questions), len(criteria))
	}
	for i := range criteria {
		id := fmt.Sprintf("criterion_%d", i+1)
		question, ok := questions[id]
		if !ok {
			t.Errorf("%s is missing: %v", id, questions)
			continue
		}
		if got := keysOf(t, question); !slices.Equal(got, []string{"instructions", "type"}) {
			t.Errorf("%s carries %v, want [instructions type]", id, got)
		}
		var kind, instruction string
		_ = json.Unmarshal(rawOf(t, question, "type"), &kind)
		_ = json.Unmarshal(rawOf(t, question, "instructions"), &instruction)
		if kind != "noul" {
			t.Errorf("%s type = %q, want noul", id, kind)
		}
		if strings.Contains(instruction, "./fastmcp") || strings.Contains(instruction, "CHANGELOG/") {
			t.Errorf("%s instruction still names a path: %q", id, instruction)
		}
		if !strings.Contains(instruction, "<path>") {
			t.Errorf("%s instruction lost the placeholder: %q", id, instruction)
		}
	}
}

// THE WORDING IS THE INSTRUMENT, pinned as a case: the same evidence, one criterion in the evidence
// vocabulary and one that asks for content - the mechanical one answers high, the content one low, and
// the refusal NAMES the criterion that failed while carrying every answer. The criteria reach Jev with
// no instruction prefix of the gate's own and no rescue; since the O9a ruling the two that name a path
// leave with a placeholder instead, and the criterion that names nothing travels verbatim.
func TestTheCriterionWordingDecidesAndTheRefusalNamesIt(t *testing.T) {
	const mechanical = "The acceptance command `go test -count=1 ./fastmcp/...` exits 0 and the numstat names exactly one path under CHANGELOG/"
	const content = "a real closure through the gate is demonstrated from a seat"
	var request json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		_, _ = w.Write([]byte(`{"answers":{"criterion_1":{"noul":0.97},"criterion_2":{"noul":0.12}}}`))
	}))
	defer server.Close()

	body := clientevidence.Body{Numstat: "10\t0\tCHANGELOG/2026-10-16--x.md\n"}
	body.Test.Command = "go test -count=1 ./fastmcp/..."
	body.Test.Failed = []string{}
	cfg := Config{APIKey: "k", Model: "m", BaseURL: server.URL, Threshold: 0.8}
	verdict, reason, err := cfg.EvaluateCriteria(context.Background(), []string{mechanical, content}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != Reject {
		t.Errorf("verdict = %q (%s), want reject: one criterion cannot be settled from the evidence", verdict, reason)
	}
	if !strings.Contains(reason, "criterion_2: 0.12") {
		t.Errorf("the refusal must name the criterion it refused: %s", reason)
	}
	if !strings.Contains(reason, "criterion_1: 0.97") {
		t.Errorf("the refusal must carry every answer, so the reader sees which one failed: %s", reason)
	}
	var questions map[string]struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(rawOf(t, request, "questions"), &questions); err != nil {
		t.Fatal(err)
	}
	if got := questions["criterion_2"].Instructions; got != content {
		t.Errorf("criterion_2 instructions = %q, want %q verbatim: it names no path or test", got, content)
	}
	shaped := questions["criterion_1"].Instructions
	if strings.Contains(shaped, "./fastmcp") || strings.Contains(shaped, "CHANGELOG/") {
		t.Errorf("criterion_1 still names a path: %q", shaped)
	}
	if !strings.Contains(shaped, "The acceptance command") || !strings.Contains(shaped, "<path>") {
		t.Errorf("criterion_1 lost its shape: %q", shaped)
	}
}

// A numstat the state cannot read is an error, never a zero summary: a judge told files_changed 0 for a
// five-file change would answer about a bundle that was never measured. The gate refuses this body at D5
// first; this is the decide path's guard, and the request must not leave at all.
func TestAnUnreadableNumstatIsNeverSentAsZeros(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"answers":{"criterion_1":{"noul":0.99}}}`))
	}))
	defer server.Close()
	cfg := Config{APIKey: "k", Model: "m", BaseURL: server.URL, Threshold: 0.8}
	body := clientevidence.Body{BaseSHA: "99cdc0ef", HeadSHA: "0c53c313", Numstat: "garbage\n"}
	verdict, reason, err := cfg.EvaluateCriteria(context.Background(), []string{"it works"}, nil, body)
	if err == nil || verdict != Uncertain {
		t.Fatalf("verdict %q, reason %q, err %v: want uncertain and an error", verdict, reason, err)
	}
	if !strings.Contains(reason, `numstat line "garbage" is not added, deleted, path`) {
		t.Errorf("the reason must quote the parser's own words: %q", reason)
	}
	if hits != 0 {
		t.Errorf("Jev was called %d times for a body whose counts do not parse", hits)
	}
}
