package clientroom

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

func TestClassifyBash(t *testing.T) {
	cases := map[string]string{
		`4genteam room queue create room-a --to x --summary y`: actQueueWr,
		`cd /repo && 4genteam room queue claim room-a qitem-1`: actQueueWr,
		`4genteam room queue list room-a`:                      actQueuePoll,
		`cd /repo/agenthub_go && go test ./...`:                actTestBuild,
		`cargo test --offline`:                                 actTestBuild,
		`echo "=== x ==="; grep -rn foo .`:                     actMeasuring,
		`export A=b; git log --oneline -5`:                     actMeasuring,
		`FOO=1 wc -l file`:                                     actMeasuring,
		`sha256sum a`:                                          actMeasuring,
		`git rev-parse HEAD`:                                   actMeasuring,
		`git commit -m x`:                                      actOther,
		`4genteam send lead hi`:                                actOther,
	}
	for command, want := range cases {
		if got := classifyBash(command); got != want {
			t.Errorf("classifyBash(%q) = %s, want %s", command, got, want)
		}
	}
}

func TestClassifyToolCall(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"edit", map[string]any{"path": "/r/a.go"}, actCodeEdit},
		{"write", map[string]any{"path": "/r/CHANGELOG/x.go"}, actDocEdit},
		{"edit", map[string]any{"path": "/r/README.md"}, actDocEdit},
		{"write", map[string]any{"path": "/r/ai_docs/x.txt"}, actDocEdit},
		{"read", map[string]any{"path": "/r/a.go"}, actRead},
		{"grep", map[string]any{}, actMeasuring},
		{"todo", map[string]any{}, actOther},
		{"bash", map[string]any{"command": "go vet ./..."}, actTestBuild},
	}
	for _, c := range cases {
		if got := classifyToolCall(c.tool, c.args); got != c.want {
			t.Errorf("classifyToolCall(%s, %v) = %s, want %s", c.tool, c.args, got, c.want)
		}
	}
}

func TestParseSince(t *testing.T) {
	for raw, want := range map[string]time.Duration{"24h": 24 * time.Hour, "90m": 90 * time.Minute, "7d": 168 * time.Hour} {
		if got, err := parseSince(raw); err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "x", "-1h", "0d", "dd"} {
		if _, err := parseSince(raw); err == nil {
			t.Errorf("parseSince(%q) accepted a bad window", raw)
		}
	}
}

func TestParseGitLogReadsSeatTrailerAndDocsOnly(t *testing.T) {
	out := recordSep + "agenthub--room-a--go-dev" + fieldSep + "\n\nx.go\nCHANGELOG/a.md\n" +
		recordSep + "agenthub--room-a--lead" + fieldSep + "\n\nREADME.md\nai_docs/y.txt\n" +
		recordSep + "agenthub--other--lead" + fieldSep + "\n\nz.md\n" +
		recordSep + fieldSep + "\n"
	got := parseGitLog(out, "room-a")
	want := []commitRecord{{"go-dev", false}, {"lead", true}, {"", true}, {"", false}}
	if len(got) != len(want) {
		t.Fatalf("got %d commits, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("commit %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFindStaleTasksKeepsOnlyOldOpenTasksOldestFirst(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	answers := map[string]map[string]any{
		"todo": {"tasks": []any{
			map[string]any{"id": "t1", "title": "old todo", "updated_at": "2026-10-14T12:00:00Z"},
			map[string]any{"id": "t2", "title": "fresh", "updated_at": "2026-10-16T08:00:00Z"},
		}},
		"in_progress": {"tasks": []any{
			map[string]any{"id": "t3", "title": "older", "updated_at": "2026-10-13T12:00:00Z"},
			map[string]any{"id": "t4", "title": "no stamp"},
		}},
	}
	stale, err := findStaleTasks(func(status string) (map[string]any, error) { return answers[status], nil }, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 2 || stale[0].ID != "t3" || stale[0].Status != "in_progress" || stale[1].ID != "t1" {
		t.Fatalf("stale = %+v, want t3 then t1", stale)
	}
	if _, err := findStaleTasks(func(string) (map[string]any, error) { return nil, fmt.Errorf("down") }, now); err == nil {
		t.Fatal("a failed fetch must be an error, not an empty list")
	}
}

// reportFixture builds a room directory with one seat, a git repository with two commits in the window and
// three session turns (one old, outside the window), and returns the room directory and the repository.
func reportFixture(t *testing.T, now time.Time) (roomDir, repo string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	commit := func(file, message string) {
		if err := os.WriteFile(filepath.Join(repo, file), []byte(message), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", file)
		git("commit", "-q", "-m", message)
	}
	commit("a.go", "feat: code\n\nSeat: agenthub--room-a--go-dev")
	commit("notes.md", "docs: note\n\nSeat: agenthub--room-a--go-dev")

	roomDir = filepath.Join(root, "rooms", "room-a")
	seatDir := filepath.Join(roomDir, "seats", "go-dev")
	if err := os.MkdirAll(filepath.Join(seatDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	turn := func(at time.Time, cost float64, parts string) string {
		return fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[%s],"usage":{"output":10,"cost":{"total":%v}}}}`,
			at.Format(time.RFC3339Nano), parts, cost)
	}
	edit := `{"type":"toolCall","name":"edit","arguments":{"path":"/r/a.go"}}`
	grep := `{"type":"toolCall","name":"bash","arguments":{"command":"grep -rn x ."}}`
	lines := []string{
		fmt.Sprintf(`{"type":"session","cwd":%q}`, repo),
		turn(now.Add(-time.Hour), 3, `{"type":"thinking"},`+edit),
		turn(now.Add(-2*time.Hour), 1, grep),
		turn(now.Add(-3*time.Hour), 1, `{"type":"text","text":"hi"}`),
		turn(now.Add(-48*time.Hour), 100, edit),
		"not json",
		"",
	}
	if err := os.WriteFile(filepath.Join(seatDir, "sessions", "s.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := fmt.Sprintf(`{"at":%q,"outcome":"blocked"}`+"\n"+`{"at":%q,"outcome":"ok"}`+"\n"+`{"at":%q,"outcome":"blocked"}`+"\n",
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-72*time.Hour).Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(seatDir, "tool-calls.jsonl"), []byte(calls), 0o644); err != nil {
		t.Fatal(err)
	}
	return roomDir, repo
}

func TestBuildReportMeasuresTheWindowOnly(t *testing.T) {
	now := time.Now().UTC()
	roomDir, repo := reportFixture(t, now)
	rows := []clientruntime.QueueRow{
		{SourceSession: "agenthub--room-a--go-dev", Priority: "high", Body: "abcd", TsCreated: now.Add(-time.Hour).Format(time.RFC3339Nano)},
		{SourceSession: "agenthub--room-a--go-dev", Priority: "normal", Body: "ab", TsCreated: now.Add(-time.Hour).Format(time.RFC3339Nano)},
		{SourceSession: "agenthub--room-a--go-dev", Priority: "high", Body: "old", TsCreated: now.Add(-72 * time.Hour).Format(time.RFC3339Nano)},
	}
	report, err := buildReport("room-a", roomDir, 24*time.Hour, now,
		func() ([]clientruntime.QueueRow, error) { return rows, nil },
		func() ([]staleTask, error) { return nil, fmt.Errorf("no hub") })
	if err != nil {
		t.Fatal(err)
	}
	if report.Repo != repo || report.Sources["git"] != "ok" || report.Sources["queue"] != "ok" || !strings.HasPrefix(report.Sources["tasks"], "unavailable (no hub") {
		t.Fatalf("repo/sources = %q %v", report.Repo, report.Sources)
	}
	seat := report.Total
	if seat.Turns != 3 || seat.Cost != 5 || seat.OutputTokens != 30 || seat.Blocked != 1 {
		t.Errorf("turns/cost/tokens/blocked = %d %v %d %d, want 3 5 30 1", seat.Turns, seat.Cost, seat.OutputTokens, seat.Blocked)
	}
	if seat.ActivityPct[actCodeEdit] != 60 || seat.ActivityPct[actMeasuring] != 20 || seat.ActivityPct[actTextOnly] != 20 {
		t.Errorf("activity pct = %v", seat.ActivityPct)
	}
	if seat.Commits != 2 || seat.DocsOnly != 1 || seat.QueueRows != 2 || seat.QueueHigh != 1 || seat.BodyChars != 6 {
		t.Errorf("commits/docs/rows/high/chars = %d %d %d %d %d", seat.Commits, seat.DocsOnly, seat.QueueRows, seat.QueueHigh, seat.BodyChars)
	}
	results := map[string]string{}
	for _, target := range report.Targets {
		results[target.Name] = target.Result
	}
	want := map[string]string{"docs-only commits %": "FAIL", "queue rows/day": "PASS", "high-priority rows %": "FAIL", "measuring cost %": "PASS", "code-edit cost %": "PASS"}
	for name, verdict := range want {
		if results[name] != verdict {
			t.Errorf("target %q = %s, want %s", name, results[name], verdict)
		}
	}
}

func TestBuildReportMarksAnUnreadableQueueUnavailableNotZero(t *testing.T) {
	now := time.Now().UTC()
	roomDir, _ := reportFixture(t, now)
	report, err := buildReport("room-a", roomDir, 24*time.Hour, now,
		func() ([]clientruntime.QueueRow, error) { return nil, fmt.Errorf("daemon down") },
		func() ([]staleTask, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Sources["queue"]; got != "unavailable (daemon down)" {
		t.Fatalf("queue source = %q", got)
	}
	for _, target := range report.Targets {
		if (target.Name == "queue rows/day" || target.Name == "high-priority rows %") && target.Result != "n/a" {
			t.Errorf("target %q = %s, want n/a when the queue was not read", target.Name, target.Result)
		}
	}
}

func TestRoomReportEndToEndAgainstTheDaemon(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "")
	now := time.Now().UTC()
	roomDir, _ := reportFixture(t, now)
	row := `{"qitem_id":"qitem-1","state":"pending","priority":"high","summary":"s","body":"SECRET-BODY","source_session":"agenthub--room-a--go-dev","ts_created":"` +
		now.Add(-time.Hour).Format(time.RFC3339Nano) + `"}`
	daemon := newFakeDaemon(t, map[string]string{"QueueList": okData("[" + row + "]")})
	daemon.stateDir = filepath.Dir(filepath.Dir(roomDir))

	code, stdout, stderr := runRoom(t, append(daemon.args("report", "room-a"), "--since", "24h")...)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"sources: git ok; queue ok; tasks unavailable (AGENTHUB_URL is not set)", "go-dev", "tasks: unavailable (AGENTHUB_URL is not set)", "FAIL docs-only commits %"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "SECRET-BODY") {
		t.Errorf("a queue body was printed:\n%s", stdout)
	}

	code, stdout, stderr = runRoom(t, append(daemon.args("report", "room-a"), "--json")...)
	var decoded Report
	if code != clientcmd.ExitOK || json.Unmarshal([]byte(stdout[strings.Index(stdout, "{"):]), &decoded) != nil || decoded.Room != "room-a" {
		t.Fatalf("--json: exit %d, stderr %q, stdout %q", code, stderr, stdout)
	}
}

func TestRoomReportRefusesBadInput(t *testing.T) {
	daemon := newFakeDaemon(t, map[string]string{})
	for _, args := range [][]string{
		append(daemon.args("report", "room-a"), "--since", "soon"),
		daemon.args("report", "no-such-room"),
		daemon.args("report"),
	} {
		if code, _, _ := runRoom(t, args...); code != clientcmd.ExitRoomRefused {
			t.Errorf("%v: exit %d, want %d", args, code, clientcmd.ExitRoomRefused)
		}
	}
}
