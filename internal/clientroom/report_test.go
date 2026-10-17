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
	const id = "0b6c1c4e-8f1d-4f0e-9a52-0123456789ab"
	out := recordSep + "s1" + fieldSep + "t1" + fieldSep + "agenthub--room-a--go-dev" + fieldSep + id + fieldSep + "2026-10-16T10:00:00Z" + fieldSep + "\n\nx.go\nCHANGELOG/a.md\n" +
		recordSep + "s2" + fieldSep + "t2" + fieldSep + "agenthub--room-a--lead" + fieldSep + "not-a-uuid" + fieldSep + "2026-10-16T10:00:00Z" + fieldSep + "\n\nREADME.md\nai_docs/y.txt\n" +
		recordSep + "s3" + fieldSep + "t3" + fieldSep + "agenthub--other--lead" + fieldSep + fieldSep + "2026-10-16T10:00:00Z" + fieldSep + "\n\nz.md\n" +
		recordSep + "s4" + fieldSep + "t4" + fieldSep + fieldSep + fieldSep + "2026-10-16T10:00:00Z" + fieldSep + "\n"
	got := parseGitLog(out, "room-a")
	want := []commitRecord{{Seat: "go-dev", SHA: "s1", Title: "t1", TaskID: id}, {Seat: "lead", DocsOnly: true, SHA: "s2", Title: "t2"}, {DocsOnly: true, SHA: "s3", Title: "t3"}, {SHA: "s4", Title: "t4"}}
	if len(got) != len(want) {
		t.Fatalf("got %d commits, want %d: %v", len(got), len(want), got)
	}
	if got[0].Files != 2 || !got[0].Time.Equal(time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("commit 0 has %d files at %v", got[0].Files, got[0].Time)
	}
	for i := range want {
		plain := got[i]
		plain.Time, plain.Files = time.Time{}, 0
		if plain != want[i] {
			t.Errorf("commit %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFindStaleTasksKeepsOnlyOldOpenTasksOldestFirst(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	lists := map[string]map[string]any{
		"todo":        {"tasks": []any{map[string]any{"id": "t1"}, map[string]any{"id": "t2"}}},
		"in_progress": {"tasks": []any{map[string]any{"id": "t3"}}},
	}
	// updated_at is fresh on every task: the sweeper rewrites it, so only created_at may decide.
	details := map[string]map[string]any{
		"t1": {"task": map[string]any{"title": "old todo", "created_at": "2026-10-14T12:00:00Z", "updated_at": "2026-10-16T11:00:00Z"}},
		"t2": {"task": map[string]any{"title": "fresh", "created_at": "2026-10-16T08:00:00Z"}},
		"t3": {"task": map[string]any{"title": "older", "created_at": "2026-10-13T12:00:00.123456+00:00"}},
	}
	list := func(status string) (map[string]any, error) { return lists[status], nil }
	get := func(id string) (map[string]any, error) { return details[id], nil }
	stale, err := findStaleTasks(list, get, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 2 || stale[0].ID != "t3" || stale[0].Status != "in_progress" || stale[1].ID != "t1" {
		t.Fatalf("stale = %+v, want t3 then t1", stale)
	}
	if _, err := findStaleTasks(func(string) (map[string]any, error) { return nil, fmt.Errorf("down") }, get, now); err == nil {
		t.Fatal("a failed list must be an error, not an empty list")
	}
	noStamp := func(string) (map[string]any, error) { return map[string]any{"task": map[string]any{}}, nil }
	if _, err := findStaleTasks(list, noStamp, now); err == nil {
		t.Fatal("a task without created_at must be an error, not silently skipped")
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

func noTaskStatus(string) (string, error) { return "", fmt.Errorf("no hub") }

const (
	openTaskID   = "11111111-1111-4111-8111-111111111111"
	closedTaskID = "22222222-2222-4222-8222-222222222222"
)

func TestBuildReportListsTasksCommittedAgainstButStillOpen(t *testing.T) {
	now := time.Now().UTC()
	roomDir, repo := reportFixture(t, now)
	for i, message := range []string{
		"feat: first\n\nTask: " + openTaskID,
		"fix: second\n\nTask: " + openTaskID,
		"feat: closed work\n\nTask: " + closedTaskID,
	} {
		file := fmt.Sprintf("t%d.go", i)
		if err := os.WriteFile(filepath.Join(repo, file), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", file}, {"commit", "-q", "-m", message}} {
			cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	statuses := map[string]string{openTaskID: "in_progress", closedTaskID: "done"}
	report, err := buildReport("room-a", roomDir, 24*time.Hour, now,
		func() ([]clientruntime.QueueRow, error) { return nil, nil },
		func() ([]staleTask, error) { return nil, nil },
		func(id string) (string, error) { return statuses[id], nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(report.CommittedOpen) != 1 || report.CommittedOpen[0].ID != openTaskID || report.CommittedOpen[0].Status != "in_progress" ||
		report.CommittedOpen[0].Commits != 2 || report.CommittedOpen[0].Title != "fix: second" || len(report.CommittedOpen[0].LastSHA) != 40 {
		t.Fatalf("committed but open = %+v", report.CommittedOpen)
	}
	if report.NoTrailer != 2 {
		t.Errorf("commits without trailer = %d, want the 2 fixture commits", report.NoTrailer)
	}
	var out strings.Builder
	report.renderText(&out)
	for _, want := range []string{"committed but still open: 1", openTaskID, "commits without Task trailer: 2", "FAIL tasks closed after commit"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), closedTaskID) {
		t.Errorf("a closed task was listed:\n%s", out.String())
	}

	statuses[openTaskID] = "cancelled"
	report, _ = buildReport("room-a", roomDir, 24*time.Hour, now,
		func() ([]clientruntime.QueueRow, error) { return nil, nil },
		func() ([]staleTask, error) { return nil, nil },
		func(id string) (string, error) { return statuses[id], nil })
	if len(report.CommittedOpen) != 0 {
		t.Errorf("all tasks closed, still listed: %+v", report.CommittedOpen)
	}
	report, _ = buildReport("room-a", roomDir, 24*time.Hour, now,
		func() ([]clientruntime.QueueRow, error) { return nil, nil },
		func() ([]staleTask, error) { return nil, nil }, noTaskStatus)
	out.Reset()
	report.renderText(&out)
	if !strings.Contains(out.String(), "committed but still open: unavailable (no hub)") || report.Targets[len(report.Targets)-1].Result != "n/a" {
		t.Errorf("unreachable hub not marked unavailable / n/a:\n%s", out.String())
	}
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
		func() ([]staleTask, error) { return nil, fmt.Errorf("no hub") }, noTaskStatus)
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
	want := map[string]string{"docs-only commits %": "FAIL", "queue rows/day": "PASS", "high-priority rows %": "FAIL", "measuring cost %": "PASS", "code-edit cost %": "diag"}
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
		func() ([]staleTask, error) { return nil, nil }, noTaskStatus)
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

// A TURN THAT READS AND THEN EDITS IS A CODE-EDIT TURN. The old reader scored a turn by its first tool call
// only, and every edit is preceded by a read, so editing was under-counted by exactly the read that found the
// file (seats/architect/RULING-code-edit-share-target.md). The docs turn is the boundary: its first call is a
// read and neither call edits CODE, so it stays a read even under the any-call rule.
func TestAReadThenEditTurnCountsAsCodeEdit(t *testing.T) {
	seatDir := filepath.Join(t.TempDir(), "seat")
	if err := os.MkdirAll(filepath.Join(seatDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	turn := func(cost float64, parts string) string {
		return fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[%s],"usage":{"output":1,"cost":{"total":%v}}}}`,
			now.Add(-time.Hour).Format(time.RFC3339Nano), parts, cost)
	}
	readThenEdit := `{"type":"toolCall","name":"read","arguments":{"path":"/r/a.go"}},` +
		`{"type":"toolCall","name":"edit","arguments":{"path":"/r/a.go"}}`
	readThenDoc := `{"type":"toolCall","name":"read","arguments":{"path":"/r/notes.md"}},` +
		`{"type":"toolCall","name":"edit","arguments":{"path":"/r/notes.md"}}`
	grep := `{"type":"toolCall","name":"bash","arguments":{"command":"grep -rn x ."}}`
	session := strings.Join([]string{
		turn(6, readThenEdit),
		turn(1, readThenDoc),
		turn(3, grep),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(seatDir, "sessions", "s.jsonl"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}

	stats := newSeatStats("dev")
	if _, err := readSessions(seatDir, now.Add(-24*time.Hour), stats); err != nil {
		t.Fatal(err)
	}
	stats.finish()
	if got := stats.ActivityPct[actCodeEdit]; got != 60 {
		t.Errorf("code-edit share = %.1f%%, want 60: the 6 USD read-then-edit turn is an edit", got)
	}
	if got := stats.CodeEditFirstCallPct; got != 0 {
		t.Errorf("first-call code-edit share = %.1f%%, want 0: neither turn edits on its first call", got)
	}
	if got := stats.ActivityPct[actRead]; got != 10 {
		t.Errorf("read share = %.1f%%, want 10: only the docs turn keeps its first-call class", got)
	}
}

// writeSpec writes one room spec naming each seat's type: the report reads it to know which seats EDIT.
func writeSpec(t *testing.T, roomDir string, seats [][2]string) {
	t.Helper()
	entries := make([]string, 0, len(seats))
	for _, seat := range seats {
		entries = append(entries, fmt.Sprintf(`{"seat_key":%q,"seat_type":%q,"runtime":"omp"}`, seat[0], seat[1]))
	}
	spec := fmt.Sprintf(`{"room":%q,"seats":[%s],"links":[]}`, filepath.Base(roomDir), strings.Join(entries, ","))
	if err := os.WriteFile(filepath.Join(roomDir, "spec.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSeatTurns writes one session file for a seat with one turn per given cost, each a lone measuring call,
// except a cost of -1 which writes a code-edit turn - the shape the tests below need to set a share.
func writeSeatTurns(t *testing.T, roomDir, seat string, costs []float64) {
	t.Helper()
	seatDir := filepath.Join(roomDir, "seats", seat)
	if err := os.MkdirAll(filepath.Join(seatDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var lines []string
	for _, cost := range costs {
		parts := `{"type":"toolCall","name":"bash","arguments":{"command":"grep -rn x ."}}`
		if cost < 0 {
			parts = `{"type":"toolCall","name":"edit","arguments":{"path":"/r/a.go"}}`
			cost = -cost
		}
		lines = append(lines, fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[%s],"usage":{"output":1,"cost":{"total":%v}}}}`,
			now.Add(-time.Hour).Format(time.RFC3339Nano), parts, cost))
	}
	if err := os.WriteFile(filepath.Join(seatDir, "sessions", "s.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// THE ROOM LINE IS A DIAGNOSTIC; THE EDITING SEATS ARE THE TARGET. Three of five seats may not edit code and
// hold most of a gated room's cost, so a room-wide floor fails by construction (the ruling's arithmetic). The
// test reads the rendered report, not the struct: what a seat sees is a `diag` room line and PASS/FAIL per
// developer seat.
func TestTheRoomCodeEditLineIsDiagnosticWhileEachEditingSeatIsGated(t *testing.T) {
	now := time.Now().UTC()
	roomDir, _ := reportFixture(t, now)
	writeSpec(t, roomDir, [][2]string{{"lead", "lead"}, {"go-dev", "developer"}, {"fe-dev", "developer"}, {"reviewer", "reviewer"}})
	// go-dev keeps the fixture's turns (60% code-edit); fe-dev is all measuring, so 0%.
	writeSeatTurns(t, roomDir, "fe-dev", []float64{2, 3})
	report, err := buildReport("room-a", roomDir, 24*time.Hour, now,
		func() ([]clientruntime.QueueRow, error) { return nil, nil },
		func() ([]staleTask, error) { return nil, nil }, noTaskStatus)
	if err != nil {
		t.Fatal(err)
	}
	bySeat := map[string]targetResult{}
	for _, target := range report.Targets {
		if target.Name == "code-edit cost %" {
			bySeat[target.Seat] = target
		}
	}
	if got := bySeat["go-dev"]; got.Result != "PASS" || got.Value != 60 {
		t.Errorf("go-dev line = %+v, want PASS at 60", got)
	}
	if got := bySeat["fe-dev"]; got.Result != "FAIL" || got.Value != 0 {
		t.Errorf("fe-dev line = %+v, want FAIL at 0", got)
	}
	if got := bySeat["room"]; got.Result != verdictDiag {
		t.Errorf("room line = %+v, want %q: the room share is printed, never gated", got, verdictDiag)
	}
	if _, gated := bySeat["lead"]; gated {
		t.Error("a non-editing seat holds a code-edit target")
	}

	var out strings.Builder
	report.renderText(&out)
	text := out.String()
	for _, want := range []string{"PASS code-edit cost % [go-dev]", "FAIL code-edit cost % [fe-dev]", "diag code-edit cost % [room]", "(want diagnostic, not a target)", "code-edit by FIRST tool call"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "code-edit cost % [room]") && (strings.Contains(line, "PASS") || strings.Contains(line, "FAIL")) {
			t.Errorf("the room code-edit line carries a verdict:\n%s", line)
		}
	}
}

// ONE SEAT'S TARGET DOES NOT MOVE WITH THE OTHER'S NUMBER: each editing seat is gated on its own reading, so
// raising fe-dev to PASS must leave go-dev's line exactly as it was.
func TestOneSeatsCodeEditTargetDoesNotMoveWithTheOthers(t *testing.T) {
	seatWith := func(name string, editShare float64) *seatStats {
		s := newSeatStats(name)
		s.Turns = 2
		s.Cost = 100
		s.ActivityCost[actCodeEdit] = editShare
		s.ActivityCost[actMeasuring] = 100 - editShare
		s.finish()
		return s
	}
	room := newSeatStats("ROOM")
	room.Cost = 200
	room.ActivityCost[actCodeEdit] = 60
	room.ActivityCost[actMeasuring] = 140
	room.finish()
	goDev := seatWith("go-dev", 50)
	feDev := seatWith("fe-dev", 10)

	line := func(targets []targetResult, seat string) targetResult {
		for _, target := range targets {
			if target.Name == "code-edit cost %" && target.Seat == seat {
				return target
			}
		}
		t.Fatalf("no code-edit line for %q in %+v", seat, targets)
		return targetResult{}
	}

	first := evaluateTargets(room, 1, true, true, []*seatStats{goDev, feDev})
	if got := line(first, "go-dev"); got.Result != "PASS" || got.Value != 50 {
		t.Fatalf("go-dev line = %+v, want PASS at 50", got)
	}
	if got := line(first, "fe-dev"); got.Result != "FAIL" || got.Value != 10 {
		t.Fatalf("fe-dev line = %+v, want FAIL at 10", got)
	}

	raised := seatWith("fe-dev", 90)
	second := evaluateTargets(room, 1, true, true, []*seatStats{goDev, raised})
	if got := line(second, "fe-dev"); got.Result != "PASS" || got.Value != 90 {
		t.Fatalf("fe-dev line = %+v, want PASS at 90", got)
	}
	if firstGo, secondGo := line(first, "go-dev"), line(second, "go-dev"); firstGo != secondGo {
		t.Errorf("go-dev moved with fe-dev: %+v -> %+v", firstGo, secondGo)
	}
	for _, targets := range [][]targetResult{first, second} {
		if got := line(targets, "room"); got.Result != verdictDiag {
			t.Errorf("room line = %+v, want %q", got, verdictDiag)
		}
	}
}
