package clientroom

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

func TestTargetClassKeepsTheVerbAndNothingElse(t *testing.T) {
	cases := []struct{ tool, target, want string }{
		{"bash", "cd /r && git commit -m 'TOKEN=abc'", "git commit"},
		{"bash", "API_KEY=abc curl -H secret http://x", "curl"},
		{"bash", "/usr/bin/go test ./...", "go test"},
		{"bash", "4genteam room queue create --body SECRET", "4genteam room"},
		{"bash", "$(cat .env)", "other"},
		{"mcp__agenthub_http_manage_task", "mcp:manage_task.update", "mcp:manage_task.update"},
		{"edit", "/r/README.md", "doc"},
		{"read", "/r/a.go", "code"},
		{"bash", "", ""},
	}
	for _, c := range cases {
		if got := targetClass(c.tool, c.target); got != c.want {
			t.Errorf("targetClass(%s, %q) = %q, want %q", c.tool, c.target, got, c.want)
		}
	}
}

func readEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line is not JSON: %q", line)
		}
		events = append(events, event)
	}
	return events
}

func TestRoomMetricsIsIncrementalIdempotentAndRedacted(t *testing.T) {
	now := time.Now().UTC()
	roomDir, _ := reportFixture(t, now)
	seatDir := filepath.Join(roomDir, "seats", "go-dev")
	at := now.Add(-time.Hour).Format(time.RFC3339Nano)
	extra := `{"at":"` + at + `","seat":"go-dev","session":"s1","tool":"bash","target":"export TOKEN=zzz; git commit -m SECRET","outcome":"ok","ms":7,"bytes_out":12}` + "\n" +
		`{"at":"` + at + `","seat":"go-dev","tool":"bash","target":"git push","outcome":"blocked","rule":"push"}` + "\n"
	file, err := os.OpenFile(filepath.Join(seatDir, "tool-calls.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(extra)
	file.Close()
	row := `{"qitem_id":"qitem-1","state":"pending","priority":"high","tags":["a","b"],"summary":"sum","body":"SECRET-BODY","source_session":"agenthub--room-a--go-dev","destination_session":"agenthub--room-a--lead","ts_created":"` + at + `"}`
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[" + row + "]")})
	daemon.stateDir = filepath.Dir(filepath.Dir(roomDir))
	args := append(daemon.args("metrics", "room-a"), "--since", "24h")

	// --print writes lines and persists nothing.
	code, stdout, stderr := runRoom(t, append(args, "--print")...)
	if code != clientcmd.ExitOK || strings.Count(stdout, "\n") < 8 {
		t.Fatalf("--print: exit %d, stderr %q, stdout %q", code, stderr, stdout)
	}
	if _, err := os.Stat(filepath.Join(roomDir, "metrics")); !os.IsNotExist(err) {
		t.Fatalf("--print created the metrics dir: %v", err)
	}

	if code, _, stderr = runRoom(t, args...); code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	out := filepath.Join(roomDir, "metrics", "go-dev.jsonl")
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("metrics file: %v %v", err, info)
	}
	events := readEvents(t, out)
	count := map[string]int{}
	var cost float64
	for _, event := range events {
		count[event["event"].(string)]++
		if event["schema"].(float64) != 1 || event["room"] != "room-a" || event["seat"] != "go-dev" {
			t.Errorf("bad base fields: %v", event)
		}
		switch event["event"] {
		case "turn":
			cost += event["cost_usd"].(float64)
		case "tool":
			if event["tool"] == "bash" && event["outcome"] == "ok" && (event["target_class"] != "git commit" || event["session"] != "s1") {
				t.Errorf("tool event: %v", event)
			}
			if event["outcome"] == "blocked" && event["tool"] == "bash" && event["blocked_rule"] != "push" {
				t.Errorf("blocked event: %v", event)
			}
		case "queue_row":
			if event["body_chars"].(float64) != 11 || event["summary_chars"].(float64) != 3 || event["to"] != "lead" || event["tag"] != "a,b" {
				t.Errorf("queue_row: %v", event)
			}
		}
	}
	if count["turn"] != 3 || count["tool"] != 4 || count["commit"] != 2 || count["queue_row"] != 1 {
		t.Errorf("event counts = %v", count)
	}
	if cost != 5 {
		t.Errorf("turn cost = %v, want 5 (the 48h-old turn is outside the window)", cost)
	}
	raw, _ := os.ReadFile(out)
	for _, forbidden := range []string{"SECRET", "TOKEN", "zzz", "KEY="} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the metrics log contains %q", forbidden)
		}
	}

	// A second run appends nothing; a new transcript line is picked up alone.
	if code, stdout, _ = runRoom(t, args...); code != clientcmd.ExitOK || !strings.Contains(stdout, "0 new lines") {
		t.Fatalf("second run: exit %d, stdout %q", code, stdout)
	}
	if again := readEvents(t, out); len(again) != len(events) {
		t.Fatalf("second run appended %d lines", len(again)-len(events))
	}
	session, _ := os.OpenFile(filepath.Join(seatDir, "sessions", "s.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	session.WriteString(`{"type":"message","timestamp":"` + now.Format(time.RFC3339Nano) + `","message":{"role":"assistant","model":"m","content":[{"type":"toolCall","name":"bash","arguments":{"command":"go test ./...","task_id":"` + openTaskID + `"}}],"usage":{"cost":{"total":2}}}}` + "\n")
	session.Close()
	if code, _, _ = runRoom(t, args...); code != clientcmd.ExitOK {
		t.Fatal("third run failed")
	}
	grown := readEvents(t, out)
	if len(grown) != len(events)+2 {
		t.Fatalf("third run appended %d lines, want 2 (the turn and the block it followed)", len(grown)-len(events))
	}
	var last, retry map[string]any
	for _, event := range grown[len(events):] {
		if event["event"] == "turn" {
			last = event
			continue
		}
		retry = event
	}
	if last["activity"] != "test/build" || last["first_tool"] != "bash" || last["tools"].(float64) != 1 || last["task"] != openTaskID || last["model"] != "m" {
		t.Errorf("new turn: %v", last)
	}
	if retry["event"] != "block_retry" || retry["rule"] != "push" || retry["cost_usd"].(float64) != 2 ||
		retry["blocked_ts"] != at || retry["seat"] != "go-dev" {
		t.Errorf("block retry: %v", retry)
	}
}

func TestRoomMetricsRecordsARefusedCommitWithPathClasses(t *testing.T) {
	now := time.Now().UTC()
	roomDir, repo := reportFixture(t, now)
	spool := filepath.Join(repo, ".git", "parked-diffs", "refusals.jsonl")
	if err := os.MkdirAll(filepath.Dir(spool), 0o755); err != nil {
		t.Fatal(err)
	}
	at := now.Add(-30 * time.Minute)
	// The wrapper writes `datetime.now(timezone.utc).isoformat()`: microseconds and a +00:00 offset.
	line := fmt.Sprintf(`{"id":"r1","ts":%q,"seat":"go-dev","session":"s1","rule":"docs_only","docs":2,"code":0}`+"\n",
		at.Format("2006-01-02T15:04:05.000000+00:00"))
	if err := os.WriteFile(spool, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[]")})
	daemon.stateDir = filepath.Dir(filepath.Dir(roomDir))
	args := daemon.args("metrics", "room-a")
	if code, _, stderr := runRoom(t, args...); code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	out := filepath.Join(roomDir, "metrics", "go-dev.jsonl")
	var refused map[string]any
	for _, event := range readEvents(t, out) {
		if event["event"] == "commit_refused" {
			refused = event
		}
	}
	if refused == nil {
		t.Fatal("no commit_refused event")
	}
	if refused["rule"] != "docs_only" || refused["docs"].(float64) != 2 || refused["code"].(float64) != 0 ||
		refused["seat"] != "go-dev" || refused["session"] != "s1" ||
		refused["ts"] != at.Truncate(time.Microsecond).UTC().Format(time.RFC3339Nano) {
		t.Errorf("commit_refused: %v", refused)
	}
	// The spool line carries classes, and the metrics line must not carry the paths it was computed from.
	raw, _ := os.ReadFile(out)
	for _, forbidden := range []string{"CHANGELOG", "docs/", "notes.md"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the metrics log contains %q", forbidden)
		}
	}
	// Idempotent by refusal id: the spool is re-read whole, and nothing is appended twice.
	if code, stdout, _ := runRoom(t, args...); code != clientcmd.ExitOK || !strings.Contains(stdout, "0 new lines") {
		t.Fatalf("second run: exit %d, stdout %q", code, stdout)
	}
}

func TestRoomMetricsAttributesTheTurnABlockCosts(t *testing.T) {
	now := time.Now().UTC()
	roomDir, _ := reportFixture(t, now)
	seatDir := filepath.Join(roomDir, "seats", "go-dev")
	blockAt := now.Add(-10 * time.Minute)
	calls := fmt.Sprintf(`{"at":%q,"seat":"go-dev","session":"s1","tool":"bash","target":"git push","outcome":"blocked","rule":"push"}`+"\n",
		blockAt.Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(seatDir, "tool-calls.jsonl"), []byte(calls), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[]")})
	daemon.stateDir = filepath.Dir(filepath.Dir(roomDir))
	args := daemon.args("metrics", "room-a")
	if code, _, stderr := runRoom(t, args...); code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	out := filepath.Join(roomDir, "metrics", "go-dev.jsonl")
	for _, event := range readEvents(t, out) {
		if event["event"] == "block_retry" {
			t.Fatalf("a block with no turn after it was attributed: %v", event)
		}
	}
	// The retry is the seat's first turn after the block; it is attributed on the next run, from the cursor.
	turn := fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","model":"m","content":[{"type":"toolCall","name":"bash","arguments":{"command":"go test ./..."}},{"type":"toolCall","name":"read","arguments":{"path":"/r/a.go"}}],"usage":{"cost":{"total":4}}}}`+"\n",
		now.Add(-time.Minute).Format(time.RFC3339Nano))
	file, err := os.OpenFile(filepath.Join(seatDir, "sessions", "s.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(turn)
	file.Close()
	if code, _, stderr := runRoom(t, args...); code != clientcmd.ExitOK {
		t.Fatalf("second run: exit %d, stderr %q", code, stderr)
	}
	var retry map[string]any
	for _, event := range readEvents(t, out) {
		if event["event"] == "block_retry" {
			retry = event
		}
	}
	if retry == nil {
		t.Fatal("no block_retry after the turn that followed the block")
	}
	if retry["rule"] != "push" || retry["blocked_ts"] != blockAt.Format(time.RFC3339Nano) ||
		retry["cost_usd"].(float64) != 4 || retry["tools"].(float64) != 2 || retry["session"] != "s" {
		t.Errorf("block_retry: %v", retry)
	}
	// A turn is one retry: the block is consumed, and a further turn does not charge it again.
	later := fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","model":"m","content":[{"type":"text","text":"next"}],"usage":{"cost":{"total":9}}}}`+"\n",
		now.Format(time.RFC3339Nano))
	if file, err := os.OpenFile(filepath.Join(seatDir, "sessions", "s.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
		t.Fatal(err)
	} else {
		file.WriteString(later)
		file.Close()
	}
	if code, _, stderr := runRoom(t, args...); code != clientcmd.ExitOK {
		t.Fatalf("third run: exit %d, stderr %q", code, stderr)
	}
	blocks := 0
	for _, event := range readEvents(t, out) {
		if event["event"] == "block_retry" {
			blocks++
		}
	}
	if blocks != 1 {
		t.Fatalf("a later turn was charged to the same block: %d block_retry rows", blocks)
	}
	if code, stdout, _ := runRoom(t, args...); code != clientcmd.ExitOK || !strings.Contains(stdout, "0 new lines") {
		t.Fatalf("fourth run: exit %d, stdout %q", code, stdout)
	}
}

func TestRoomMetricsSeatFilterAndHelp(t *testing.T) {
	now := time.Now().UTC()
	roomDir, _ := reportFixture(t, now)
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[]")})
	daemon.stateDir = filepath.Dir(filepath.Dir(roomDir))
	if code, stdout, _ := runRoom(t, append(daemon.args("metrics", "room-a"), "--seat", "lead", "--print")...); code != clientcmd.ExitOK || strings.TrimSpace(stdout) != "" && !strings.HasPrefix(stdout, "room:") {
		t.Errorf("--seat lead: exit %d, stdout %q", code, stdout)
	}
	if code, stdout, _ := runRoom(t, "metrics", "--help"); code != clientcmd.ExitOK || !strings.Contains(stdout, "cost per task") {
		t.Errorf("--help: exit %d", code)
	}
}
