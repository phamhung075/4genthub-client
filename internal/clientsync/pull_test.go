package clientsync

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// fakeCloud is the Python fixture's env.set_seat: one resolved seat, whose hash and files the test
// changes between calls to stand in for a newer snapshot.
type fakeCloud struct {
	room, seat string
	hash       string
	files      []map[string]any
	policy     map[string]any
}

func (c *fakeCloud) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathsPath+"/"+c.room+"/"+c.seat {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		files := make([]any, 0, len(c.files))
		for _, f := range c.files {
			files = append(files, f)
		}
		payload := map[string]any{
			"success": true,
			"resolved_seat": map[string]any{
				"room": c.room, "seat": c.seat, "hash": c.hash,
				"files": files, "policy": c.policy,
			},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode: %v", err)
		}
	}
}

func pullTestSetup(t *testing.T, cloud *fakeCloud) (out string, stdout, stderr *bytes.Buffer) {
	t.Helper()
	server := httptest.NewServer(cloud.handler(t))
	t.Cleanup(server.Close)
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok")
	return t.TempDir(), &bytes.Buffer{}, &bytes.Buffer{}
}

// TestPullWritesFilesAndCreatesLock is the Python spec test_pull_writes_files_and_creates_lock: exit 0,
// stdout EXACTLY the path line, stderr EMPTY, the files written through nested directories, the lock
// holding the hash and the snapshot path, and the policy written beside them.
func TestPullWritesFilesAndCreatesLock(t *testing.T) {
	const hashA = "h1a2b3c4"
	cloud := &fakeCloud{room: "room1", seat: "seat1", hash: hashA, policy: map[string]any{"name": "p1"},
		files: []map[string]any{
			{"path": "docs/readme.md", "content": "hello"},
			{"path": "nested/deeper/notes.txt", "content": "notes"},
		}}
	out, stdout, stderr := pullTestSetup(t, cloud)

	code := RunPullVerb(context.Background(), []string{"room1", "seat1", "--out", out}, stdout, stderr)

	seatDir := filepath.Join(out, "room1", "seat1")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if want := "path:" + filepath.Join(seatDir, hashA) + "\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on a first pull", stderr.String())
	}
	if got := readFile(t, filepath.Join(seatDir, hashA, "docs", "readme.md")); got != "hello" {
		t.Errorf("docs/readme.md = %q", got)
	}
	if got := readFile(t, filepath.Join(seatDir, hashA, "nested", "deeper", "notes.txt")); got != "notes" {
		t.Errorf("nested/deeper/notes.txt = %q", got)
	}
	lock := readJSON(t, filepath.Join(seatDir, "pinned.json"))
	if lock["hash"] != hashA || lock["path"] != filepath.Join(seatDir, hashA) {
		t.Errorf("lock = %v, want the hash and the snapshot path", lock)
	}
	if policy := readJSON(t, filepath.Join(seatDir, "policy.json")); policy["name"] != "p1" {
		t.Errorf("policy.json = %v, want the resolved policy", policy)
	}
}

// TestSecondPullKeepsLockAndPrintsNotice is test_second_pull_keeps_lock_and_prints_notice: a newer cloud
// WITHOUT --update announces the newer hash on STDERR, keeps the pin where it was, does not materialize
// the new snapshot, and leaves the pinned files exactly as they were.
func TestSecondPullKeepsLockAndPrintsNotice(t *testing.T) {
	const hashA, hashB = "h1a2b3c4", "hb5b6b7b"
	cloud := &fakeCloud{room: "room1", seat: "seat1", hash: hashA,
		files: []map[string]any{{"path": "docs/readme.md", "content": "hello"}}}
	out, stdout, stderr := pullTestSetup(t, cloud)
	if code := RunPullVerb(context.Background(), []string{"room1", "seat1", "--out", out}, stdout, stderr); code != clientcmd.ExitOK {
		t.Fatalf("first pull exit = %d", code)
	}
	stdout.Reset()
	stderr.Reset()

	cloud.hash = hashB
	cloud.files = []map[string]any{{"path": "docs/readme.md", "content": "newer"}}
	code := RunPullVerb(context.Background(), []string{"room1", "seat1", "--out", out}, stdout, stderr)

	seatDir := filepath.Join(out, "room1", "seat1")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (a newer snapshot is a notice, not a failure)", code)
	}
	if want := "path:" + filepath.Join(seatDir, hashA) + "\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want the PINNED path %q", stdout.String(), want)
	}
	if want := "newer snapshot available: " + hashB + " (run with --update to adopt)\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if lock := readJSON(t, filepath.Join(seatDir, "pinned.json")); lock["hash"] != hashA {
		t.Errorf("lock = %v, want the pin unmoved", lock)
	}
	if _, err := os.Stat(filepath.Join(seatDir, hashB)); !os.IsNotExist(err) {
		t.Errorf("the newer snapshot was materialized without --update")
	}
	if got := readFile(t, filepath.Join(seatDir, hashA, "docs", "readme.md")); got != "hello" {
		t.Errorf("the pinned file changed: %q", got)
	}
}

// TestUpdateMovesLockAndMaterializesNewHash is test_update_moves_lock_and_materializes_new_hash: --update
// writes the new snapshot, moves the pin to it, and leaves the PREVIOUS snapshot untouched - the
// immutability that lets a running seat keep reading the files it was launched with.
func TestUpdateMovesLockAndMaterializesNewHash(t *testing.T) {
	const hashA, hashB = "h1a2b3c4", "hb5b6b7b"
	cloud := &fakeCloud{room: "room1", seat: "seat1", hash: hashA,
		files: []map[string]any{{"path": "docs/readme.md", "content": "hello"}}}
	out, stdout, stderr := pullTestSetup(t, cloud)
	if code := RunPullVerb(context.Background(), []string{"room1", "seat1", "--out", out}, stdout, stderr); code != clientcmd.ExitOK {
		t.Fatalf("first pull exit = %d", code)
	}
	stdout.Reset()
	stderr.Reset()

	cloud.hash = hashB
	cloud.files = []map[string]any{{"path": "docs/readme.md", "content": "newer"}}
	code := RunPullVerb(context.Background(), []string{"room1", "seat1", "--out", out, "--update"}, stdout, stderr)

	seatDir := filepath.Join(out, "room1", "seat1")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if want := "path:" + filepath.Join(seatDir, hashB) + "\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want the NEW path %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty when the update was asked for", stderr.String())
	}
	if lock := readJSON(t, filepath.Join(seatDir, "pinned.json")); lock["hash"] != hashB {
		t.Errorf("lock = %v, want the pin moved to the new hash", lock)
	}
	if got := readFile(t, filepath.Join(seatDir, hashB, "docs", "readme.md")); got != "newer" {
		t.Errorf("new snapshot file = %q, want newer", got)
	}
	if got := readFile(t, filepath.Join(seatDir, hashA, "docs", "readme.md")); got != "hello" {
		t.Errorf("the PREVIOUS snapshot changed: %q - hash directories are immutable", got)
	}
}

// TestPullRefusesWhenThePinnedDirectoryIsGone pins the refusal the docs comment promises: a pin whose
// snapshot is missing is EXIT_USAGE with a message that says what to fix, rather than a silent fallback
// to a different snapshot.
func TestPullRefusesWhenThePinnedDirectoryIsGone(t *testing.T) {
	const hashA, hashB = "h1a2b3c4", "hb5b6b7b"
	cloud := &fakeCloud{room: "room1", seat: "seat1", hash: hashA,
		files: []map[string]any{{"path": "docs/readme.md", "content": "hello"}}}
	out, stdout, stderr := pullTestSetup(t, cloud)
	if code := RunPullVerb(context.Background(), []string{"room1", "seat1", "--out", out}, stdout, stderr); code != clientcmd.ExitOK {
		t.Fatalf("first pull exit = %d", code)
	}
	stdout.Reset()
	stderr.Reset()

	seatDir := filepath.Join(out, "room1", "seat1")
	if err := os.RemoveAll(filepath.Join(seatDir, hashA)); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	cloud.hash = hashB
	code := RunPullVerb(context.Background(), []string{"room1", "seat1", "--out", out}, stdout, stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d (a usage error: the store, not the cloud)", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "pinned seat directory is missing") ||
		!strings.Contains(stderr.String(), "refusing silent fallback") {
		t.Errorf("stderr = %q, want the refusal and what to do about it", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: the pull refused", stdout.String())
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(raw)
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &decoded); err != nil {
		t.Fatalf("Unmarshal(%s): %v", path, err)
	}
	return decoded
}

// test_unsafe_room_name_rejected: pull validates its room exactly as its sibling verbs do, refusing
// before any network call with the name it refused.
func TestPullRejectsAnUnsafeRoomName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunPullVerb(context.Background(), []string{"../escape", "seat1"}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "invalid room name") || !strings.Contains(stderr.String(), "../escape") {
		t.Errorf("stderr = %q, want it to name the invalid room", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

// test_unsafe_seat_name_rejected: likewise for the seat, refused before any network call.
func TestPullRejectsAnUnsafeSeatName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunPullVerb(context.Background(), []string{"room1", "bad/name"}, &stdout, &stderr)

	if code != clientcmd.ExitUsage {
		t.Errorf("exit = %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "invalid seat name") || !strings.Contains(stderr.String(), "bad/name") {
		t.Errorf("stderr = %q, want it to name the invalid seat", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}
