package clientpolicy

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
	"github.com/phamhung075/4genthub-client/internal/clientsync"
)

// policyDoc is a hub render in the shape the seatrenderer produces: the fold's `bash`, `mcp` and `tools`
// blocks, with the deny that matters for this suite (a developer keeps `edit`; this document denies it).
const policyDoc = "bash:\n  allowCompoundCommands: true\nmcp:\n  startupTimeoutMs: 0\ntools:\n  approval:\n    edit: deny\n    ast_edit: deny\n"

// policyCloud stands up a hub that knows ONE seat and renders a runtime/omp-config.yml for it; any other
// seat is the 404 the hub's own API answers with.
func policyCloud(t *testing.T, seat string, files []map[string]any) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != clientsync.PathsPath+"/room1/"+seat {
			http.Error(w, `{"detail":"seat not found"}`, http.StatusNotFound)
			return
		}
		entries := make([]any, 0, len(files))
		for _, f := range files {
			entries = append(entries, f)
		}
		body, err := json.Marshal(map[string]any{
			"success": true,
			"resolved_seat": map[string]any{
				"room": "room1", "seat": seat, "hash": "abc",
				"files": entries, "policy": map[string]any{},
			},
		})
		if err != nil {
			t.Errorf("encode: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTHUB_URL", server.URL)
	t.Setenv("AGENTHUB_TOKEN", "tok")
}

// policyCloudWithDocument is the ordinary case: the seat's render carries the document.
func policyCloudWithDocument(t *testing.T, seat string) {
	t.Helper()
	policyCloud(t, seat, []map[string]any{
		{"path": "AGENTS.md", "content": "the seat's own guide"},
		{"path": clientsync.SeatRenderPath, "content": policyDoc},
	})
}

// policyStateRoot makes a fresh state root and, when `installed` is given, the seat's own directory with a
// config.yml in it - the file the runtime installs and `--check` reads.
func policyStateRoot(t *testing.T, installed string) string {
	t.Helper()
	root := t.TempDir()
	if installed == "" {
		return root
	}
	path := clientsync.SeatConfigPath(root, "room1", "seat1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(installed), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func runPolicy(t *testing.T, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = policyCommand{}.Run(context.Background(), argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

// WHAT `policy render` PRINTS IS WHAT WOULD BE INSTALLED: the hub's document, key for key, plus this client's
// own non-policy key - and nothing else, because there is no client-side table left to add to it.
func TestPolicyRenderPrintsTheHubDocument(t *testing.T) {
	policyCloudWithDocument(t, "seat1")

	code, stdout, stderr := runPolicy(t, "render", "--room", "room1", "--seat", "seat1")

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stderr %q)", code, clientcmd.ExitOK, stderr)
	}
	if want := clientsync.InstallDocument(policyDoc); stdout != want {
		t.Errorf("stdout = %q, want exactly the installable document %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

// A INSTALLED FILE THAT CARRIES THE RENDER IS NOT DRIFT, and `--check` says so on stdout and exits 0 - the
// file's own extra top-level keys are none of this comparison's business.
func TestPolicyCheckAcceptsAFileThatCarriesTheRender(t *testing.T) {
	policyCloudWithDocument(t, "seat1")
	root := policyStateRoot(t, clientsync.InstallDocument(policyDoc)+"otherTool:\n  key: 1\n")

	code, stdout, stderr := runPolicy(t, "render", "--room", "room1", "--seat", "seat1", "--state-root", root, "--check")

	if code != clientcmd.ExitOK {
		t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", code, clientcmd.ExitOK, stdout, stderr)
	}
	if !strings.Contains(stdout, "seat1: ok (") {
		t.Errorf("stdout = %q, want the ok line naming the file", stdout)
	}
}

// A FILE THAT NO LONGER CARRIES THE RENDER IS REPORTED, NOT KEPT. The case here is the one the change is
// about: a tool the hub denies is left allowed in the installed file, which a check by shape must catch
// (the file still parses, still has a `tools.approval`, and still names the tool).
func TestPolicyCheckReportsADriftedFile(t *testing.T) {
	policyCloudWithDocument(t, "seat1")
	drifted := strings.Replace(clientsync.InstallDocument(policyDoc), "    edit: deny\n", "    edit: allow\n", 1)
	root := policyStateRoot(t, drifted)

	code, stdout, _ := runPolicy(t, "render", "--room", "room1", "--seat", "seat1", "--state-root", root, "--check")

	if code != ExitDrift {
		t.Fatalf("exit %d, want %d (stdout %q)", code, ExitDrift, stdout)
	}
	if !strings.Contains(stdout, "seat1: DRIFT (") {
		t.Errorf("stdout = %q, want the drift line naming the file", stdout)
	}
}

// A FILE THAT IS NOT THERE IS THE LOUDEST DRIFT: the seat is running with no policy file at all, which is the
// state this change exists to end.
func TestPolicyCheckReportsAMissingFile(t *testing.T) {
	policyCloudWithDocument(t, "seat1")
	root := policyStateRoot(t, "")

	code, stdout, _ := runPolicy(t, "render", "--room", "room1", "--seat", "seat1", "--state-root", root, "--check")

	if code != ExitDrift {
		t.Fatalf("exit %d, want %d (stdout %q)", code, ExitDrift, stdout)
	}
	if !strings.Contains(stdout, "no installed file at") {
		t.Errorf("stdout = %q, want the missing file and its path", stdout)
	}
}

// A SEAT WHOSE RENDER CARRIES NO DOCUMENT HAS NOTHING TO INSTALL. That is not a drift and not a failure to
// read: it is said, and the verb exits without printing a document.
func TestPolicyRenderRefusesASeatWithNoDocument(t *testing.T) {
	policyCloud(t, "seat1", []map[string]any{{"path": "AGENTS.md", "content": "guide"}})

	code, stdout, stderr := runPolicy(t, "render", "--room", "room1", "--seat", "seat1")

	if code == clientcmd.ExitOK {
		t.Fatalf("exit %d, want a refusal (stdout %q)", code, stdout)
	}
	if !strings.Contains(stderr, clientsync.SeatRenderPath) {
		t.Errorf("stderr = %q, want the missing entry named", stderr)
	}
}

// A SEAT THE HUB DOES NOT KNOW IS A REFUSAL THAT NAMES THE HUB'S OWN ANSWER: this verb asks the hub, so an
// unknown seat is exactly the case it must not paper over with an empty document.
func TestPolicyRenderRefusesASeatTheHubDoesNotKnow(t *testing.T) {
	policyCloudWithDocument(t, "seat1")

	code, stdout, stderr := runPolicy(t, "render", "--room", "room1", "--seat", "seat2")

	if code == clientcmd.ExitOK {
		t.Fatalf("exit %d, want a refusal (stdout %q)", code, stdout)
	}
	if !strings.Contains(stderr, "HTTP 404") || !strings.Contains(stderr, "room1/seat2") {
		t.Errorf("stderr = %q, want the seat and the hub's reason", stderr)
	}
}

// THE DELETED INPUT STAYS DELETED: `--team` is gone with the role table it named, and this pins that the flag
// is refused BY NAME rather than carried and ignored.
func TestPolicyRenderRefusesTheRemovedTeamFlagAndMissingArguments(t *testing.T) {
	policyCloudWithDocument(t, "seat1")

	code, _, stderr := runPolicy(t, "render", "--room", "room1", "--seat", "seat1", "--team", "/tmp/team.json")
	if code != clientcmd.ExitUsage {
		t.Errorf("exit %d with --team, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(stderr, "unknown flag --team") {
		t.Errorf("stderr = %q, want the flag named", stderr)
	}

	code, _, stderr = runPolicy(t, "render", "--room", "room1")
	if code != clientcmd.ExitUsage || !strings.Contains(stderr, "--seat") {
		t.Errorf("exit %d, stderr %q, want a usage refusal naming --seat", code, stderr)
	}

	code, _, stderr = runPolicy(t, "apply", "--room", "room1", "--seat", "seat1")
	if code != clientcmd.ExitUsage || !strings.Contains(stderr, "unknown verb") {
		t.Errorf("exit %d, stderr %q, want the removed apply verb named", code, stderr)
	}
}

// THE COMPARISON IS EXACT INSIDE THE KEYS IT DEFINES, and blind to the keys it does not: an extra top-level
// key is another tool's business, while a changed entry INSIDE `tools.approval` is this one's.
func TestPolicyKeysMatchComparesInsideTheKeysItDefines(t *testing.T) {
	want := policyDoc
	cases := []struct {
		name  string
		have  string
		match bool
	}{
		{"identical", want, true},
		{"an extra top-level key", want + "other:\n  key: 1\n", true},
		{"a reordered document", "tools:\n  approval:\n    edit: deny\n    ast_edit: deny\nmcp:\n  startupTimeoutMs: 0\nbash:\n  allowCompoundCommands: true\n", true},
		{"an entry changed inside tools", strings.Replace(want, "edit: deny", "edit: allow", 1), false},
		{"an entry added inside tools", strings.Replace(want, "edit: deny", "edit: deny\n    write: deny", 1), false},
		{"the tools block gone", "bash:\n  allowCompoundCommands: true\nmcp:\n  startupTimeoutMs: 0\n", false},
		{"not a document", "\t\x00", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := PolicyKeysMatch(want, tc.have); got != tc.match {
			t.Errorf("%s: PolicyKeysMatch = %v, want %v", tc.name, got, tc.match)
		}
	}
}
