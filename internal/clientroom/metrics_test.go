package clientroom

import (
	"encoding/json"
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
	if len(grown) != len(events)+1 {
		t.Fatalf("third run appended %d lines, want 1", len(grown)-len(events))
	}
	last := grown[len(grown)-1]
	if last["activity"] != "test/build" || last["first_tool"] != "bash" || last["tools"].(float64) != 1 || last["task"] != openTaskID || last["model"] != "m" {
		t.Errorf("new turn: %v", last)
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
