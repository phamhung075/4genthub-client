// These tests pin the `room sync` CONTRACT: the route and its bearer, the ROOM default, what a refusal says,
// and that a drift is data rather than a failure. The route's own derivation (newest report wins, rows by
// seat) belongs to the hub's tests; this package renders what it is given.
package clientroom

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// syncAnswerBody is the wire shape documented at hub 9c3e3013, including one drifted row: a drift is an
// ordinary answer, not a refusal.
const syncAnswerBody = `{"success":true,"room":"room-a","seats":[` +
	`{"seat":"architect","expected_hash":"e1","pinned_hash":"e1","machine_id":"m1","reported_at":"2026-10-19T00:00:00Z","sync":"in_sync"},` +
	`{"seat":"fe-dev","expected_hash":"e2","pinned_hash":"p9","machine_id":"m1","reported_at":"2026-10-19T00:00:01Z","sync":"drift"}]}`

// syncServer answers GET with the given status and body, and records what it was asked.
type syncServer struct {
	*httptest.Server
	method string
	path   string
	auth   string
	calls  int
}

func newSyncServer(t *testing.T, status int, body string) *syncServer {
	t.Helper()
	s := &syncServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.method, s.path, s.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

// runSyncTest runs the verb with the server's URL in $AGENTHUB_URL and a token present, unless a test says
// otherwise afterwards.
func runSyncTest(t *testing.T, serverURL string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("AGENTHUB_URL", serverURL)
	t.Setenv("AGENTHUB_TOKEN", "tok")
	t.Setenv("AGENTHUB_ROOM", "")
	var stdout, stderr bytes.Buffer
	code := runSync(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestSyncGetsTheRoomRouteAndRendersTheRows(t *testing.T) {
	server := newSyncServer(t, http.StatusOK, syncAnswerBody)
	code, stdout, stderr := runSyncTest(t, server.URL, "room-a")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if server.method != http.MethodGet || server.path != "/api/v2/rooms/room-a/sync" {
		t.Errorf("%s %s, want GET /api/v2/rooms/room-a/sync", server.method, server.path)
	}
	if server.auth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the bearer token", server.auth)
	}
	for _, want := range []string{
		"room: room-a@" + server.URL,
		"SEAT", "SYNC", "EXPECTED", "PINNED", "MACHINE", "REPORTED",
		"architect", "in_sync", "e1", "m1", "2026-10-19T00:00:00Z",
		"fe-dev", "drift", "e2", "p9",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
}

func TestSyncJSONPrintsTheAnswerVerbatim(t *testing.T) {
	server := newSyncServer(t, http.StatusOK, syncAnswerBody)
	code, stdout, stderr := runSyncTest(t, server.URL, "room-a", "--json")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	var got, want map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if err := json.Unmarshal([]byte(syncAnswerBody), &want); err != nil {
		t.Fatal(err)
	}
	if len(got["seats"].([]any)) != len(want["seats"].([]any)) {
		t.Errorf("seats = %v, want the server's %v", got["seats"], want["seats"])
	}
	if strings.Contains(stdout, "SEAT\t") || strings.Contains(stdout, "room: room-a@") {
		t.Errorf("--json rendered the table as well:\n%s", stdout)
	}
}

func TestSyncRoomDefaultsToTheEnvironment(t *testing.T) {
	server := newSyncServer(t, http.StatusOK, `{"success":true,"room":"room-b","seats":[]}`)
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok")
	t.Setenv("AGENTHUB_ROOM", "room-b")
	var stdout, stderr bytes.Buffer
	code := runSync(context.Background(), nil, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if server.path != "/api/v2/rooms/room-b/sync" {
		t.Errorf("path = %q, want the room $AGENTHUB_ROOM named", server.path)
	}
}

func TestSyncWithoutARoomRefusesAndNamesTheVariable(t *testing.T) {
	server := newSyncServer(t, http.StatusOK, syncAnswerBody)
	code, _, stderr := runSyncTest(t, server.URL)
	if code != clientcmd.ExitRoomRefused {
		t.Fatalf("exit = %d, want %d", code, clientcmd.ExitRoomRefused)
	}
	if !strings.Contains(stderr, "AGENTHUB_ROOM") || server.calls != 0 {
		t.Errorf("stderr = %q, calls = %d; want the variable named and no request", stderr, server.calls)
	}
}

func TestSyncRefusesMoreThanOneRoomBeforeDialling(t *testing.T) {
	server := newSyncServer(t, http.StatusOK, syncAnswerBody)
	code, _, stderr := runSyncTest(t, server.URL, "room-a", "room-b")
	if code != clientcmd.ExitRoomRefused || server.calls != 0 {
		t.Fatalf("exit = %d, calls = %d; want a refusal with no request", code, server.calls)
	}
	if !strings.Contains(stderr, "one room at most") {
		t.Errorf("stderr = %q, want the refusal to say why", stderr)
	}
}

func TestSyncWithoutURLOrTokenExitsUsage(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "tok")
	var stdout, stderr bytes.Buffer
	if code := runSync(context.Background(), []string{"room-a"}, &stdout, &stderr); code != clientcmd.ExitUsage {
		t.Errorf("a missing URL exits %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "AGENTHUB_URL") {
		t.Errorf("stderr = %q, want the variable named", stderr.String())
	}

	t.Setenv("AGENTHUB_URL", "https://example.invalid")
	t.Setenv("AGENTHUB_TOKEN", "")
	stdout.Reset()
	stderr.Reset()
	if code := runSync(context.Background(), []string{"room-a"}, &stdout, &stderr); code != clientcmd.ExitUsage {
		t.Errorf("a missing token exits %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr.String(), "AGENTHUB_TOKEN") {
		t.Errorf("stderr = %q, want the variable named", stderr.String())
	}
}

func TestSyncReportsTheServersRefusalInItsOwnWords(t *testing.T) {
	server := newSyncServer(t, http.StatusNotFound, `{"detail":"room not found"}`)
	code, _, stderr := runSyncTest(t, server.URL, "ghost")
	if code != clientcmd.ExitRemote {
		t.Fatalf("exit = %d, want %d", code, clientcmd.ExitRemote)
	}
	if !strings.Contains(stderr, "HTTP 404") || !strings.Contains(stderr, "room not found") {
		t.Errorf("stderr = %q, want the status and the detail", stderr)
	}
}

func TestSyncSaysAbsenceIsNeverInSync(t *testing.T) {
	server := newSyncServer(t, http.StatusOK, `{"success":true,"room":"room-a","seats":[]}`)
	code, stdout, stderr := runSyncTest(t, server.URL, "room-a")
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "absence is never in_sync") || !strings.Contains(stdout, "/api/v2/rooms/room-a/seats") {
		t.Errorf("stdout = %q, want the absence rule and the roster route", stdout)
	}
}
