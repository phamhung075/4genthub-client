package clientfacts

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
	"slices"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
)

// The fixture repo is the whole point of these tests: git and the pin compare run FOR REAL against it, and
// every key and value the block prints is pinned here, so a field cannot be renamed or a count silently
// reversed without a red test.

func isolateGit(t *testing.T) {
	t.Helper()
	// The machine's global config must not decide a fixture's commit signing, hooks or identity.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "facts@example.test")
	runGit(t, dir, "config", "user.name", "facts test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, message string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// clientFixture is the worktree the block describes: branch `work` one commit ahead of and one behind main,
// two dirty paths. Its HEAD is returned, and the first commit is the pin an "ahead" case compares against.
func clientFixture(t *testing.T) (dir, head, first string) {
	t.Helper()
	isolateGit(t)
	dir = newRepo(t, filepath.Join(t.TempDir(), "client"))
	writeFile(t, dir, "a.txt", "one\n")
	first = commit(t, dir, "one")
	runGit(t, dir, "checkout", "-q", "-b", "work")
	writeFile(t, dir, "b.txt", "two\n")
	head = commit(t, dir, "two")
	runGit(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "c.txt", "three\n")
	commit(t, dir, "three")
	runGit(t, dir, "checkout", "-q", "work")
	writeFile(t, dir, "a.txt", "one changed\n")
	writeFile(t, dir, "scratch.txt", "untracked\n")
	return dir, head, first
}

// hubFixture is the repo that PINS the client: its main branch holds agenthub_client as a gitlink at pin.
func hubFixture(t *testing.T, pin string) string {
	t.Helper()
	isolateGit(t)
	dir := newRepo(t, filepath.Join(t.TempDir(), "hub"))
	writeFile(t, dir, "README.md", "hub\n")
	commit(t, dir, "hub")
	runGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+pin+",agenthub_client")
	runGit(t, dir, "commit", "-qm", "pin")
	return dir
}

// stubSources replaces the four sources that would need a daemon, a clock, gh or a hub. The block test then
// pins THEIR fields and values too, while git and the pin compare stay real.
func stubSources(t *testing.T) {
	t.Helper()
	queue, offpeak, ci, task := queueSource, offpeakSource, ciSource, taskSource
	queueSource = func(room, _ string, _ string) Queue {
		return Queue{
			State: "ok", Room: room, Rows: 5, Pending: 2, InProgress: 1, Done: 1, HandedOff: 1,
			MinePending: 1, MineInProgress: 1, NewestMineID: "qitem-b", NewestMineSummary: "the newest row",
		}
	}
	offpeakSource = func() Offpeak {
		return Offpeak{
			State: "peak", Reason: "PEAK: 06:00-10:00 UTC (Mon-Fri)", Peak: true,
			Stamp: "2026-10-17T06:30Z", Next: "10:00 UTC (off-peak resumes)",
		}
	}
	ciSource = func(_ string, _ string) CI {
		return CI{State: "ok", Head: "cafe", Status: "completed", Conclusion: "success",
			Workflow: "ci", RunID: 42, URL: "https://example.test/run/42"}
	}
	taskSource = func(_ context.Context, id, _ string, _ string) Task {
		return Task{State: "ok", ID: id, Status: "in_progress", Priority: "high",
			Criteria: []string{"criterion one", "criterion two"}, CriteriaCount: 2}
	}
	t.Cleanup(func() { queueSource, offpeakSource, ciSource, taskSource = queue, offpeak, ci, task })
}

func jsonKeys(t *testing.T, value map[string]any) []string {
	t.Helper()
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func object(t *testing.T, parent map[string]any, name string) map[string]any {
	t.Helper()
	raw, ok := parent[name].(map[string]any)
	if !ok {
		t.Fatalf("%s is not a JSON object: %#v", name, parent[name])
	}
	return raw
}

func TestFactsBlockIsOneStableJSONObject(t *testing.T) {
	client, head, _ := clientFixture(t)
	hub := hubFixture(t, head)
	stubSources(t)
	t.Setenv("AGENTHUB_ROOM", "room-from-env")
	t.Setenv("AGENTHUB_SEAT", "seat-from-env")
	t.Setenv("AGENTHUB_SESSION_NAME", "")
	t.Setenv("AGENTHUB_URL", "https://hub.example.test")
	t.Setenv("AGENTHUB_TOKEN", "env-token")
	var seenURL, seenToken string
	taskSource = func(_ context.Context, id, baseURL, token string) Task {
		seenURL, seenToken = baseURL, token
		return Task{State: "ok", ID: id, Status: "in_progress", Priority: "high",
			Criteria: []string{"criterion one", "criterion two"}, CriteriaCount: 2}
	}

	var stdout, stderr bytes.Buffer
	code := command{}.Run(context.Background(), []string{
		"--repo", client, "--pins-repo", hub, "--task", "task-1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("facts exited %d: %s", code, stderr.String())
	}

	var block map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &block); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout.String())
	}
	wantTop := []string{"ci", "offpeak", "pins", "queue", "repo", "room", "schema", "seat", "session", "task"}
	if got := jsonKeys(t, block); !slices.Equal(got, wantTop) {
		t.Fatalf("top-level keys = %v, want %v", got, wantTop)
	}
	if block["schema"] != Schema {
		t.Fatalf("schema = %v, want %s", block["schema"], Schema)
	}
	if block["room"] != "room-from-env" || block["seat"] != "seat-from-env" {
		t.Fatalf("room/seat = %v/%v, want the environment's", block["room"], block["seat"])
	}
	// The session is DERIVED from room+seat when the environment names no session.
	if block["session"] != "agenthub--room-from-env--seat-from-env" {
		t.Fatalf("session = %v, want the derived seat session", block["session"])
	}

	repo := object(t, block, "repo")
	wantRepo := []string{"ahead_of_base", "base", "base_sha", "base_state", "behind_base", "branch",
		"detached", "dirty", "dirty_paths", "head", "head_short", "reason", "root", "state"}
	if got := jsonKeys(t, repo); !slices.Equal(got, wantRepo) {
		t.Fatalf("repo keys = %v, want %v", got, wantRepo)
	}
	assertString(t, repo, "state", "ok")
	assertString(t, repo, "branch", "work")
	assertString(t, repo, "head", head)
	assertString(t, repo, "base", "main")
	assertString(t, repo, "base_state", "ok")
	assertNumber(t, repo, "ahead_of_base", 1)
	assertNumber(t, repo, "behind_base", 1)
	assertNumber(t, repo, "dirty", 2)
	paths, ok := repo["dirty_paths"].([]any)
	if !ok || len(paths) != 2 || paths[0] != "a.txt" || paths[1] != "scratch.txt" {
		t.Fatalf("dirty_paths = %#v, want [a.txt scratch.txt]", repo["dirty_paths"])
	}
	if repo["base_sha"] == "" {
		t.Fatalf("base_sha is empty on a fixture whose main exists")
	}

	pins := object(t, block, "pins")
	wantPins := []string{"ahead", "behind", "head", "pin", "reason", "ref", "repo", "state"}
	if got := jsonKeys(t, pins); !slices.Equal(got, wantPins) {
		t.Fatalf("pins keys = %v, want %v", got, wantPins)
	}
	assertString(t, pins, "state", "same")
	assertString(t, pins, "pin", head)
	assertString(t, pins, "head", head)
	assertString(t, pins, "ref", "main")
	assertString(t, pins, "repo", hub)
	assertNumber(t, pins, "ahead", 0)
	assertNumber(t, pins, "behind", 0)

	queue := object(t, block, "queue")
	wantQueue := []string{"blocked", "done", "handed_off", "in_progress", "mine_blocked",
		"mine_in_progress", "mine_pending", "newest_mine_id", "newest_mine_summary", "other", "pending",
		"reason", "room", "rows", "state"}
	if got := jsonKeys(t, queue); !slices.Equal(got, wantQueue) {
		t.Fatalf("queue keys = %v, want %v", got, wantQueue)
	}
	assertString(t, queue, "state", "ok")
	assertString(t, queue, "room", "room-from-env")
	assertNumber(t, queue, "rows", 5)
	assertNumber(t, queue, "pending", 2)
	assertNumber(t, queue, "in_progress", 1)
	assertNumber(t, queue, "done", 1)
	assertNumber(t, queue, "handed_off", 1)
	assertNumber(t, queue, "mine_pending", 1)
	assertString(t, queue, "newest_mine_id", "qitem-b")

	offpeak := object(t, block, "offpeak")
	wantOffpeak := []string{"holiday", "next", "peak", "reason", "stamp", "state"}
	if got := jsonKeys(t, offpeak); !slices.Equal(got, wantOffpeak) {
		t.Fatalf("offpeak keys = %v, want %v", got, wantOffpeak)
	}
	assertString(t, offpeak, "state", "peak")
	if offpeak["peak"] != true {
		t.Fatalf("offpeak.peak = %#v, want true", offpeak["peak"])
	}
	assertString(t, offpeak, "stamp", "2026-10-17T06:30Z")

	ci := object(t, block, "ci")
	wantCI := []string{"conclusion", "head", "reason", "run_id", "state", "status", "url", "workflow"}
	if got := jsonKeys(t, ci); !slices.Equal(got, wantCI) {
		t.Fatalf("ci keys = %v, want %v", got, wantCI)
	}
	assertString(t, ci, "state", "ok")
	assertString(t, ci, "head", "cafe")
	assertString(t, ci, "conclusion", "success")
	assertNumber(t, ci, "run_id", 42)

	task := object(t, block, "task")
	wantTask := []string{"criteria", "criteria_count", "id", "priority", "reason", "state", "status"}
	if got := jsonKeys(t, task); !slices.Equal(got, wantTask) {
		t.Fatalf("task keys = %v, want %v", got, wantTask)
	}
	assertString(t, task, "state", "ok")
	assertString(t, task, "id", "task-1")
	assertNumber(t, task, "criteria_count", 2)
	// The task source is reached with the environment's URL and token when no flag overrides them.
	if seenURL != "https://hub.example.test" || seenToken != "env-token" {
		t.Fatalf("task source got url %q token %q, want the environment's", seenURL, seenToken)
	}
	criteria, ok := task["criteria"].([]any)
	if !ok || len(criteria) != 2 || criteria[0] != "criterion one" {
		t.Fatalf("criteria = %#v", task["criteria"])
	}
}

func TestPinsCompareStates(t *testing.T) {
	client, head, first := clientFixture(t)
	// A side branch diverges from the work branch: each holds a commit the other lacks.
	runGit(t, client, "checkout", "-q", "-b", "side", "main")
	writeFile(t, client, "d.txt", "side\n")
	side := commit(t, client, "side")
	runGit(t, client, "checkout", "-q", "work")

	cases := []struct {
		name          string
		pin, head     string
		state         string
		ahead, behind int
	}{
		{"same", head, head, "same", 0, 0},
		{"ahead", first, head, "ahead", 1, 0},
		{"behind", head, first, "behind", 0, 1},
		{"diverged", head, side, "diverged", 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hub := hubFixture(t, c.pin)
			got := pinsFacts(client, c.head, hub, "main")
			if got.State != c.state || got.Ahead != c.ahead || got.Behind != c.behind {
				t.Fatalf("pins = %+v, want state %s ahead %d behind %d", got, c.state, c.ahead, c.behind)
			}
		})
	}

	t.Run("no gitlink", func(t *testing.T) {
		hub := hubFixture(t, head)
		// Replace the gitlink with a plain directory record: the name exists, the pin does not.
		runGit(t, hub, "update-index", "--force-remove", "agenthub_client")
		writeFile(t, hub, "agenthub_client", "not a gitlink\n")
		runGit(t, hub, "add", "agenthub_client")
		runGit(t, hub, "commit", "-qm", "a file, not a pin")
		got := pinsFacts(client, head, hub, "main")
		if got.State != "unavailable" || !strings.Contains(got.Reason, "gitlink") {
			t.Fatalf("pins = %+v, want unavailable naming the missing gitlink", got)
		}
	})
}

// submoduleFixture is the shape this room has: the hub RECORDS the client sha as a gitlink, and the client
// CHECKOUT - a separate repo inside it - is the only place that sha is an object. hubHead is the hub's own
// HEAD, which is what a seat working in a hub worktree passes as its head.
func submoduleFixture(t *testing.T) (hub, client, pin, hubHead string) {
	t.Helper()
	isolateGit(t)
	hub = newRepo(t, filepath.Join(t.TempDir(), "hub"))
	writeFile(t, hub, "README.md", "hub\n")
	commit(t, hub, "hub")
	client = newRepo(t, filepath.Join(hub, gitlinkName))
	writeFile(t, client, "a.txt", "one\n")
	pin = commit(t, client, "one")
	runGit(t, hub, "update-index", "--add", "--cacheinfo", "160000,"+pin+","+gitlinkName)
	runGit(t, hub, "commit", "-qm", "pin")
	return hub, client, pin, runGit(t, hub, "rev-parse", "HEAD")
}

// TestPinsResolvesTheGitlinkInTheSubmoduleCheckout: the pin's commit lives in the submodule checkout, not in
// the repo that records it, so the comparison and the reported head are that checkout's. A seat in a hub
// worktree, answering pins.state=unavailable "Invalid symmetric difference" every session, is this case.
func TestPinsResolvesTheGitlinkInTheSubmoduleCheckout(t *testing.T) {
	hub, client, pin, hubHead := submoduleFixture(t)

	got := pinsFacts(hub, hubHead, hub, "main")
	if got.State != "same" || got.Pin != pin || got.Head != pin || got.Ahead != 0 || got.Behind != 0 {
		t.Fatalf("pins = %+v, want state same, pin and head %s, ahead 0 behind 0", got, pin)
	}

	// Discovery without --pins-repo: the caller's own tree carries the gitlink, so it IS the pinning repo.
	got = pinsFacts(hub, hubHead, "", "main")
	if got.State != "same" || got.Repo != hub {
		t.Fatalf("pins = %+v, want state same against the caller's own repo %s", got, hub)
	}

	// One client commit after the pin: the submodule's head is one ahead, and the hub's head is not consulted.
	writeFile(t, client, "b.txt", "two\n")
	commit(t, client, "two")
	got = pinsFacts(hub, hubHead, hub, "main")
	if got.State != "ahead" || got.Ahead != 1 || got.Behind != 0 {
		t.Fatalf("pins = %+v, want state ahead 1 behind 0", got)
	}
}

// TestPinsUnavailableWhenNeitherRepoHoldsThePin: a gitlink naming a commit neither repo has is a fetch away,
// and the reason says which submodule checkout was read instead of reporting the hub's own git error.
func TestPinsUnavailableWhenNeitherRepoHoldsThePin(t *testing.T) {
	hub, _, _, hubHead := submoduleFixture(t)
	absent := strings.Repeat("a", 40)
	runGit(t, hub, "update-index", "--add", "--cacheinfo", "160000,"+absent+","+gitlinkName)
	runGit(t, hub, "commit", "-qm", "a pin the checkout does not hold")

	got := pinsFacts(hub, hubHead, hub, "main")
	if got.State != "unavailable" || !strings.Contains(got.Reason, gitlinkName) || !strings.Contains(got.Reason, "submodule") {
		t.Fatalf("pins = %+v, want unavailable with a reason naming the %s submodule", got, gitlinkName)
	}
	if got.Pin != absent {
		t.Fatalf("pins.Pin = %q, want the sha the hub records %q", got.Pin, absent)
	}
}

func TestPinsDiscoveryFindsTheParentRepo(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	hub := newRepo(t, root)
	client := newRepo(t, filepath.Join(root, "agenthub_client"))
	writeFile(t, client, "a.txt", "one\n")
	head := commit(t, client, "one")
	runGit(t, hub, "update-index", "--add", "--cacheinfo", "160000,"+head+",agenthub_client")
	runGit(t, hub, "commit", "-qm", "pin")

	got := pinsFacts(client, head, "", "main")
	if got.State != "same" || got.Repo != root || got.Pin != head {
		t.Fatalf("pins = %+v, want state same against the parent repo %s", got, root)
	}
}

func TestRepoFactsUnavailableAndBaseMissing(t *testing.T) {
	notARepo := t.TempDir()
	got := repoFacts(notARepo, "main")
	if got.State != "unavailable" || got.Reason == "" {
		t.Fatalf("repo = %+v, want unavailable with a reason", got)
	}

	client, _, _ := clientFixture(t)
	got = repoFacts(client, "no-such-base")
	if got.State != "ok" || got.BaseState != "missing" || got.AheadOfBase != 0 || got.BehindBase != 0 {
		t.Fatalf("repo = %+v, want ok with base_state missing", got)
	}
}

func TestQueueCountsRowsAndNamesNewestMine(t *testing.T) {
	session := "agenthub--r--s"
	rows := []clientruntime.QueueRow{
		{QitemID: "q-done", State: "done", DestinationSession: session, TsCreated: "2026-10-17T01:00:00Z"},
		{QitemID: "q-old", State: "pending", DestinationSession: session, TsCreated: "2026-10-17T02:00:00Z", Summary: "old"},
		{QitemID: "q-new", State: "pending", DestinationSession: session, TsCreated: "2026-10-17T03:00:00Z", Summary: "new"},
		{QitemID: "q-run", State: "in-progress", DestinationSession: session, TsCreated: "2026-10-17T04:00:00Z"},
		{QitemID: "q-block", State: "blocked", DestinationSession: session, TsCreated: "2026-10-17T05:00:00Z"},
		{QitemID: "q-other", State: "pending", DestinationSession: "someone-else", TsCreated: "2026-10-17T06:00:00Z"},
		{QitemID: "q-handoff", State: "handed-off", DestinationSession: "someone-else", TsCreated: "2026-10-17T07:00:00Z"},
	}
	got := countQueue("r", session, rows)
	if got.Rows != 7 || got.Pending != 3 || got.InProgress != 1 || got.Blocked != 1 || got.Done != 1 || got.HandedOff != 1 {
		t.Fatalf("queue counts = %+v", got)
	}
	if got.MinePending != 2 || got.MineInProgress != 1 || got.MineBlocked != 1 {
		t.Fatalf("mine counts = %+v", got)
	}
	if got.NewestMineID != "q-block" || got.NewestMineSummary != "" {
		t.Fatalf("newest mine = %s/%q, want q-block (newest active row to this session)", got.NewestMineID, got.NewestMineSummary)
	}

	withoutSeat := countQueue("r", "", rows)
	if withoutSeat.MinePending != 0 || withoutSeat.NewestMineID != "" {
		t.Fatalf("a sessionless read claimed someone's rows: %+v", withoutSeat)
	}
}

func TestTaskFactsReadsTheAcceptanceRecord(t *testing.T) {
	var seenPath, seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"success":true,"task":{"status":"in_progress","priority":"high",
			"acceptance_criteria":["criterion one","criterion two"]}}`)
	}))
	defer server.Close()
	restore := factsClient
	factsClient = server.Client()
	t.Cleanup(func() { factsClient = restore })

	got := taskFacts(context.Background(), "abc123", server.URL, "secret-token")
	if got.State != "ok" || got.Status != "in_progress" || got.Priority != "high" || got.CriteriaCount != 2 {
		t.Fatalf("task = %+v", got)
	}
	if got.Criteria[0] != "criterion one" {
		t.Fatalf("criteria = %#v", got.Criteria)
	}
	if seenPath != "/api/v2/tasks/abc123" || seenAuth != "Bearer secret-token" {
		t.Fatalf("request = %s auth %q", seenPath, seenAuth)
	}

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer refused.Close()
	factsClient = refused.Client()
	got = taskFacts(context.Background(), "abc123", refused.URL, "secret-token")
	if got.State != "unavailable" || !strings.Contains(got.Reason, "HTTP 403") {
		t.Fatalf("task = %+v, want unavailable naming HTTP 403", got)
	}

	noTask := taskFacts(context.Background(), "", server.URL, "secret-token")
	if noTask.State != "unavailable" || !strings.Contains(noTask.Reason, "--task") {
		t.Fatalf("task = %+v, want the missing --task named", noTask)
	}
}

func TestCIFactsParsesTheNewestRun(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `[{"status":"completed","conclusion":"success","headSha":"cafe","workflowName":"ci","databaseId":42,"url":"https://example.test/run/42"}]`
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '"+body+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := ciFacts(t.TempDir(), "work")
	if got.State != "ok" || got.Head != "cafe" || got.Status != "completed" ||
		got.Conclusion != "success" || got.Workflow != "ci" || got.RunID != 42 || got.URL != "https://example.test/run/42" {
		t.Fatalf("ci = %+v", got)
	}

	failing := filepath.Join(bin, "gh-fail")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho 'no workflows found' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(failing, script); err != nil {
		t.Fatal(err)
	}
	got = ciFacts(t.TempDir(), "work")
	if got.State != "unavailable" || !strings.Contains(got.Reason, "no workflows found") {
		t.Fatalf("ci = %+v, want unavailable naming gh's reason", got)
	}
}

func TestOffpeakFactsFollowsThePinnedClock(t *testing.T) {
	t.Setenv("OFFPEAK_UTC", "2026-10-16 06:30")
	peak := offpeakFacts()
	if peak.State != "peak" || !peak.Peak || peak.Stamp != "2026-10-16T06:30Z" {
		t.Fatalf("offpeak at the pinned peak = %+v", peak)
	}
	t.Setenv("OFFPEAK_UTC", "2026-10-16 11:00")
	off := offpeakFacts()
	if off.State != "off-peak" || off.Peak || off.Stamp != "2026-10-16T11:00Z" {
		t.Fatalf("offpeak at the pinned off-peak = %+v", off)
	}
}

func TestFactsUsageRefusals(t *testing.T) {
	for _, args := range [][]string{
		{"--nope"},
		{"--room"},
		{"positional"},
		{"--repo", ""},
	} {
		var stdout, stderr bytes.Buffer
		if code := (command{}).Run(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Fatalf("facts %v exited %d, want 2 (stderr %q)", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "usage: 4genteam facts") {
			t.Fatalf("facts %v printed no usage: %q", args, stderr.String())
		}
	}
}

func assertString(t *testing.T, object map[string]any, key, want string) {
	t.Helper()
	got, ok := object[key].(string)
	if !ok || got != want {
		t.Fatalf("%s = %#v, want %q", key, object[key], want)
	}
}

func assertNumber(t *testing.T, object map[string]any, key string, want float64) {
	t.Helper()
	got, ok := object[key].(float64)
	if !ok || got != want {
		t.Fatalf("%s = %#v, want %v", key, object[key], want)
	}
}
