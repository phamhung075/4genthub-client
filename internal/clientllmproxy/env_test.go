// THE REDIRECT, AS A CASE: what `up` writes into the state root's `.env`, and what a second run must NOT
// write. The file holds credentials, so the assertion is not only "the line is there" but "everything else
// is exactly as it was", and the upstream keys are checked because they are where the origin the redirect
// replaced survives - a listener that forgot it has nowhere to forward to.
package clientllmproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempStateRoot is a state root with a chosen .env body.
func tempStateRoot(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// envBody reads the .env back.
func envBody(t *testing.T, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestEnsureEnvRedirectsTheVariableOnceAndKeepsTheOrigin is the slice's first acceptance: the variable is
// pointed at the listener, the origin it held is kept under the route's upstream name, a variable with no
// environment surface gets no line, and every other line survives byte for byte.
func TestEnsureEnvRedirectsTheVariableOnceAndKeepsTheOrigin(t *testing.T) {
	root := tempStateRoot(t, "# the operator's file\nOTHER=keep-me\nANTHROPIC_BASE_URL=https://api.anthropic.com\n")

	written, err := EnsureEnv(root)
	if err != nil {
		t.Fatalf("EnsureEnv: %v", err)
	}
	want := []string{
		"AGENTHUB_LLM_UPSTREAM_CLAUDE_CODE",
		"AGENTHUB_LLM_UPSTREAM_OMP_ANTHROPIC",
		"ANTHROPIC_BASE_URL",
	}
	if strings.Join(written, ",") != strings.Join(want, ",") {
		t.Fatalf("written = %v, want %v", written, want)
	}

	body := envBody(t, root)
	for _, line := range []string{
		"# the operator's file",
		"OTHER=keep-me",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:7391",
		"AGENTHUB_LLM_UPSTREAM_CLAUDE_CODE=https://api.anthropic.com",
		"AGENTHUB_LLM_UPSTREAM_OMP_ANTHROPIC=https://api.anthropic.com",
	} {
		if !strings.Contains(body, line) {
			t.Errorf("the .env is missing %q:\n%s", line, body)
		}
	}
	if strings.Contains(body, "OPENAI_BASE_URL") {
		t.Errorf("a line was written for a surface the table does not carry:\n%s", body)
	}
	if strings.Contains(body, "CODEX") {
		t.Errorf("codex's surface is a file, not a line here, so no CODEX name belongs in .env:\n%s", body)
	}

	// The second run is a no-op: the file is byte-identical and nothing is reported written.
	before, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	written, err = EnsureEnv(root)
	if err != nil {
		t.Fatalf("the second EnsureEnv: %v", err)
	}
	if len(written) != 0 {
		t.Errorf("the second run wrote %v, want nothing", written)
	}
	after, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("the second run changed the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestEnsureEnvCreatesWhatIsAbsent: a machine with no .env at all gets the two lines the table requires, the
// upstream keys from the table rather than from a replaced value, and a file only its owner can read.
func TestEnsureEnvCreatesWhatIsAbsent(t *testing.T) {
	root := tempStateRoot(t, "")
	written, err := EnsureEnv(root)
	if err != nil {
		t.Fatalf("EnsureEnv: %v", err)
	}
	if len(written) != 3 {
		t.Fatalf("written = %v, want the two upstream keys and the variable", written)
	}
	body := envBody(t, root)
	if !strings.Contains(body, "ANTHROPIC_BASE_URL=http://127.0.0.1:7391\n") {
		t.Errorf("the redirect line is missing:\n%s", body)
	}
	for _, route := range []string{"claude-code", "omp-anthropic"} {
		upstream, ok := lookupRoute(route)
		if !ok {
			t.Fatalf("%s is not in the table", route)
		}
		if !strings.Contains(body, UpstreamKey(route)+"="+upstream.Upstream+"\n") {
			t.Errorf("the upstream key for %s does not carry the table's origin:\n%s", route, body)
		}
	}
	info, err := os.Stat(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the created .env has mode %o, want 600", mode)
	}
}

// TestEnsureEnvKeepsAFirstOriginAcrossASecondRedirect: the upstream key is written once. A file already
// redirected to the listener and then edited back to a second origin keeps the FIRST origin, which is the
// address the seats were actually leaving through before this client pointed them here.
func TestEnsureEnvKeepsAFirstOriginAcrossASecondRedirect(t *testing.T) {
	root := tempStateRoot(t, "ANTHROPIC_BASE_URL=http://127.0.0.1:7391\n"+
		"AGENTHUB_LLM_UPSTREAM_CLAUDE_CODE=https://first.example\n"+
		"AGENTHUB_LLM_UPSTREAM_OMP_ANTHROPIC=https://first.example\n")
	if _, err := EnsureEnv(root); err != nil {
		t.Fatalf("EnsureEnv: %v", err)
	}
	if body := envBody(t, root); !strings.Contains(body, "AGENTHUB_LLM_UPSTREAM_CLAUDE_CODE=https://first.example") {
		t.Errorf("the first origin was overwritten:\n%s", body)
	}
}

// TestListenersIsOnePerSurface: one listener per base-URL variable however many routes share it, plus
// codex's own - the count the report and the row are measured against.
func TestListenersIsOnePerSurface(t *testing.T) {
	listeners := Listeners()
	if len(listeners) != 2 {
		t.Fatalf("Listeners() = %v, want one ANTHROPIC listener and codex's", listeners)
	}
	if listeners[0].Surface != "ANTHROPIC_BASE_URL" || listeners[0].Port != 7391 {
		t.Errorf("the first listener is %+v, want ANTHROPIC_BASE_URL on 7391", listeners[0])
	}
	if listeners[0].Addr() != "127.0.0.1:7391" || listeners[0].BaseURL() != "http://127.0.0.1:7391" {
		t.Errorf("the first listener's address = %q/%q", listeners[0].Addr(), listeners[0].BaseURL())
	}
	if listeners[1].Surface != "" || listeners[1].Port != 7392 || listeners[1].Name() != "codex" {
		t.Errorf("codex's listener is %+v, want a file surface on 7392 named codex", listeners[1])
	}
}

// TestUpstreamKeyNamesTheRoute: the key is the route in upper snake case, so the origin of claude-code and
// the origin of omp-anthropic cannot collide.
func TestUpstreamKeyNamesTheRoute(t *testing.T) {
	for route, want := range map[string]string{
		"claude-code":   "AGENTHUB_LLM_UPSTREAM_CLAUDE_CODE",
		"omp-anthropic": "AGENTHUB_LLM_UPSTREAM_OMP_ANTHROPIC",
		"codex":         "AGENTHUB_LLM_UPSTREAM_CODEX",
	} {
		if got := UpstreamKey(route); got != want {
			t.Errorf("UpstreamKey(%q) = %q, want %q", route, got, want)
		}
	}
}
