package clientwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The feed renderer's contract, ported from tests/test_watch_tools.py test-for-test. Every test here
// is a pure function call: no herdr, no tmux, no rig, no seat.

func sessionLine(role string, content any) string {
	payload := map[string]any{"message": map[string]any{"role": role, "content": content}}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func toolCall(name string, arguments map[string]any) string {
	return sessionLine("assistant", []any{
		map[string]any{"type": "toolCall", "name": name, "arguments": arguments},
	})
}

func toolResult(text string) string {
	return sessionLine("toolResult", []any{map[string]any{"type": "text", "text": text}})
}

func TestAToolCallShowsItsNameAndTheMainArgument(t *testing.T) {
	rendered := events(toolCall("bash", map[string]any{"command": "git status", "cwd": "/x"}), 200, false, 25)
	if len(rendered) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(rendered), rendered)
	}
	for _, want := range []string{"bash", "command=git status", "+cwd"} {
		if !strings.Contains(rendered[0], want) {
			t.Errorf("line %q missing %q", rendered[0], want)
		}
	}
}

func TestOnlyARealPolicyRefusalIsMarkedBlocked(t *testing.T) {
	refusal := `Tool "bash" is blocked by tool policy. Reason: Blocked by bash pattern: git push*`
	quoted := "POLICY=BLOCKED (Blocked by bash pattern: git push*)"
	blocked := events(toolResult(refusal), 200, false, 25)
	plain := events(toolResult(quoted), 200, false, 25)
	if len(blocked) != 1 || !strings.Contains(blocked[0], "BLOCKED") {
		t.Errorf("a real refusal was not marked: %v", blocked)
	}
	if len(plain) != 1 {
		t.Fatalf("got %d lines, want 1", len(plain))
	}
	if head, _, _ := strings.Cut(plain[0], "POLICY="); strings.Contains(head, "BLOCKED") {
		t.Errorf("a quoted refusal was marked as one: %q", plain[0])
	}
}

func TestLinesThatAreNotToolEventsOrNotJSONProduceNothing(t *testing.T) {
	if rendered := events("not json", 200, false, 25); len(rendered) != 0 {
		t.Errorf("a non-JSON line produced %v", rendered)
	}
	saying := sessionLine("assistant", []any{map[string]any{"type": "text", "text": "hi"}})
	if rendered := events(saying, 200, false, 25); len(rendered) != 0 {
		t.Errorf("a plain reply produced %v without --detail", rendered)
	}
}

func TestMCPToolsGetTheirOwnColour(t *testing.T) {
	rendered := events(toolCall("mcp__deepseek_agent", map[string]any{"prompt": "x"}), 200, false, 25)
	if len(rendered) != 1 {
		t.Fatalf("got %d lines, want 1", len(rendered))
	}
	if !strings.Contains(rendered[0], fg(mcpColor)) {
		t.Errorf("an mcp call did not use the mcp colour: %q", rendered[0])
	}
}

func TestDetailAddsReasoningSpeechAndIncomingMessagesAndNothingElseDoes(t *testing.T) {
	cases := []struct {
		line   string
		marker string
	}{
		{sessionLine("assistant", []any{map[string]any{"type": "thinking", "thinking": "weigh the two options"}}), "think"},
		{sessionLine("assistant", []any{map[string]any{"type": "text", "text": "I will run the tests now"}}), "say"},
		{sessionLine("user", []any{map[string]any{"type": "text", "text": "please review packet 5"}}), "in"},
	}
	for _, entry := range cases {
		if rendered := events(entry.line, 200, false, 25); len(rendered) != 0 {
			t.Errorf("%s appeared without --detail: %v", entry.marker, rendered)
		}
		rendered := events(entry.line, 200, true, 25)
		if len(rendered) != 1 || !strings.Contains(rendered[0], entry.marker) {
			t.Errorf("%s missing with --detail: %v", entry.marker, rendered)
		}
	}
}

func TestASingleCharacterReplyIsNotShownAsSomethingSaid(t *testing.T) {
	dot := sessionLine("assistant", []any{map[string]any{"type": "text", "text": "."}})
	if rendered := events(dot, 200, true, 25); len(rendered) != 0 {
		t.Errorf("a one-character reply was shown: %v", rendered)
	}
}

func TestAMultiLineResultIsShownAsLinesNotJoined(t *testing.T) {
	rendered := events(toolResult("first\nsecond\nthird"), 200, false, 5)
	if len(rendered) != 1 {
		t.Fatalf("got %d lines, want 1", len(rendered))
	}
	if strings.Contains(rendered[0], "⏎") {
		t.Errorf("a multi-line result was joined: %q", rendered[0])
	}
	if got := strings.Count(rendered[0], "\n"); got != 2 {
		t.Errorf("newlines = %d, want 2", got)
	}
}

func TestLinesBeyondTheCapAreCountedNotDroppedSilently(t *testing.T) {
	rows := []string{}
	for n := range 12 {
		rows = append(rows, fmt.Sprintf("row %d", n))
	}
	rendered := events(toolResult(strings.Join(rows, "\n")), 200, false, 5)
	if len(rendered) != 1 {
		t.Fatalf("got %d lines, want 1", len(rendered))
	}
	if !strings.Contains(rendered[0], "row 4") || strings.Contains(rendered[0], "row 5") {
		t.Errorf("the cap was not applied at 5 rows: %q", rendered[0])
	}
	if !strings.Contains(rendered[0], "+7 more lines") {
		t.Errorf("the hidden rows were not counted: %q", rendered[0])
	}
}

func TestACompactJSONResultIsShownIndented(t *testing.T) {
	rendered := events(toolResult(`{"success":true,"data":{"task":{"id":"abc"}}}`), 200, false, 25)
	if len(rendered) != 1 {
		t.Fatalf("got %d lines, want 1", len(rendered))
	}
	if !strings.Contains(rendered[0], "  \"success\": true") {
		t.Errorf("not indented:\n%s", rendered[0])
	}
	if got := strings.Count(rendered[0], "\n"); got < 4 {
		t.Errorf("newlines = %d, want at least 4", got)
	}
}

func TestALongLineIsCutAtTheWidthButTheOtherLinesStay(t *testing.T) {
	rendered := events(toolResult(strings.Repeat("x", 500)+"\nsecond"), 50, false, 25)
	if len(rendered) != 1 {
		t.Fatalf("got %d lines, want 1", len(rendered))
	}
	if !strings.Contains(rendered[0], strings.Repeat("x", 50)) {
		t.Errorf("the kept prefix is missing")
	}
	if strings.Contains(rendered[0], strings.Repeat("x", 51)) {
		t.Errorf("the line was not cut at 50")
	}
	if !strings.Contains(rendered[0], "second") {
		t.Errorf("the second line was dropped")
	}
}

func TestPrettyIndentsJSONFollowedByATrailer(t *testing.T) {
	shown := pretty(`{"a":[1,{"b":2}]}` + "\n\n" + "Wall time: 0.1 seconds")
	if !strings.HasPrefix(shown, "{\n  \"a\": [\n    1,") {
		t.Errorf("the JSON was not indented:\n%s", shown)
	}
	if !strings.HasSuffix(shown, "}\n\nWall time: 0.1 seconds") {
		t.Errorf("the trailer after the JSON was rewritten or dropped:\n%s", shown)
	}
}

func TestPrettyIndentsJSONCutOffByTheLog(t *testing.T) {
	shown := pretty(`{"a":[1,{"b":"x, {y}"},{"k":"cut` + "\n\n" + "[Some lines truncated]")
	if !strings.Contains(shown, `"b":"x, {y}"`) {
		t.Errorf("a comma or brace inside a string was broken as structure:\n%s", shown)
	}
	if strings.Count(shown, "\n") <= 6 {
		t.Errorf("the cut-off JSON was not indented as far as it goes:\n%s", shown)
	}
	if !strings.HasSuffix(shown, "[Some lines truncated]") {
		t.Errorf("the log's truncation notice was dropped:\n%s", shown)
	}
}

func TestPrettyLeavesProseAlone(t *testing.T) {
	const prose = `plain {"a":1}`
	if got := pretty(prose); got != prose {
		t.Errorf("pretty(%q) = %q, want the prose returned unchanged", prose, got)
	}
}

func TestKiloeAndTokenBarReadLikeThePython(t *testing.T) {
	if got := kilo(150_000); got != "150k" {
		t.Errorf("kilo(150000) = %q, want 150k", got)
	}
	if got := kilo(1_500_000); got != "1.50M" {
		t.Errorf("kilo(1500000) = %q, want 1.50M", got)
	}
	none := tokenBar(nil, 1000)
	if !strings.Contains(none, "ctx …") {
		t.Errorf("no reading = %q", none)
	}
	over := 1000
	if got := tokenBar(&over, 1000); !strings.Contains(got, "LIMIT REACHED") {
		t.Errorf("at the limit = %q", got)
	}
	half := 500
	rendered := tokenBar(&half, 1000)
	// Python's f"{0.5:.0f}" is "0" (round-half-even), so 500 of 1000 reads "0k/1k" - the bar and the
	// percent are what carry the reading.
	if !strings.Contains(rendered, "50%") || !strings.Contains(rendered, "0k/1k to compact") {
		t.Errorf("half = %q", rendered)
	}
	if got := strings.Count(rendered, "█"); got != barCells/2 {
		t.Errorf("filled cells = %d, want %d", got, barCells/2)
	}
}

// fakeExec answers the commands the verbs issue, keyed by the whole command line.
type fakeExec struct {
	mu         sync.Mutex
	calls      [][]string
	responses  map[string]string
	prefixes   [][2]string
	failures   map[string]error
	working    map[string]bool
	splitCount int
}

func newFakeExec(t *testing.T) *fakeExec {
	t.Helper()
	fake := &fakeExec{responses: map[string]string{}, failures: map[string]error{}, working: map[string]bool{}}
	previousExec, previousPath := execCommand, lookPath
	execCommand = func(_ context.Context, name string, args ...string) (string, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		fake.mu.Lock()
		fake.calls = append(fake.calls, append([]string{name}, args...))
		failure := fake.failures[key]
		response := fake.responses[key]
		if response == "" && failure == nil {
			for _, prefix := range fake.prefixes {
				if strings.HasPrefix(key, prefix[0]) {
					response = prefix[1]
					break
				}
			}
		}
		fake.mu.Unlock()
		if failure != nil {
			return "", failure
		}
		return response, nil
	}
	lookPath = func(name string) (string, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if fake.working[name] {
			return "/usr/bin/" + name, nil
		}
		return "", fmt.Errorf("%s: not found", name)
	}
	t.Cleanup(func() { execCommand, lookPath = previousExec, previousPath })
	return fake
}

func (f *fakeExec) on(key, response string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[key] = response
}

// onPrefix answers every command starting with prefix, for the calls whose argument varies (a pane
// split names the pane it splits).
func (f *fakeExec) onPrefix(prefix, response string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefixes = append(f.prefixes, [2]string{prefix, response})
}

// newPane answers every split with a fresh pane id, the way herdr does.
func (f *fakeExec) newPane() {
	f.onPrefix("herdr pane split", `{"result": {"pane": {"pane_id": "w25:new"}}}`)
}

func (f *fakeExec) callsOf(name string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := [][]string{}
	for _, call := range f.calls {
		if len(call) > 0 && call[0] == name {
			out = append(out, call)
		}
	}
	return out
}

// herdrPanes answers the calls a grid/inputs run makes, the way the Python's fake_herdr does.
const fakeGridWorkspace = "w25"

func (f *fakeExec) answerHerdr(panes []map[string]any) {
	f.working["herdr"] = true
	f.on("herdr workspace list", `{"result": {"workspaces": [{"workspace_id": "`+fakeGridWorkspace+`", "label": "r grid"}]}}`)
	f.on("herdr workspace create --cwd "+workingDirKey()+" --label r grid",
		`{"result": {"root_pane": {"pane_id": "`+fakeGridWorkspace+`:p1"}}}`)
	encoded, _ := json.Marshal(map[string]any{"result": map[string]any{"panes": panes}})
	f.on("herdr pane list", string(encoded))
	f.on("herdr workspace close "+fakeGridWorkspace, `{"result": {}}`)
	f.newPane()
}

func workingDirKey() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

// seatsFromTmux makes `tmux list-sessions` answer with one session per seat.
func (f *fakeExec) seatsFromTmux(rig string, seats []string) {
	names := []string{}
	for _, seat := range seats {
		names = append(names, rig+"-"+seat+"@"+rig)
	}
	f.on("tmux list-sessions -F #{session_name}", strings.Join(names, "\n"))
}

func paneEntry(label string) map[string]any {
	return map[string]any{"pane_id": fakeGridWorkspace + ":" + label, "workspace_id": fakeGridWorkspace, "label": label}
}

func sortedCalls(calls [][]string) []string {
	out := []string{}
	for _, call := range calls {
		out = append(out, strings.Join(call, " "))
	}
	sort.Strings(out)
	return out
}
