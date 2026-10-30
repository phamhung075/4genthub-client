package clientdecide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientdecisions"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
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
	isolateRoute(t)
	return rec, logPath
}

// asks records the owner asks raised; isolateRoute points routing at a temp state root with no room, no seat
// session and no daemon, so a test run from inside a seat never writes to a live rail.
type asks struct {
	rows []clientruntime.QueueCreateInput
	room []string
}

func isolateRoute(t *testing.T) (*asks, string) {
	t.Helper()
	for _, name := range []string{"AGENTHUB_SESSION_NAME", "OPENRIG_SESSION_NAME", "AGENTHUB_ROOM", "AGENTHUB_LANES_ROOT"} {
		t.Setenv(name, "")
	}
	root := t.TempDir()
	got := &asks{}
	oldRoot, oldStamp, oldCreate := stateRoot, stamp, createAsk
	stateRoot = func() string { return root }
	stamp = func() string { return "2026-10-30 07:00 CET" }
	createAsk = func(room string, in clientruntime.QueueCreateInput) (string, error) {
		got.rows, got.room = append(got.rows, in), append(got.room, room)
		return fmt.Sprintf("qitem-test-%d", len(got.rows)), nil
	}
	t.Cleanup(func() { stateRoot, stamp, createAsk = oldRoot, oldStamp, oldCreate })
	return got, root
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

// THE OWNER'S COUNTS-ONLY RULE, read from both ends: a question that names a file path and a test name
// leaves with placeholders, while the local log keeps the full question, its options and the pick
// (owner answer O9a, 2026-10-16: "No file paths or test names to Jev/TypeSafe: counts only").
func TestTheWireLosesTheNamesTheLogKeeps(t *testing.T) {
	named := `[{"id":"q1","question":"Is internal/clientjev/jev.go fixed and TestRedactForWire green?","options":[{"id":"yes","label":"fix internal/clientjev/jev.go","recommended":true},{"id":"no","label":"revert"}]}]`
	rec, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"yes","confidence":0.93}}}`)
	code, _, errs := run("--tier", "seat", "--category", "General", "--summary", "fix", "--detail", "the redactor", "--questions", named)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	sent, _ := json.Marshal(rec.body)
	for _, forbidden := range []string{".go", "TestRedactForWire", "clientjev"} {
		if strings.Contains(string(sent), forbidden) {
			t.Errorf("the wire carries %q: %s", forbidden, sent)
		}
	}
	q1 := rec.body["questions"].(map[string]any)["q1"].(map[string]any)
	if instr, _ := q1["instructions"].(string); !strings.Contains(instr, "<path>") || !strings.Contains(instr, "<test>") {
		t.Errorf("the shaped question lost its placeholders: %v", q1)
	}
	entries := readLog(t, logPath)
	if len(entries) != 1 {
		t.Fatalf("log = %v", entries)
	}
	logged, _ := json.Marshal(entries[0])
	if !strings.Contains(string(logged), "TestRedactForWire") || !strings.Contains(string(logged), "internal/clientjev/jev.go") {
		t.Errorf("the log lost the question's names: %s", logged)
	}
	if entries[0]["outcome"] != "decided" || !strings.Contains(string(logged), `"selected_option_id":"yes"`) {
		t.Errorf("the log lost the pick: %s", logged)
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

// The refusal decide.go:109 makes is `secretscan.Contains(summary|detail)`, and the sk- pattern's `\b` plus its
// alphanumeric tail is what keeps a brief that names a hyphenated doc from being refused as a credential. This
// is the shape the row was filed for: the same summary and detail as a real ask about task-workflow-and-reporting.md,
// answered confidently so the run completes and the only thing under test is the entry check.
func TestABriefThatNamesAHyphenatedDocIsNotACredential(t *testing.T) {
	rec, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"pg","confidence":0.93}}}`)
	code, _, errs := run("--tier", "seat", "--summary", "the register line for task-workflow-and-reporting.md",
		"--detail", "changed ai_docs/agent-system/task-workflow-and-reporting.md and ai_docs/agent-system/seat-model-and-mcp-surface.md",
		"--questions", questions)
	if code != 0 || rec.hits != 1 {
		t.Fatalf("a doc name is not a credential: exit %d, %d Jev calls, stderr %q", code, rec.hits, errs)
	}
	if strings.Contains(errs, "credential") {
		t.Errorf("the entry check named a credential: %q", errs)
	}
	if entries := readLog(t, logPath); len(entries) != 1 {
		t.Errorf("the decided ask logged %d entries, want one", len(entries))
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

func TestDetailFileCarriesTextAShellWouldBreak(t *testing.T) {
	_, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"pg","confidence":0.9}}}`)
	detail := "line one has \"quotes\" and $HOME\nline two; echo \"EXIT=$?\""
	path := filepath.Join(t.TempDir(), "detail.txt")
	if err := os.WriteFile(path, []byte(detail+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("--tier", "seat", "--summary", "s", "--detail-file", path, "--questions", questions); code != clientcmd.ExitOK {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
	if entries := readLog(t, logPath); len(entries) != 1 || entries[0]["detail"] != detail {
		t.Errorf("logged detail = %v, want %q", entries, detail)
	}
	if code, _, _ := run("--tier", "seat", "--summary", "s", "--detail", "x", "--detail-file", path, "--questions", questions); code != clientcmd.ExitUsage {
		t.Errorf("both --detail and --detail-file: exit %d, want usage", code)
	}
}

// TestALeadCannotRewordAnOwnerAskIntoItsOwnDecision: the aa2-rs sequence of 2026-10-27. The lead's ask went
// to the owner; the same subject, narrowed, came back at lead tier and Jev decided it. Now the narrowed ask
// is not sent to Jev at all - it escalates to the owner again and names the open escalation - unless the
// lead states why it is a different question, and that reason is logged.
func TestALeadCannotRewordAnOwnerAskIntoItsOwnDecision(t *testing.T) {
	rec, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"lite","confidence":0.5}}}`)
	if code, _, _ := run("--tier", "lead", "--summary", "Phase 1 depth-gate exit rule: penetration alone, or penetration plus measured clearance", "--questions", questions); code != ExitEscalated {
		t.Fatalf("the first ask did not escalate: %d", code)
	}
	hits := rec.hits

	for _, reworded := range []string{"Phase 1 exit rule scope", "Phase 1 exit rule: does short gate"} {
		code, out, errText := run("--tier", "lead", "--summary", reworded, "--questions", questions)
		var got outcome
		_ = json.Unmarshal([]byte(out), &got)
		if code != ExitEscalated || got.EscalateTo != "owner" || !strings.Contains(got.Reason, "already escalated to the owner") || !strings.Contains(errText, "--distinct-from-owner-ask") {
			t.Errorf("%q: exit %d out %q err %q", reworded, code, out, errText)
		}
	}
	if rec.hits != hits {
		t.Errorf("Jev was asked %d more times for a question the owner holds", rec.hits-hits)
	}

	// A different subject is untouched, and so is a seat-tier ask.
	run("--tier", "lead", "--summary", "continue the p performance line", "--questions", questions)
	run("--tier", "seat", "--summary", "Phase 1 exit rule scope", "--questions", questions)
	if rec.hits != hits+2 {
		t.Errorf("unrelated asks reached Jev %d times, want 2", rec.hits-hits)
	}

	// The override goes through to Jev, and its reason is on the record.
	run("--tier", "lead", "--summary", "Phase 1 exit rule scope", "--questions", questions,
		"--distinct-from-owner-ask", "asks which pairs the rule covers, not which rule")
	if rec.hits != hits+3 {
		t.Errorf("the override did not reach Jev")
	}
	entries := readLog(t, logPath)
	last := entries[len(entries)-1]
	if detail, _ := last["detail"].(string); !strings.Contains(detail, "[distinct-from-owner-ask] asks which pairs") {
		t.Errorf("the override is not on the record: %v", last)
	}
}

// Regression, 2026-10-30: 52 escalated-to-owner log entries, one owner-decision row on any rail. A fresh lead
// escalation now raises the owner ask itself, with its questions recorded for the dashboard, and names the room.
func TestALeadEscalationRaisesTheOwnerAskForTheDashboard(t *testing.T) {
	_, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"lite","confidence":0.5}}}`)
	got, root := isolateRoute(t)
	t.Setenv("AGENTHUB_SESSION_NAME", "agenthub--plain--lead")
	code, out, errText := run("--tier", "lead", "--summary", "landing channel for two gated commits", "--questions", questions)
	var result outcome
	_ = json.Unmarshal([]byte(out), &result)
	if code != ExitEscalated || result.EscalateTo != "owner" || result.OwnerAsk != "qitem-test-1" || result.Room != "plain" {
		t.Fatalf("exit %d out %q err %q", code, out, errText)
	}
	if len(got.rows) != 1 || got.room[0] != "plain" {
		t.Fatalf("asks = %+v", got)
	}
	row := got.rows[0]
	if row.DestinationSession != "human@plain" || row.SourceSession != "agenthub--plain--lead" || len(row.Tags) != 1 || row.Tags[0] != "owner-decision" {
		t.Errorf("row = %+v", row)
	}
	caps := map[string]int{"facts": 280, "rule": 110, "decision": 100, "next": 78}
	for _, line := range strings.Split(row.Body, "\n") {
		key, value, _ := strings.Cut(line, ": ")
		if limit, ok := caps[key]; !ok || len([]rune(value)) > limit {
			t.Errorf("body line %q breaks the rail's shape", line)
		}
	}
	if records, err := clientdecisions.List(root, "plain"); err != nil || len(records) != 1 || records[0].QitemID != "qitem-test-1" {
		t.Errorf("record = %v %v", records, err)
	}
	if entries := readLog(t, logPath); entries[0]["room"] != "plain" || entries[0]["outcome"] != "escalated-to-owner" {
		t.Errorf("log = %v", entries)
	}

	// The re-ask the guard answers is not a second ask; a seat's escalation to the lead raises nothing.
	run("--tier", "lead", "--summary", "landing channel for the two gated commits", "--questions", questions)
	run("--tier", "seat", "--summary", "unrelated seat question here", "--questions", questions)
	if len(got.rows) != 1 {
		t.Errorf("%d asks raised, want 1", len(got.rows))
	}
}

// Owner, 2026-10-30: in a room with a director office the owner tier is the director's ready.md, not the dashboard.
func TestADirectorRoomSendsTheOwnerTierToReadyMd(t *testing.T) {
	_, logPath := jev(t, 200, `{"answers":{"q1":{"choice":"lite","confidence":0.5}}}`)
	got, root := isolateRoute(t)
	office := filepath.Join(root, "rooms", "dir", "director")
	if err := os.MkdirAll(office, 0o700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(office, "ready.md")
	_ = os.WriteFile(ready, []byte("- earlier line"), 0o600)
	t.Setenv("AGENTHUB_SESSION_NAME", "agenthub--dir--rust-dev")
	code, out, errText := run("--tier", "seat", "--category", "spend", "--summary", "buy | more\nslots", "--questions", questions)
	var result outcome
	_ = json.Unmarshal([]byte(out), &result)
	if code != ExitEscalated || result.EscalateTo != "director" || result.Ready != ready || result.OwnerAsk != "" {
		t.Fatalf("exit %d out %q err %q", code, out, errText)
	}
	if len(got.rows) != 0 {
		t.Errorf("a director room raised %d dashboard asks", len(got.rows))
	}
	if !strings.Contains(errText, "the director answers there") {
		t.Errorf("stderr = %q", errText)
	}
	data, _ := os.ReadFile(ready)
	want := "- earlier line\n- 2026-10-30 07:00 CET | QUESTION | rust-dev (Jev escalation) | buy / more slots; options: pg: PostgreSQL, lite: SQLite; asker's pick: pg\n"
	if string(data) != want {
		t.Errorf("ready.md =\n%q\nwant\n%q", data, want)
	}
	// The lead's own escalation is appended once; its re-wording is told the director holds it.
	run("--tier", "lead", "--summary", "buy more slots now", "--questions", questions)
	_, out, _ = run("--tier", "lead", "--summary", "buy more slots", "--questions", questions)
	if !strings.Contains(out, "already escalated to the director") {
		t.Errorf("re-ask out = %q", out)
	}
	if entries := readLog(t, logPath); entries[0]["outcome"] != "escalated-to-director" || entries[0]["room"] != "dir" {
		t.Errorf("log = %v", entries)
	}
	if after, _ := os.ReadFile(ready); strings.Count(string(after), "QUESTION") != 2 {
		t.Errorf("ready.md =\n%s", after)
	}
}
