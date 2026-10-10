package clientsync

// The Go counterparts of tests/test_seat_sync.py's test_rig_* cases (and the one launch_state_root
// helper test and the rig half of test_pull_and_rig_fail_loudly_without_the_link). Each assertions
// the same OBSERVABLE thing the Python test does: an exit code, a file written, a symlink target, a
// stdout/stderr line, or a refusal message. The Python test that only pins argparse flags
// (test_rig_requests_plain_path_and_has_no_permission_policy_flag) is class (ii) plumbing and is not
// ported - it is noted in the report.
//
// Everything is driven through a fake HTTP cloud and the package's own seams, so no test needs a real
// 4genthub server, a real `rig`, a tmux server or a live state root.

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
	"runtime"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientpolicy"
)

var (
	rigHashA = strings.Repeat("a1", 32)
	rigHashB = strings.Repeat("b2", 32)
)

const rigDefaultYAML = "name: room1\npods:\n  - id: main\n    members:\n      - id: seat1\n        agent_ref: local:agents/seat1\n"

const (
	rigOMPMCPDocumentJSON = `{"mcpServers":{"agenthub_http":{"type":"http","url":"https://api.example.test/mcp","headers":{"Authorization":"Bearer ${AGENTHUB_TOKEN}"}}}}`
	rigOMPConfigFragment  = "mcp:\n  startupTimeoutMs: 0\n"
	rigAgentsMDDocument   = "<!-- seat-hash: a1a1 -->\n<!-- seat-type-version: 1.2.0 -->\n\n## Guide: every seat\n\nshared words\n\n## Guide: seat1\n\nits own words\n"
)

func rigDefaultFiles() []map[string]any {
	return []map[string]any{
		{"path": "docs/readme.md", "content": "hello"},
		{"path": "nested/deeper/notes.txt", "content": "notes"},
	}
}

func rigDefaultPolicy() map[string]any {
	return map[string]any{"max_turns": 5, "runtime": "claude"}
}

// --- the fake cloud ---------------------------------------------------------------------------

type rigSeatResponse struct {
	status int
	hash   string
	files  []map[string]any
	policy map[string]any
}

type rigVerbCloud struct {
	mu           sync.Mutex
	rigspecCode  int
	rigspecError string
	rigspecName  string
	rigspecYAML  string
	rigspecSeats []map[string]any
	seats        map[string]*rigSeatResponse
	gets         []string
}

func newRigVerbCloud() *rigVerbCloud {
	return &rigVerbCloud{
		rigspecName:  "room1",
		rigspecYAML:  rigDefaultYAML,
		rigspecSeats: []map[string]any{{"seat": "seat1", "hash": rigHashA}},
		seats:        map[string]*rigSeatResponse{},
	}
}

func (c *rigVerbCloud) setSeat(room, seat, hash string, files []map[string]any, policy map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if files == nil {
		files = rigDefaultFiles()
	}
	if policy == nil {
		policy = rigDefaultPolicy()
	}
	c.seats[room+"/"+seat] = &rigSeatResponse{status: 200, hash: hash, files: files, policy: policy}
}

func (c *rigVerbCloud) setSeatStatus(room, seat string, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seats[room+"/"+seat] = &rigSeatResponse{status: status}
}

func (c *rigVerbCloud) setRigspec(name, yamlText string, seats []map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rigspecName, c.rigspecYAML, c.rigspecSeats = name, yamlText, seats
}

func (c *rigVerbCloud) setRigspecStatus(status int, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rigspecCode, c.rigspecError = status, message
}

func (c *rigVerbCloud) handle(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.gets = append(c.gets, r.URL.Path)
	c.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/rigspec"):
		c.serveRigspec(w)
	case strings.HasPrefix(r.URL.Path, PathsPath+"/"):
		c.serveSeat(w, strings.TrimPrefix(r.URL.Path, PathsPath+"/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (c *rigVerbCloud) serveRigspec(w http.ResponseWriter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rigspecCode != 0 && c.rigspecCode != 200 {
		rigWriteJSON(w, c.rigspecCode, map[string]any{"success": false, "error": c.rigspecError})
		return
	}
	rigWriteJSON(w, 200, map[string]any{"success": true, "rigspec": map[string]any{
		"name": c.rigspecName, "yaml": c.rigspecYAML, "seats": rigAnySlice(c.rigspecSeats),
	}})
}

func (c *rigVerbCloud) serveSeat(w http.ResponseWriter, rest string) {
	parts := strings.SplitN(rest, "/", 2)
	c.mu.Lock()
	seat, ok := c.seats[rest]
	c.mu.Unlock()
	if !ok {
		rigWriteJSON(w, 404, map[string]any{"success": false, "error": "not found"})
		return
	}
	if seat.status != 200 {
		rigWriteJSON(w, seat.status, map[string]any{"success": false, "error": "boom"})
		return
	}
	room, seatName := "room1", rest
	if len(parts) == 2 {
		room, seatName = parts[0], parts[1]
	}
	rigWriteJSON(w, 200, map[string]any{"success": true, "resolved_seat": map[string]any{
		"room": room, "seat": seatName, "hash": seat.hash, "runtime": "claude",
		"files": rigAnySlice(seat.files), "policy": seat.policy,
	}})
}

func rigWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// rigAnySlice widens a typed slice to the []any a JSON body's file/seat lists decode into.
func rigAnySlice[T any](items []T) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

// --- the fixture --------------------------------------------------------------------------------

type rigVerbFixture struct {
	t       *testing.T
	cloud   *rigVerbCloud
	out     string
	ompRoot string
	keyFile string
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
}

func rigVerbTest(t *testing.T) *rigVerbFixture {
	t.Helper()
	f := &rigVerbFixture{t: t, cloud: newRigVerbCloud()}
	server := httptest.NewServer(http.HandlerFunc(f.cloud.handle))
	t.Cleanup(server.Close)
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "test-token")
	f.stdout, f.stderr = &bytes.Buffer{}, &bytes.Buffer{}
	f.out = t.TempDir()
	f.ompRoot = filepath.Join(t.TempDir(), "ompstate")
	f.keyFile = filepath.Join(t.TempDir(), "provider-env")

	origRequire, origLaunch := rigRequireChecker, rigLaunchStateRoot
	origPolicy, origRoot, origEnv := rigPolicyTable, rigDefaultStateRoot, rigEnvSource
	t.Cleanup(func() {
		rigRequireChecker, rigLaunchStateRoot = origRequire, origLaunch
		rigPolicyTable, rigDefaultStateRoot, rigEnvSource = origPolicy, origRoot, origEnv
	})
	rigRequireChecker = func(string, io.Writer) error { return nil }
	rigLaunchStateRoot = func(string) string { return "" }
	rigPolicyTable = func(string) (map[string]string, bool) { return nil, false }
	rigDefaultStateRoot = func() string { return f.ompRoot }
	rigEnvSource = func() (string, error) { return f.keyFile, nil }
	return f
}

func (f *rigVerbFixture) run(args ...string) (int, string, string) {
	f.t.Helper()
	f.stdout.Reset()
	f.stderr.Reset()
	code := RunRigVerb(context.Background(), args, f.stdout, f.stderr)
	return code, f.stdout.String(), f.stderr.String()
}

func (f *rigVerbFixture) roomDir(room string) string { return filepath.Join(f.out, room) }
func (f *rigVerbFixture) rigDir(room string) string  { return filepath.Join(f.out, room, "rig") }

func (f *rigVerbFixture) ompAgentDir(pod, seat, room string) string {
	return filepath.Join(f.ompRoot, rigSessionName(pod, seat, room), "agent")
}

// ompRig is the Python _omp_rig helper: a rig whose seat render may carry any of the omp files, plus a
// state root for the seat.
func (f *rigVerbFixture) ompRig(withRender, withConfig, withAgents, makeAgentDir bool) {
	f.t.Helper()
	files := rigDefaultFiles()
	if withRender {
		files = append(files, map[string]any{"path": rigOMPMCPRenderPath, "content": rigOMPMCPDocumentJSON})
	}
	if withConfig {
		files = append(files, map[string]any{"path": rigOMPConfigRenderPath, "content": rigOMPConfigFragment})
	}
	if withAgents {
		files = append(files, map[string]any{"path": rigAgentsMDRenderPath, "content": rigAgentsMDDocument})
	}
	f.cloud.setSeat("room1", "seat1", rigHashA, files, nil)
	if makeAgentDir {
		if err := os.MkdirAll(f.ompAgentDir("main", "seat1", "room1"), 0o755); err != nil {
			f.t.Fatalf("MkdirAll: %v", err)
		}
	}
}

// --- helpers ------------------------------------------------------------------------------------

func rigParseYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal([]byte(readFile(t, path)), &document); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return document
}

func rigMembersOf(t *testing.T, document map[string]any) []map[string]any {
	t.Helper()
	pods, _ := document["pods"].([]any)
	if len(pods) == 0 {
		t.Fatalf("spec has no pods: %v", document)
	}
	var members []map[string]any
	for _, podAny := range pods {
		pod, _ := podAny.(map[string]any)
		for _, memberAny := range rigAnyList(pod["members"]) {
			member, _ := memberAny.(map[string]any)
			members = append(members, member)
		}
	}
	return members
}

func rigMtime(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return info.ModTime().UnixNano()
}

func rigMustResolve(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return resolved
}

func rigIsSymlink(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

func rigNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists (err=%v), want absent", path, err)
	}
}

// policyDocumentFor renders a per-seat policy document through the SAME helper the policy verb uses,
// which is what makes the single-source assertion meaningful rather than circular.
func rigPolicyDocumentFor(seat, role string) string {
	return clientpolicy.RenderConfig(clientpolicy.Seat{Key: seat, Type: role})
}

// --- the tests ----------------------------------------------------------------------------------

// test_rig_writes_yaml_verbatim_and_materializes_each_seat_with_its_policy.
func TestRigWritesYAMLAndMaterializesEachSeatWithItsPolicy(t *testing.T) {
	f := rigVerbTest(t)
	yamlText := "name: room1\npods:\n  - id: main\n    members:\n" +
		"      - id: coder\n        agent_ref: local:agents/coder\n" +
		"      - id: checker\n        agent_ref: local:agents/checker\n"
	f.cloud.setRigspec("room1", yamlText, []map[string]any{
		{"seat": "coder", "hash": rigHashA}, {"seat": "checker", "hash": rigHashB},
	})
	f.cloud.setSeat("room1", "coder", rigHashA, nil, nil)
	f.cloud.setSeat("room1", "checker", rigHashB, nil, nil)

	code, stdout, stderr := f.run("room1", "--out", f.out)

	rigDir := f.rigDir("room1")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if want := "rig:" + filepath.Join(rigDir, "rig.yaml") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	document := rigParseYAML(t, filepath.Join(rigDir, "rig.yaml"))
	members := rigMembersOf(t, document)
	if len(members) != 2 || members[0]["id"] != "coder" || members[1]["agent_ref"] != "local:agents/checker" {
		t.Errorf("members = %v, want the server's ids and refs", members)
	}
	for _, member := range members {
		if member["cwd"] != f.roomDir("room1") {
			t.Errorf("cwd = %v, want the room directory", member["cwd"])
		}
		if member["permission_policy"] != "builtin:yolo" {
			t.Errorf("permission_policy = %v", member["permission_policy"])
		}
	}
	for seat, hash := range map[string]string{"coder": rigHashA, "checker": rigHashB} {
		agent := filepath.Join(rigDir, "agents", seat)
		info, err := os.Stat(agent)
		if err != nil || !info.IsDir() || rigIsSymlink(t, agent) {
			t.Fatalf("%s is not a materialized directory: %v", agent, err)
		}
		if got := readFile(t, filepath.Join(agent, "docs", "readme.md")); got != "hello" {
			t.Errorf("%s docs/readme.md = %q", seat, got)
		}
		if got := readFile(t, filepath.Join(agent, "nested", "deeper", "notes.txt")); got != "notes" {
			t.Errorf("%s notes.txt = %q", seat, got)
		}
		if lock := readJSON(t, filepath.Join(agent, "pinned.json")); lock["hash"] != hash {
			t.Errorf("%s pinned.json = %v, want %s", seat, lock, hash)
		}
		if policy := readJSON(t, filepath.Join(agent, "policy.json")); policy["runtime"] != "claude" {
			t.Errorf("%s policy.json = %v, want the resolved policy", seat, policy)
		}
	}
}

// test_rig_accepts_uppercase_names.
func TestRigAcceptsUppercaseNames(t *testing.T) {
	f := rigVerbTest(t)
	upper := strings.ReplaceAll(rigDefaultYAML, "id: seat1", "id: Coder")
	f.cloud.setRigspec("Room1", upper, []map[string]any{{"seat": "Coder", "hash": rigHashA}})
	f.cloud.setSeat("Room1", "Coder", rigHashA, nil, nil)

	code, stdout, stderr := f.run("Room1", "--out", f.out)

	rigDir := f.rigDir("Room1")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if want := "rig:" + filepath.Join(rigDir, "rig.yaml") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	agent := filepath.Join(rigDir, "agents", "Coder")
	if info, err := os.Stat(agent); err != nil || !info.IsDir() || rigIsSymlink(t, agent) {
		t.Fatalf("Coder agent is not a materialized directory: %v", err)
	}
	if got := readFile(t, filepath.Join(agent, "docs", "readme.md")); got != "hello" {
		t.Errorf("docs/readme.md = %q", got)
	}
	if lock := readJSON(t, filepath.Join(agent, "pinned.json")); lock["hash"] != rigHashA {
		t.Errorf("pinned.json = %v", lock)
	}
}

// test_rig_second_run_keeps_pin_and_prints_notice.
func TestRigSecondRunKeepsPinAndPrintsNotice(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setSeat("room1", "seat1", rigHashA, nil, nil)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}

	shifted := strings.Replace(rigDefaultYAML, "id: main", "id: shifted", 1)
	f.cloud.setRigspec("room1", shifted, []map[string]any{{"seat": "seat1", "hash": rigHashB}})
	f.cloud.setSeat("room1", "seat1", rigHashB, []map[string]any{{"path": "docs/readme.md", "content": "newer"}}, nil)
	code, stdout, stderr := f.run("room1", "--out", f.out)

	seatDir := filepath.Join(f.roomDir("room1"), "seat1")
	rigDir := f.rigDir("room1")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if want := "rig:" + filepath.Join(rigDir, "rig.yaml") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if want := "newer snapshot available: " + rigHashB + " (run with --update to adopt)\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if lock := readJSON(t, filepath.Join(seatDir, "pinned.json")); lock["hash"] != rigHashA {
		t.Errorf("pin moved: %v", lock)
	}
	agent := filepath.Join(rigDir, "agents", "seat1")
	if lock := readJSON(t, filepath.Join(agent, "pinned.json")); lock["hash"] != rigHashA {
		t.Errorf("agent pinned.json = %v, want the PINNED hash", lock)
	}
	if got := readFile(t, filepath.Join(agent, "docs", "readme.md")); got != "hello" {
		t.Errorf("agent came from the newer snapshot (%q)", got)
	}
	rigNotExist(t, filepath.Join(seatDir, rigHashB))
	document := rigParseYAML(t, filepath.Join(rigDir, "rig.yaml"))
	if pods, _ := document["pods"].([]any); len(pods) == 0 || pods[0].(map[string]any)["id"] != "shifted" {
		t.Errorf("rig.yaml did not come from the current server response: %v", document)
	}
}

// test_rig_build_keeps_operator_files_it_did_not_create.
func TestRigBuildKeepsOperatorFilesItDidNotCreate(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setSeat("room1", "seat1", rigHashA, nil, nil)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}

	rigDir := f.rigDir("room1")
	notes := filepath.Join(rigDir, "operator-notes.txt")
	if err := os.WriteFile(notes, []byte("placed by the operator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.out, "credential-target")
	if err := os.WriteFile(target, []byte("not a credential\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rigDir, "operator-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if want := "rig:" + filepath.Join(rigDir, "rig.yaml") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want only the machine-readable line", stdout)
	}
	if got := readFile(t, notes); got != "placed by the operator\n" {
		t.Errorf("operator notes = %q", got)
	}
	if dest, err := os.Readlink(link); err != nil || dest != target {
		t.Errorf("operator link -> %q, %v; want %q", dest, err, target)
	}
	for _, want := range []string{"kept 2 file(s) the build did not create", "operator-link", "operator-notes.txt"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}

// test_rig_build_keeps_a_file_placed_while_it_materializes: the file lands in the LIVE rig directory
// while the staging tree is being materialized, so only a swap-time derivation can keep it.
func TestRigBuildKeepsAFilePlacedWhileItMaterializes(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setSeat("room1", "seat1", rigHashA, nil, nil)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}
	rigDir := f.rigDir("room1")

	original := rigMaterializeAgent
	t.Cleanup(func() { rigMaterializeAgent = original })
	rigMaterializeAgent = func(source, seatDir, target string) error {
		if err := original(source, seatDir, target); err != nil {
			return err
		}
		if info, err := os.Stat(rigDir); err == nil && info.IsDir() {
			_ = os.WriteFile(filepath.Join(rigDir, "dropped-while-building.txt"), []byte("landed mid-build\n"), 0o644)
		}
		return nil
	}

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if got := readFile(t, filepath.Join(rigDir, "dropped-while-building.txt")); got != "landed mid-build\n" {
		t.Errorf("the mid-build file = %q, want it preserved", got)
	}
	if !strings.Contains(stderr, "dropped-while-building.txt") {
		t.Errorf("stderr = %q, want the kept file named", stderr)
	}
	if document := rigParseYAML(t, filepath.Join(rigDir, "rig.yaml")); document["name"] != "room1" {
		t.Errorf("rig.yaml = %v, want the build's own content", document)
	}
}

// test_rig_build_replaces_its_own_rendered_content: agents/ is the build's, so a seat the room no
// longer lists does not survive.
func TestRigBuildReplacesItsOwnRenderedContent(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setRigspec("room1", rigDefaultYAML, []map[string]any{
		{"seat": "seat1", "hash": rigHashA}, {"seat": "seat2", "hash": rigHashB},
	})
	f.cloud.setSeat("room1", "seat1", rigHashA, nil, nil)
	f.cloud.setSeat("room1", "seat2", rigHashB, nil, nil)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}
	rigDir := f.rigDir("room1")
	if info, err := os.Stat(filepath.Join(rigDir, "agents", "seat2")); err != nil || !info.IsDir() {
		t.Fatalf("seat2 agent missing after first run: %v", err)
	}

	f.cloud.setRigspec("room1", rigDefaultYAML, []map[string]any{{"seat": "seat1", "hash": rigHashA}})
	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if info, err := os.Stat(filepath.Join(rigDir, "agents", "seat1")); err != nil || !info.IsDir() {
		t.Errorf("seat1 agent missing: %v", err)
	}
	rigNotExist(t, filepath.Join(rigDir, "agents", "seat2"))
	if strings.Contains(stderr, "kept") {
		t.Errorf("stderr = %q, want no kept report", stderr)
	}
}

// test_rig_applies_the_per_seat_policy_from_the_single_source.
func TestRigAppliesThePerSeatPolicyFromTheSingleSource(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	document := rigPolicyDocumentFor("seat1", "dev")
	rigPolicyTable = func(room string) (map[string]string, bool) {
		if room != "room1" {
			return nil, false
		}
		return map[string]string{"seat1": document}, true
	}

	code, _, stderr := f.run("room1", "--out", f.out)

	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if got := readFile(t, config); got != document {
		t.Errorf("config.yml does not equal render_config's own document:\n got %q\nwant %q", got, document)
	}
	if !strings.Contains(stderr, "applied the per-seat policy") {
		t.Errorf("stderr = %q, want the application reported", stderr)
	}
	parsed := rigParseYAML(t, config)
	bash, _ := parsed["bash"].(map[string]any)
	patterns, _ := bash["patterns"].([]any)
	if len(patterns) == 0 {
		t.Fatalf("no bash patterns in %v", parsed)
	}
	first, _ := patterns[0].(map[string]any)
	if first["match"] != "rig whoami*" || first["approval"] != "allow" {
		t.Errorf("first pattern = %v, want the startup exemption", first)
	}
	for _, pattern := range patterns[1:] {
		rule, _ := pattern.(map[string]any)
		if rule["approval"] != "deny" {
			t.Errorf("rule %v is not a deny", rule)
		}
	}
	tools, _ := parsed["tools"].(map[string]any)
	if tools == nil || tools["approval"] == nil {
		t.Errorf("tools.approval missing: %v", parsed)
	}
	mcp, _ := parsed["mcp"].(map[string]any)
	if mcp["startupTimeoutMs"] != 0 {
		t.Errorf("mcp.startupTimeoutMs = %v, want 0", mcp["startupTimeoutMs"])
	}
}

// test_rig_policy_merge_keeps_what_the_runtime_file_already_carries.
func TestRigPolicyMergeKeepsWhatTheRuntimeFileAlreadyCarries(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	rigPolicyTable = func(string) (map[string]string, bool) {
		return map[string]string{"seat1": rigPolicyDocumentFor("seat1", "dev")}, true
	}
	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	if err := os.WriteFile(config, []byte("model: something-else\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	parsed := rigParseYAML(t, config)
	if parsed["model"] != "something-else" {
		t.Errorf("a key the policy does not define was lost: %v", parsed)
	}
	mcp, _ := parsed["mcp"].(map[string]any)
	if mcp["startupTimeoutMs"] != 0 {
		t.Errorf("mcp.startupTimeoutMs = %v", mcp["startupTimeoutMs"])
	}
	bash, _ := parsed["bash"].(map[string]any)
	if bash == nil || bash["patterns"] == nil {
		t.Errorf("the policy's own keys did not arrive: %v", parsed)
	}
	tools, _ := parsed["tools"].(map[string]any)
	if tools == nil || tools["approval"] == nil {
		t.Errorf("the policy's tools did not arrive: %v", parsed)
	}
}

// test_rig_policy_application_is_idempotent.
func TestRigPolicyApplicationIsIdempotent(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	rigPolicyTable = func(string) (map[string]string, bool) {
		return map[string]string{"seat1": rigPolicyDocumentFor("seat1", "dev")}, true
	}
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}
	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	before := readFile(t, config)
	stamp := rigMtime(t, config)

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if readFile(t, config) != before {
		t.Errorf("the config changed on a no-op rebuild")
	}
	if rigMtime(t, config) != stamp {
		t.Errorf("the config's mtime changed on a no-op rebuild")
	}
	if strings.Contains(stderr, "applied the per-seat policy") {
		t.Errorf("stderr = %q, want nothing applied", stderr)
	}
}

// test_rig_reports_a_seat_the_policy_table_does_not_cover.
func TestRigReportsASeatThePolicyTableDoesNotCover(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	rigPolicyTable = func(string) (map[string]string, bool) {
		return map[string]string{"somebody-else": rigPolicyDocumentFor("somebody-else", "dev")}, true
	}

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "room1/seat1 has no role") || !strings.Contains(stderr, "unpoliced") {
		t.Errorf("stderr = %q, want the unlisted seat reported", stderr)
	}
	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	if raw, err := os.ReadFile(config); err == nil {
		var parsed map[string]any
		if err := yaml.Unmarshal(raw, &parsed); err == nil {
			if _, ok := parsed["bash"]; ok {
				t.Errorf("an unlisted seat got a policy: %v", parsed)
			}
		}
	}
}

// test_rig_leaves_a_rig_outside_the_policy_table_alone.
func TestRigLeavesARigOutsideThePolicyTableAlone(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if strings.Contains(stderr, "no role") || strings.Contains(stderr, "applied the per-seat policy") ||
		strings.Contains(stderr, "unpoliced") {
		t.Errorf("stderr = %q, want no editorial line and no policy", stderr)
	}
	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	if raw, err := os.ReadFile(config); err == nil {
		var parsed map[string]any
		if err := yaml.Unmarshal(raw, &parsed); err == nil {
			if _, ok := parsed["bash"]; ok {
				t.Errorf("an ungoverned rig got a policy: %v", parsed)
			}
		}
	}
}

// test_rig_installs_the_rendered_omp_mcp_document_verbatim.
func TestRigInstallsTheRenderedOMPMCPDocumentVerbatim(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	installed := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), ".mcp.json")

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if want := "rig:" + filepath.Join(f.rigDir("room1"), "rig.yaml") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "installed") || !strings.Contains(stderr, installed) {
		t.Errorf("stderr = %q, want the install reported", stderr)
	}
	body := readFile(t, installed)
	if !strings.Contains(body, "${AGENTHUB_TOKEN}") {
		t.Errorf("the token variable was resolved away: %q", body)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("installed document is not JSON: %v", err)
	}
	if _, ok := decoded["mcpServers"].(map[string]any)["agenthub_http"]; !ok {
		t.Errorf("installed document = %v", decoded)
	}
}

// test_rig_leaves_the_operators_rig_root_mcp_json_alone.
func TestRigLeavesTheOperatorsRigRootMCPJSONAlone(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}
	operatorFile := filepath.Join(f.rigDir("room1"), ".mcp.json")
	before := "{\"mcpServers\": {\"operator\": {\"type\": \"stdio\", \"command\": \"x\"}}}\n"
	if err := os.WriteFile(operatorFile, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("second run exit = %d (%s)", code, stderr)
	}
	if got := readFile(t, operatorFile); got != before {
		t.Errorf("the operator's rig-root .mcp.json was touched: %q", got)
	}
}

// test_rig_sets_the_omp_startup_setting_without_clobbering_other_keys.
func TestRigSetsTheOMPStartupSettingWithoutClobberingOtherKeys(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	if err := os.WriteFile(config, []byte("mcp:\n  renderMarkdownResults: true\nmodel: something-else\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	parsed := rigParseYAML(t, config)
	mcp, _ := parsed["mcp"].(map[string]any)
	if mcp["startupTimeoutMs"] != 0 {
		t.Errorf("the rendered key was not set: %v", mcp)
	}
	if mcp["renderMarkdownResults"] != true {
		t.Errorf("an unrelated nested key was lost: %v", mcp)
	}
	if parsed["model"] != "something-else" {
		t.Errorf("an unrelated top-level key was lost: %v", parsed)
	}
	if !strings.Contains(stderr, config) {
		t.Errorf("stderr = %q, want the config path", stderr)
	}
}

// test_rig_writes_the_omp_config_verbatim_when_the_seat_has_none.
func TestRigWritesTheOMPConfigVerbatimWhenTheSeatHasNone(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)

	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("exit = %d (%s)", code, stderr)
	}
	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	if got := readFile(t, config); got != rigOMPConfigFragment {
		t.Errorf("config.yml = %q, want the render's own bytes", got)
	}
}

// test_rig_omp_config_merge_is_idempotent.
func TestRigOMPConfigMergeIsIdempotent(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}
	config := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml")
	before := readFile(t, config)
	stamp := rigMtime(t, config)

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if readFile(t, config) != before || rigMtime(t, config) != stamp {
		t.Errorf("the config changed on a rebuild")
	}
	if strings.Contains(stderr, config) {
		t.Errorf("stderr = %q, want the config not re-installed", stderr)
	}
}

// test_rig_warns_when_only_half_the_omp_render_is_present.
func TestRigWarnsWhenOnlyHalfTheOMPRenderIsPresent(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, false, false, true)
	installed := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), ".mcp.json")

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Errorf("the half that IS rendered was not installed: %v", err)
	}
	rigNotExist(t, filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "config.yml"))
	for _, want := range []string{"warning:", "runtime/omp-config.yml", "HALF"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}

// test_rig_install_is_idempotent.
func TestRigInstallIsIdempotent(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}
	installed := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), ".mcp.json")
	before := readFile(t, installed)
	stamp := rigMtime(t, installed)

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if readFile(t, installed) != before || rigMtime(t, installed) != stamp {
		t.Errorf("the installed document changed on a rebuild")
	}
	if strings.Contains(stderr, "installed") {
		t.Errorf("stderr = %q, want nothing re-installed", stderr)
	}
}

// test_rig_installs_the_rendered_guide_document_verbatim.
func TestRigInstallsTheRenderedGuideDocumentVerbatim(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, true, true)
	installed := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), "AGENTS.md")

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "installed") || !strings.Contains(stderr, installed) {
		t.Errorf("stderr = %q, want the guide install reported", stderr)
	}
	if got := readFile(t, installed); got != rigAgentsMDDocument {
		t.Errorf("AGENTS.md = %q, want the render's own bytes", got)
	}

	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("second run exit = %d (%s)", code, stderr)
	} else if strings.Contains(stderr, installed) {
		t.Errorf("stderr = %q, want a rebuild not to reinstall the guide", stderr)
	}
}

// test_rig_installs_the_guide_document_for_a_seat_with_no_mcp_block.
func TestRigInstallsTheGuideDocumentForASeatWithNoMCPBlock(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(false, false, true, true)
	agentDir := f.ompAgentDir("main", "seat1", "room1")

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if got := readFile(t, filepath.Join(agentDir, "AGENTS.md")); got != rigAgentsMDDocument {
		t.Errorf("AGENTS.md = %q", got)
	}
	if strings.Contains(stderr, "HALF") {
		t.Errorf("stderr = %q, want no half-render warning for a guide-only seat", stderr)
	}
	rigNotExist(t, filepath.Join(agentDir, ".mcp.json"))
	rigNotExist(t, filepath.Join(agentDir, "config.yml"))
}

// test_rig_writes_nothing_for_a_seat_with_no_mcp_block.
func TestRigWritesNothingForASeatWithNoMCPBlock(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(false, false, false, true)
	agentDir := f.ompAgentDir("main", "seat1", "room1")

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	rigNotExist(t, filepath.Join(agentDir, ".mcp.json"))
	if strings.Contains(stderr, "installed") {
		t.Errorf("stderr = %q, want nothing installed", stderr)
	}
}

// test_rig_creates_the_seat_agent_directory_of_a_rig_that_has_never_been_up.
func TestRigCreatesTheSeatAgentDirectoryOfARigThatHasNeverBeenUp(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, false)
	installed := filepath.Join(f.ompAgentDir("main", "seat1", "room1"), ".mcp.json")

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if info, err := os.Stat(installed); err != nil || !info.Mode().IsRegular() {
		t.Errorf(".mcp.json was not created for a never-launched seat: %v", err)
	}
	if info, err := os.Stat(filepath.Join(f.rigDir("room1"), "rig.yaml")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("rig.yaml missing: %v", err)
	}
}

// test_rig_links_the_one_deepseek_key_into_a_room_with_a_deepseek_seat - the trigger is now any
// provider/model member (contains a "/"), and the source is the client's one env file.
func TestRigLinksTheOneProviderKeyIntoARoomWithAProviderSeat(t *testing.T) {
	f := rigVerbTest(t)
	if err := os.WriteFile(f.keyFile, []byte("PROVIDER_KEY=placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yamlText := "name: room1\npods:\n  - id: main\n    members:\n" +
		"      - id: coder\n        agent_ref: local:agents/coder\n" +
		"        runtime: omp\n        model: provider/fast-model\n"
	f.cloud.setRigspec("room1", yamlText, []map[string]any{{"seat": "coder", "hash": rigHashA}})
	f.cloud.setSeat("room1", "coder", rigHashA, nil, nil)

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	link := filepath.Join(f.roomDir("room1"), ".env")
	if !rigIsSymlink(t, link) {
		t.Fatalf("%s is not a symlink", link)
	}
	if target, err := filepath.EvalSymlinks(link); err != nil || target != rigMustResolve(t, f.keyFile) {
		t.Errorf("link resolves to %q, %v; want %q", target, err, rigMustResolve(t, f.keyFile))
	}
}

// test_rig_links_no_env_into_a_room_without_a_deepseek_seat.
func TestRigLinksNoEnvIntoARoomWithoutAProviderSeat(t *testing.T) {
	f := rigVerbTest(t)
	if err := os.WriteFile(f.keyFile, []byte("PROVIDER_KEY=placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.cloud.setSeat("room1", "seat1", rigHashA, nil, nil)

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	rigNotExist(t, filepath.Join(f.roomDir("room1"), ".env"))
}

// test_rig_refuses_a_deepseek_room_when_the_key_file_is_absent.
func TestRigRefusesAProviderRoomWhenTheKeyFileIsAbsent(t *testing.T) {
	f := rigVerbTest(t)
	rigEnvSource = func() (string, error) { return filepath.Join(t.TempDir(), "missing-env"), nil }
	yamlText := "name: room1\npods:\n  - id: main\n    members:\n" +
		"      - id: coder\n        agent_ref: local:agents/coder\n" +
		"        model: provider/fast-model\n"
	f.cloud.setRigspec("room1", yamlText, []map[string]any{{"seat": "coder", "hash": rigHashA}})
	f.cloud.setSeat("room1", "coder", rigHashA, nil, nil)

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr, "does not exist") {
		t.Errorf("stderr = %q, want the missing key refusal", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	rigNotExist(t, f.rigDir("room1"))
}

// test_rig_refuses_to_replace_a_pre_existing_env_file_and_names_it.
func TestRigRefusesToReplaceAPreExistingEnvFileAndNamesIt(t *testing.T) {
	f := rigVerbTest(t)
	if err := os.WriteFile(f.keyFile, []byte("PROVIDER_KEY=placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yamlText := "name: room1\npods:\n  - id: main\n    members:\n" +
		"      - id: coder\n        agent_ref: local:agents/coder\n" +
		"        runtime: omp\n        model: provider/fast-model\n"
	f.cloud.setRigspec("room1", yamlText, []map[string]any{{"seat": "coder", "hash": rigHashA}})
	f.cloud.setSeat("room1", "coder", rigHashA,
		[]map[string]any{{"path": rigOMPMCPRenderPath, "content": rigOMPMCPDocumentJSON}}, nil)

	roomDir := f.roomDir("room1")
	if err := os.MkdirAll(roomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	operatorEnv := filepath.Join(roomDir, ".env")
	if err := os.WriteFile(operatorEnv, []byte("OPERATOR_OWN=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr, operatorEnv) {
		t.Errorf("stderr = %q, want the refused path named", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if rigIsSymlink(t, operatorEnv) {
		t.Errorf("the operator's .env was replaced with a symlink")
	}
	if got := readFile(t, operatorEnv); got != "OPERATOR_OWN=1\n" {
		t.Errorf("the operator's .env content changed: %q", got)
	}
	rigNotExist(t, f.rigDir("room1"))
	// The resolve pass must not have created anything under the live state root either.
	rigNotExist(t, f.ompAgentDir("main", "coder", "room1"))
}

// test_rig_retargets_a_stale_key_link_and_leaves_its_old_target_alone.
func TestRigRetargetsAStaleKeyLinkAndLeavesItsOldTargetAlone(t *testing.T) {
	f := rigVerbTest(t)
	if err := os.WriteFile(f.keyFile, []byte("PROVIDER_KEY=placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yamlText := "name: room1\npods:\n  - id: main\n    members:\n" +
		"      - id: coder\n        agent_ref: local:agents/coder\n" +
		"        runtime: omp\n        model: provider/fast-model\n"
	f.cloud.setRigspec("room1", yamlText, []map[string]any{{"seat": "coder", "hash": rigHashA}})
	f.cloud.setSeat("room1", "coder", rigHashA, nil, nil)

	roomDir := f.roomDir("room1")
	if err := os.MkdirAll(roomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldTarget := filepath.Join(f.out, "moved-env")
	if err := os.WriteFile(oldTarget, []byte("OLD=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(roomDir, ".env")
	if err := os.Symlink(oldTarget, link); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if !rigIsSymlink(t, link) {
		t.Fatalf("the stale link was not retargeted")
	}
	if target, err := filepath.EvalSymlinks(link); err != nil || target != rigMustResolve(t, f.keyFile) {
		t.Errorf("link resolves to %q, %v; want %q", target, err, rigMustResolve(t, f.keyFile))
	}
	if got := readFile(t, oldTarget); got != "OLD=1\n" {
		t.Errorf("the old link target was touched: %q", got)
	}
}

// test_rig_update_moves_pin_and_materialized_agent.
func TestRigUpdateMovesPinAndMaterializedAgent(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setSeat("room1", "seat1", rigHashA, nil, nil)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}

	f.cloud.setRigspec("room1", rigDefaultYAML, []map[string]any{{"seat": "seat1", "hash": rigHashB}})
	f.cloud.setSeat("room1", "seat1", rigHashB, []map[string]any{{"path": "docs/readme.md", "content": "newer"}}, nil)
	code, stdout, stderr := f.run("room1", "--out", f.out, "--update")

	seatDir := filepath.Join(f.roomDir("room1"), "seat1")
	rigDir := f.rigDir("room1")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if lock := readJSON(t, filepath.Join(seatDir, "pinned.json")); lock["hash"] != rigHashB {
		t.Errorf("pin = %v, want the new hash", lock)
	}
	agent := filepath.Join(rigDir, "agents", "seat1")
	if got := readFile(t, filepath.Join(agent, "docs", "readme.md")); got != "newer" {
		t.Errorf("agent docs/readme.md = %q", got)
	}
	if lock := readJSON(t, filepath.Join(agent, "pinned.json")); lock["hash"] != rigHashB {
		t.Errorf("agent pinned.json = %v", lock)
	}
	if want := "rig:" + filepath.Join(rigDir, "rig.yaml") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// test_rig_missing_pinned_dir_exits_1.
func TestRigMissingPinnedDirExits1(t *testing.T) {
	f := rigVerbTest(t)
	seatDir := filepath.Join(f.roomDir("room1"), "seat1")
	if err := os.MkdirAll(seatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := fmt.Sprintf("{\"hash\": %q, \"path\": %q}\n", rigHashA, filepath.Join(seatDir, rigHashA))
	if err := os.WriteFile(filepath.Join(seatDir, "pinned.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	f.cloud.setRigspec("room1", rigDefaultYAML, []map[string]any{{"seat": "seat1", "hash": rigHashB}})
	f.cloud.setSeat("room1", "seat1", rigHashB, nil, nil)

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitRemote)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	for _, want := range []string{"seat seat1 could not be pulled", "pinned seat directory is missing"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	rigNotExist(t, f.rigDir("room1"))
}

// test_rig_empty_room_409_exits_1_with_server_message.
func TestRigEmptyRoomExits1WithServerMessage(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setRigspecStatus(409, "room has no active seats")

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitRemote)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	for _, want := range []string{"HTTP 409", "room has no active seats"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	rigNotExist(t, f.rigDir("room1"))
}

// test_rig_unsafe_room_name_rejected.
func TestRigUnsafeRoomNameRejected(t *testing.T) {
	for _, bad := range []string{"../etc", "_x", "", "a/b", "room.1", "room name"} {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			f := rigVerbTest(t)
			code, stdout, stderr := f.run(bad, "--out", f.out)
			if code != clientcmd.ExitUsage {
				t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, "invalid room name") {
				t.Errorf("stderr = %q, want the invalid room name", stderr)
			}
		})
	}
}

// test_rig_unsafe_seat_name_rejected.
func TestRigUnsafeSeatNameRejected(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setRigspec("room1", rigDefaultYAML, []map[string]any{{"seat": "seat.1", "hash": rigHashA}})

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "invalid seat name") {
		t.Errorf("stderr = %q, want the invalid seat name", stderr)
	}
	rigNotExist(t, f.rigDir("room1"))
}

// test_rig_failed_seat_pull_leaves_previous_rig_untouched.
func TestRigFailedSeatPullLeavesPreviousRigUntouched(t *testing.T) {
	f := rigVerbTest(t)
	f.cloud.setSeat("room1", "seat1", rigHashA, nil, nil)
	if code, _, stderr := f.run("room1", "--out", f.out); code != clientcmd.ExitOK {
		t.Fatalf("first run exit = %d (%s)", code, stderr)
	}
	rigDir := f.rigDir("room1")
	oldYAML := readFile(t, filepath.Join(rigDir, "rig.yaml"))

	f.cloud.setRigspec("room1", rigDefaultYAML+"# changed\n", []map[string]any{{"seat": "seat1", "hash": rigHashB}})
	f.cloud.setSeatStatus("room1", "seat1", 500)
	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitRemote {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitRemote)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "seat seat1 could not be pulled") {
		t.Errorf("stderr = %q", stderr)
	}
	if readFile(t, filepath.Join(rigDir, "rig.yaml")) != oldYAML {
		t.Errorf("the previous rig.yaml changed")
	}
	agent := filepath.Join(rigDir, "agents", "seat1")
	if info, err := os.Stat(agent); err != nil || !info.IsDir() || rigIsSymlink(t, agent) {
		t.Errorf("the previous agent directory was disturbed: %v", err)
	}
	if lock := readJSON(t, filepath.Join(agent, "pinned.json")); lock["hash"] != rigHashA {
		t.Errorf("the previous pin was disturbed: %v", lock)
	}
	entries, err := os.ReadDir(f.roomDir("room1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".rig.") {
			t.Errorf("a staging directory was left behind: %s", entry.Name())
		}
	}
}

// test_rig_installs_under_the_state_root_it_is_given.
func TestRigInstallsUnderTheStateRootItIsGiven(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	chosen := filepath.Join(t.TempDir(), "chosen")

	code, _, stderr := f.run("room1", "--out", f.out, "--state-root", chosen)

	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr)
	}
	installed := filepath.Join(chosen, rigSessionName("main", "seat1", "room1"), "agent", ".mcp.json")
	if info, err := os.Stat(installed); err != nil || !info.Mode().IsRegular() {
		t.Errorf("nothing installed under the chosen root: %v", err)
	}
	if !strings.Contains(stderr, installed) {
		t.Errorf("stderr = %q, want the install path", stderr)
	}
	rigNotExist(t, filepath.Join(f.ompAgentDir("main", "seat1", "room1"), ".mcp.json"))
}

// test_rig_refuses_when_a_running_seat_uses_another_state_root.
func TestRigRefusesWhenARunningSeatUsesAnotherStateRoot(t *testing.T) {
	f := rigVerbTest(t)
	f.ompRig(true, true, false, true)
	rigLaunchStateRoot = func(string) string { return "/other/state/omp" }

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	for _, want := range []string{f.ompRoot, "/other/state/omp", "--state-root"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
	rigNotExist(t, f.roomDir("room1"))
}

// test_the_launch_state_root_comes_from_the_running_seat.
func TestRigLaunchStateRootComesFromTheRunningSeat(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("launch_state_root reads /proc, which exists on linux only")
	}
	originalRoot, originalLaunch := rigProcRoot, rigLaunchStateRoot
	t.Cleanup(func() { rigProcRoot, rigLaunchStateRoot = originalRoot, originalLaunch })
	rigProcRoot = t.TempDir()
	rigLaunchStateRoot = rigProcLaunchStateRoot

	session := "main-seat1@room1"
	if err := os.MkdirAll(filepath.Join(rigProcRoot, "1798"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmdline := strings.Join([]string{
		"node", "/home/x/pi-runner.js", "--session-name", session, "--state-root", "/other/state/omp",
		"--cwd", "/tmp/room1", "--launch-id", "abc",
	}, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(rigProcRoot, "1798", "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigProcRoot, "1800"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := strings.Join([]string{"node", "pi-runner.js", "--session-name", "main-other@room1", "--state-root", "/elsewhere"}, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(rigProcRoot, "1800", "cmdline"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := rigLaunchStateRoot(session); got != "/other/state/omp" {
		t.Errorf("launch_state_root(%s) = %q", session, got)
	}
	if got := rigLaunchStateRoot("main-other@room1"); got != "/elsewhere" {
		t.Errorf("launch_state_root(main-other@room1) = %q", got)
	}
	if got := rigLaunchStateRoot("main-missing@room1"); got != "" {
		t.Errorf("launch_state_root(main-missing@room1) = %q, want nothing", got)
	}
}

// test_pull_and_rig_fail_loudly_without_the_link (the rig half): the real require_checker refuses when
// the bare name does not resolve to the store's binary, and nothing is pulled.
func TestRigFailsLoudlyWithoutTheLink(t *testing.T) {
	f := rigVerbTest(t)
	rigRequireChecker = rigCheckChecker
	originalSeatPath := seatPath
	t.Cleanup(func() { seatPath = originalSeatPath })
	seatPath = func() (string, string) { return t.TempDir(), shellPathSource }

	code, stdout, stderr := f.run("room1", "--out", f.out)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "does not resolve") {
		t.Errorf("stderr = %q, want the checker refusal", stderr)
	}
	rigNotExist(t, f.roomDir("room1"))
}
