package clientsync

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// renderCloud answers ONE resolved seat with the file list a test hands it, and nothing else: a seat the hub
// does not know is the 404 its own API answers with, which is the case a scratch room is in.
type renderCloud struct {
	room, seat string
	files      []map[string]any
}

func (c *renderCloud) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathsPath+"/"+c.room+"/"+c.seat {
			http.Error(w, `{"detail":"seat not found"}`, http.StatusNotFound)
			return
		}
		files := make([]any, 0, len(c.files))
		for _, f := range c.files {
			files = append(files, f)
		}
		body, err := json.Marshal(map[string]any{
			"success": true,
			"resolved_seat": map[string]any{
				"room": c.room, "seat": c.seat, "hash": "abc",
				"files": files, "policy": map[string]any{},
			},
		})
		if err != nil {
			t.Errorf("encode: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// renderSetup stands up a cloud that knows one seat, points the env at it, and returns the two buffers
// SeatDocumentFor reports on.
func renderSetup(t *testing.T, files ...map[string]any) (cloud *renderCloud, said *bytes.Buffer) {
	t.Helper()
	cloud = &renderCloud{room: "room1", seat: "seat1", files: files}
	server := httptest.NewServer(cloud.handler(t))
	t.Cleanup(server.Close)
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok")
	return cloud, &bytes.Buffer{}
}

const renderDoc = "bash:\n  allowCompoundCommands: true\ntools:\n  approval:\n    edit: deny\n"

// THE DOCUMENT IS THE HUB'S, KEY FOR KEY. This is the whole point of the change: the file the runtime
// installs carries what the hub's render says, and the client adds only its own non-policy key.
func TestTheSeatDocumentIsTheHubRenderPlusTheClientsOwnKey(t *testing.T) {
	_, said := renderSetup(t,
		map[string]any{"path": "AGENTS.md", "content": "guide"},
		map[string]any{"path": SeatRenderPath, "content": renderDoc},
	)

	document := SeatDocumentFor(context.Background(), "room1", "seat1", said)

	if document == nil {
		t.Fatal("a seat whose render carries the document must be given one")
	}
	got := *document
	if !strings.HasPrefix(got, renderDoc) {
		t.Errorf("document = %q, want the render's own bytes first", got)
	}
	if !strings.Contains(got, "compaction:\n  keepRecentTokens: 8000") {
		t.Errorf("document = %q, want the client's own compaction key", got)
	}
	if said.String() != "" {
		t.Errorf("said = %q, want nothing on the happy path", said.String())
	}
	// The render itself is untouched: the added key is a suffix, which is what lets a reader diff the
	// installed file against the hub's answer and see the one difference.
	if got != InstallDocument(renderDoc) {
		t.Errorf("document = %q, want exactly InstallDocument(render)", got)
	}
}

// InstallDocument APPENDS, and a render that already states the key is left alone: a second `compaction:`
// would be a duplicate key in the YAML rather than a default.
func TestInstallDocumentAddsTheKeyOnceAndOnlyWhenItIsAbsent(t *testing.T) {
	plain := "mcp:\n  startupTimeoutMs: 0\n"
	if got := InstallDocument(plain); got != plain+"compaction:\n  keepRecentTokens: 8000\n" {
		t.Errorf("InstallDocument(plain) = %q", got)
	}
	if got := InstallDocument(plain + "\n"); !strings.HasSuffix(got, "compaction:\n  keepRecentTokens: 8000\n") {
		t.Errorf("InstallDocument(trailing newline) = %q, want one added block", got)
	}
	stated := "compaction:\n  keepRecentTokens: 12000\nmcp:\n  startupTimeoutMs: 0\n"
	if got := InstallDocument(stated); got != stated {
		t.Errorf("InstallDocument(explicit) = %q, want it untouched", got)
	}
	if got := InstallDocument(""); got != "" {
		t.Errorf("InstallDocument(empty) = %q, want empty", got)
	}
}

// A SEAT WITH NO POLICY MODULE HAS NO DOCUMENT. The hub renders no `runtime/omp-config.yml` for it, and the
// answer is "nothing to install" - not an empty document, which would install an empty policy.
func TestASeatWhoseRenderHasNoDocumentGetsNone(t *testing.T) {
	_, said := renderSetup(t, map[string]any{"path": "AGENTS.md", "content": "guide"})

	if document := SeatDocumentFor(context.Background(), "room1", "seat1", said); document != nil {
		t.Errorf("document = %q, want none", *document)
	}
	if said.String() != "" {
		t.Errorf("said = %q, want nothing: the absence rule is not a failure", said.String())
	}
}

// A HUB THAT DOES NOT KNOW THE SEAT DOES NOT STOP A START, and it is not silent either: a scratch room's
// seats are on no hub at all, and the room still starts - on the room's own record, with the reason said.
func TestASeatTheHubDoesNotKnowIsSaidAndStartsFromTheRecord(t *testing.T) {
	_, said := renderSetup(t)
	// The cloud answers one seat; ask for another, which its own API answers with 404.
	document := SeatDocumentFor(context.Background(), "room1", "another-seat", said)

	if document != nil {
		t.Errorf("document = %q, want none", *document)
	}
	if !strings.Contains(said.String(), "room1/another-seat") || !strings.Contains(said.String(), "404") {
		t.Errorf("said = %q, want the room, the seat and the hub's own reason", said.String())
	}
}

// A MISSING CREDENTIAL IS THE SAME CASE: said, not fatal.
func TestTheDocumentIsSaidAndSkippedWithNoCredentials(t *testing.T) {
	t.Setenv("AGENTHUB_URL", "")
	t.Setenv("AGENTHUB_TOKEN", "")
	said := &bytes.Buffer{}

	if document := SeatDocumentFor(context.Background(), "room1", "seat1", said); document != nil {
		t.Errorf("document = %q, want none", *document)
	}
	if !strings.Contains(said.String(), "AGENTHUB_URL is not set") {
		t.Errorf("said = %q, want the missing variable named", said.String())
	}
}

// ServesRuntime is the same gate the runtime applies before it writes the file, and a seat that names NO
// runtime is not served: the runtime treats that as a custom command, not as omp.
func TestOnlyAnOmpSeatIsFetchedFor(t *testing.T) {
	for _, runtime := range []string{"omp", " OMP ", "Omp"} {
		if !ServesRuntime(runtime) {
			t.Errorf("ServesRuntime(%q) = false, want true", runtime)
		}
	}
	for _, runtime := range []string{"", "claude-code", "agy", "codex", "sleep 300", "ompx"} {
		if ServesRuntime(runtime) {
			t.Errorf("ServesRuntime(%q) = true, want false", runtime)
		}
	}
}
