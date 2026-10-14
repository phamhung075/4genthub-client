package clientsync

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// TestParseStatusArgsMirrorsTheSubparser: a positional room, --out with the seat-store default, and a
// repeatable --seat; anything else is a usage error, which is argparse's exit 2.
func TestParseStatusArgsMirrorsTheSubparser(t *testing.T) {
	room, out, seats, err := parseStatusArgs([]string{"dev-min"})
	if err != nil || room != "dev-min" || len(seats) != 0 {
		t.Fatalf("room=%q out=%q seats=%v err=%v", room, out, seats, err)
	}
	if !strings.HasSuffix(out, filepath.Join(".config", "4genthub", "seats")) {
		t.Errorf("default out = %q, want the Python's seat store", out)
	}

	room, out, seats, err = parseStatusArgs([]string{"cd", "--out", "/tmp/store", "--seat", "lead", "--seat=go-dev"})
	if err != nil || room != "cd" || out != "/tmp/store" || strings.Join(seats, ",") != "lead,go-dev" {
		t.Fatalf("room=%q out=%q seats=%v err=%v", room, out, seats, err)
	}

	for _, bad := range [][]string{
		{},              // no room
		{"a", "b"},      // two positionals
		{"a", "--nope"}, // unknown flag
		{"a", "--seat"}, // a flag with no value
		{"a", "--out"},  // likewise
	} {
		if _, _, _, err := parseStatusArgs(bad); err == nil {
			t.Errorf("parseStatusArgs(%v) accepted it, want a usage error", bad)
		}
	}
}

// TestPinnedHashesAbsenceAndEmpty pins the two cases that look the same and are not: no lock file at all
// is ABSENCE (the Python's None, which reads "not pulled"), while a lock carrying an empty hash is a
// pin that renders BEHIND.
func TestPinnedHashesAbsenceAndEmpty(t *testing.T) {
	out := t.TempDir()
	room := "cd"
	write := func(seat, body string) {
		t.Helper()
		dir := filepath.Join(out, room, seat)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pinned.json"), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write("lead", `{"hash":"h2","path":"/tmp/store/lead"}`)
	write("writer", `{"hash":"","path":"/tmp/store/writer"}`)

	pins, err := PinnedHashes(out, room, []string{"lead", "go-dev", "writer"})
	if err != nil {
		t.Fatalf("PinnedHashes: %v", err)
	}
	if pins["lead"] != "h2" {
		t.Errorf("lead pin = %q, want h2", pins["lead"])
	}
	if _, present := pins["go-dev"]; present {
		t.Errorf("go-dev has no lock file, so it must be ABSENT (the Python's None), got %q", pins["go-dev"])
	}
	if got, present := pins["writer"]; !present || got != "" {
		t.Errorf("writer = %q present=%v, want an EMPTY pin present (a lock exists)", got, present)
	}

	// And the status words those two produce, which is the point of the distinction.
	cloud := map[string]string{"lead": "h2", "go-dev": "h1", "writer": "h3"}
	if got := StatusState("go-dev", cloud, pins); got != "not pulled" {
		t.Errorf("no lock = %q, want not pulled", got)
	}
	if got := StatusState("writer", cloud, pins); got != "BEHIND" {
		t.Errorf("empty pin = %q, want BEHIND", got)
	}
}

// TestRunStatusVerbMapsFailuresThePythonsWay pins the mapping a port gets wrong: the helpers raise with
// EXIT_REMOTE (1), and the CLIENT's main maps everything that is not EXIT_USAGE (2) to EXIT_FAILED (3).
// So a cloud that cannot be read exits 3 here - and the test would have caught the tempting
// `clientcmd.CodeOf(err, ...)`, which returns the helper's own 1.
func TestRunStatusVerbMapsFailuresThePythonsWay(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "http://127.0.0.1:1") // nothing listens: a network failure
	t.Setenv("AGENTHUB_TOKEN", "tok")
	var out, errOut bytes.Buffer
	code := RunStatusVerb(context.Background(), []string{"cd"}, &out, &errOut)
	if code != clientcmd.ExitUnavailable {
		t.Errorf("a cloud that cannot be read exited %d, want %d (the Python main's EXIT_FAILED), not %d",
			code, clientcmd.ExitUnavailable, clientcmd.ExitRemote)
	}
	if !strings.Contains(errOut.String(), "failed:") {
		t.Errorf("stderr = %q, want the helper's message", errOut.String())
	}

	// A missing env var is a USAGE error, not a remote one.
	t.Setenv("AGENTHUB_URL", "")
	out.Reset()
	errOut.Reset()
	if code := RunStatusVerb(context.Background(), []string{"cd"}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("a missing AGENTHUB_URL exited %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "AGENTHUB_URL is not set") {
		t.Errorf("stderr = %q, want the Python's message", errOut.String())
	}
}

// TestRunStatusVerbEndToEnd is the verb as a whole: a cloud answering one rigspec, one seat in sync and
// one behind, rendered in the cloud's order with the exit code a script checks.
func TestRunStatusVerbEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RoomsPath+"/cd/rigspec" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"rigspec":{"seats":[` +
			`{"seat":"writer","hash":"h3"},{"seat":"lead","hash":"h2"}]}}`))
	}))
	defer server.Close()

	out := t.TempDir()
	dir := filepath.Join(out, "cd", "lead")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pinned.json"), []byte(`{"hash":"h2","path":"/tmp/store/lead"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok")
	var stdout, stderr bytes.Buffer
	code := RunStatusVerb(context.Background(), []string{"cd", "--out", out}, &stdout, &stderr)
	if code != clientcmd.ExitBehind {
		t.Fatalf("exit = %d, want %d (writer has no lock)\nstderr: %s", code, clientcmd.ExitBehind, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("printed %d lines, want two (one per seat):\n%s", len(lines), stdout.String())
	}
	if !strings.HasPrefix(lines[0], "writer") || !strings.Contains(lines[0], "not pulled") {
		t.Errorf("line 1 = %q, want the cloud's FIRST seat (writer, which has no lock) and not pulled", lines[0])
	}
	if !strings.Contains(lines[1], "in sync") {
		t.Errorf("line 2 = %q, want lead in sync", lines[1])
	}

	// And the --seat filter narrows to one seat.
	stdout.Reset()
	if code := RunStatusVerb(context.Background(), []string{"cd", "--out", out, "--seat", "lead"}, &stdout, &stderr); code != clientcmd.ExitOK {
		t.Errorf("filtered exit = %d, want %d: only lead, which is in sync", code, clientcmd.ExitOK)
	}
	if got := strings.Count(stdout.String(), "\n"); got != 1 {
		t.Errorf("filtered output has %d lines, want 1:\n%s", got, stdout.String())
	}
}

// test_the_store_and_state_root_do_not_follow_home pins the Python test of that name: the store and the
// state root are resolved from the ACCOUNT home, so a decoy $HOME - what the rig orchestrator gives a seat - does not
// move either of them.
func TestTheStoreAndStateRootDoNotFollowHome(t *testing.T) {
	decoy := t.TempDir()
	t.Setenv("HOME", decoy)

	home, err := clientenv.RealHome()
	if err != nil {
		t.Skipf("no account home on this machine: %v", err)
	}
	store, err := clientenv.ResolveSeatStore("")
	if err != nil {
		t.Fatalf("ResolveSeatStore: %v", err)
	}
	if want := filepath.Join(home, ".config", "4genthub", "seats"); store != want {
		t.Errorf("the store followed $HOME: %q, want %q", store, want)
	}
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		t.Fatalf("ResolveStateRoot: %v", err)
	}
	if want := filepath.Join(home, ".config", "4genthub"); root != want {
		t.Errorf("the state root followed $HOME: %q, want %q", root, want)
	}
	if strings.HasPrefix(store, decoy) || strings.HasPrefix(root, decoy) {
		t.Errorf("a resolved path is under the decoy home: store=%q root=%q", store, root)
	}
}

// Every sync verb that resolves a store here takes --out, so its refusal names the flag. The machine
// cannot resolve a home on demand, so the seam supplies the failure and the composition under test is
// the real one.
func TestTheStoreRefusalNamesTheOutFlagTheSyncVerbsTake(t *testing.T) {
	original := seatStore
	seatStore = func(string) (string, error) {
		return "", &clientcmd.CodedError{Message: clientenv.SeatStoreMessage, Code: clientcmd.ExitUsage}
	}
	t.Cleanup(func() { seatStore = original })

	var out, errOut bytes.Buffer
	code := RunStatusVerb(context.Background(), []string{"room1"}, &out, &errOut)
	if code != clientcmd.ExitUsage {
		t.Fatalf("exit %d, want %d: %s", code, clientcmd.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), clientenv.SeatStoreOutFlag) {
		t.Errorf("the refusal does not name the flag the verb accepts: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), clientenv.SeatStoreMessage) {
		t.Errorf("the refusal dropped the reason: %q", errOut.String())
	}
}
