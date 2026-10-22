package clientllmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// mustURL parses a test fixture's URL, failing the test rather than returning a half-built proxy.
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}

// startOwnListener serves THIS package's handler on a fresh loopback port and returns the port. It is
// the "our own listener" side of the reuse decision, built from the same handler the verb serves.
func startOwnListener(t *testing.T, row routeRow) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a loopback port: %v", err)
	}
	server := &http.Server{Handler: newProxy(row, mustURL(t, "http://127.0.0.1:1"), t.TempDir(), emptyEnviron, io.Discard)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	waitForHealth(t, port, row.Name)
	return port
}

func waitForHealth(t *testing.T, port int, runtime string) {
	t.Helper()
	target := fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(target)
		if err == nil {
			var answer struct {
				Runtime string `json:"runtime"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&answer)
			_ = response.Body.Close()
			if decodeErr == nil && answer.Runtime == runtime {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the listener on port %d never answered %s for %q", port, healthPath, runtime)
}

// TestASecondStartReusesOurOwnListener is the one exception to the taken-port refusal: a restart, or a
// second room's start, must reuse the listener already serving that route, print its address in the
// --json shape, and exit 0 rather than fail the room.
func TestASecondStartReusesOurOwnListener(t *testing.T) {
	row := routeTable[0]
	port := startOwnListener(t, row)

	var stdout, stderr bytes.Buffer
	code := llmproxyCommand{}.Run(t.Context(),
		[]string{"--runtime", row.Name, "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--json"},
		&stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	want := fmt.Sprintf(`{"listen":"http://127.0.0.1:%d","runtime":%q}`, port, row.Name)
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Errorf("report = %s, want %s", got, want)
	}
}

// TestTheSharedPortIsReusedAcrossTwoRouteKeys is amendment A's consequence: the port belongs to the
// VARIABLE, so the reuse decision keys on the variable a row names, not on the route string. claude-code's
// listener on ANTHROPIC_BASE_URL is omp-anthropic's listener too - the second route reuses it, exit 0,
// rather than refusing because the health answer names a different route key.
func TestTheSharedPortIsReusedAcrossTwoRouteKeys(t *testing.T) {
	first, ok := lookupRoute("claude-code")
	if !ok {
		t.Fatal("claude-code is not in the table")
	}
	second, ok := lookupRoute("omp-anthropic")
	if !ok {
		t.Fatal("omp-anthropic is not in the table")
	}
	if first.EnvVar == "" || first.EnvVar != second.EnvVar || first.Port != second.Port {
		t.Fatalf("the two routes are (%q, %d) and (%q, %d), want one shared variable and port",
			first.EnvVar, first.Port, second.EnvVar, second.Port)
	}
	port := startOwnListener(t, first)

	var stdout, stderr bytes.Buffer
	code := llmproxyCommand{}.Run(t.Context(),
		[]string{"--runtime", second.Name, "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--json"},
		&stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0: the second route on the shared variable reuses the listener "+
			"(stderr: %s)", code, stderr.String())
	}
	want := fmt.Sprintf(`{"listen":"http://127.0.0.1:%d","runtime":%q}`, port, second.Name)
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Errorf("report = %s, want %s", got, want)
	}
}

// TestAPortHeldByAStrangerRefusesToStart is the other half of that rule: a port bound by anything that is
// not this client's listener for this route's surface is a refusal that names the port, never a re-pick.
func TestAPortHeldByAStrangerRefusesToStart(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		want   string
	}{
		{name: "a plain listener with no health answer", answer: "not a proxy", want: healthPath},
		{name: "a listener on another route's surface", answer: `{"runtime":"codex"}`, want: "codex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, c.answer)
			}))
			t.Cleanup(stranger.Close)
			held := mustURL(t, stranger.URL)
			host, port, err := net.SplitHostPort(held.Host)
			if err != nil {
				t.Fatalf("split the stranger's address %q: %v", held.Host, err)
			}

			var stdout, stderr bytes.Buffer
			code := llmproxyCommand{}.Run(t.Context(),
				[]string{"--runtime", routeTable[0].Name, "--listen", net.JoinHostPort(host, port)}, &stdout, &stderr)
			if code == clientcmd.ExitOK {
				t.Fatalf("exit = 0, want non-zero for a port held by a stranger (stdout: %s)", stdout.String())
			}
			if !strings.Contains(stderr.String(), port) {
				t.Errorf("stderr = %q, want the held port named", stderr.String())
			}
			if !strings.Contains(stderr.String(), c.want) {
				t.Errorf("stderr = %q, want it to name %q", stderr.String(), c.want)
			}
		})
	}
}

// TestAStrangerOnTheSharedPortIsRefused drives the fixed port the .env names: a stranger holding 7391 is a
// refusal that names it, because the tables's shared variable has no second port to fall back to. The
// test skips when this machine already has 7391 bound - another room's listener is not a stranger, and
// the test must not fight it for the port.
func TestAStrangerOnTheSharedPortIsRefused(t *testing.T) {
	const addr = "127.0.0.1:7391"
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("port %s is not free on this machine: %v", addr, err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not a proxy")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	var stdout, stderr bytes.Buffer
	code := llmproxyCommand{}.Run(t.Context(), []string{"--runtime", "claude-code"}, &stdout, &stderr)
	if code == clientcmd.ExitOK {
		t.Fatalf("exit = 0, want non-zero for a stranger on %s (stdout: %s)", addr, stdout.String())
	}
	if !strings.Contains(stderr.String(), "7391") {
		t.Errorf("stderr = %q, want the shared port named", stderr.String())
	}
}

// TestANewListenerReportsItsAddressAndStopsWithTheContext runs the real start path on an ephemeral port:
// the --json line is exactly the decision's shape, printed only once the listener is bound, and the verb
// honours its context instead of running forever.
func TestANewListenerReportsItsAddressAndStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	code := llmproxyCommand{}.Run(ctx, []string{
		"--state-root", t.TempDir(),
		"--upstream", "http://127.0.0.1:1",
		"--listen", "127.0.0.1:0",
		"--json",
	}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	pattern := regexp.MustCompile(`^\{"listen":"http://127\.0\.0\.1:\d+","runtime":"` +
		regexp.QuoteMeta(routeTable[0].Name) + `"\}\n$`)
	if !pattern.MatchString(stdout.String()) {
		t.Errorf("stdout = %q, want exactly the documented --json shape", stdout.String())
	}
}

// codexHome writes a codex config into a temp CODEX_HOME and returns the directory. The fixture config is
// the shape the owner's real one has: a global provider selection and the provider table it names.
func codexHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	config := `# the owner's codex config
model = "gpt-5"
model_provider = "openai"

[model_providers.openai]
name = "OpenAI"
base_url = "https://api.openai.com/v1"
env_key = "OPENAI_API_KEY"
`
	if err := os.WriteFile(filepath.Join(home, codexConfigName), []byte(config), 0o600); err != nil {
		t.Fatalf("write the temp codex config: %v", err)
	}
	return home
}

// TestTheVerbWritesTheCodexSurface is slice 5's wiring: asking the verb for the codex route writes the
// owned provider entry and the profile that selects it into CODEX_HOME's config, after the listener is
// bound, and still reports the listener in the --json shape.
func TestTheVerbWritesTheCodexSurface(t *testing.T) {
	home := codexHome(t)
	t.Setenv("CODEX_HOME", home)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer
	code := llmproxyCommand{}.Run(ctx, []string{
		"--runtime", "codex",
		"--state-root", t.TempDir(),
		"--upstream", "http://127.0.0.1:1",
		"--listen", "127.0.0.1:0",
		"--json",
	}, &stdout, &stderr)
	if code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	pattern := regexp.MustCompile(`^\{"listen":"http://127\.0\.0\.1:\d+","runtime":"codex"\}\n$`)
	if !pattern.MatchString(stdout.String()) {
		t.Errorf("stdout = %q, want the codex listener's address", stdout.String())
	}
	var listened listenReport
	if err := json.Unmarshal(stdout.Bytes(), &listened); err != nil {
		t.Fatalf("the --json line is not the documented report: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, codexConfigName))
	if err != nil {
		t.Fatalf("read the written codex config: %v", err)
	}
	for _, want := range []string{
		"[model_providers." + codexProviderID + "]",
		`base_url = "` + listened.Listen + `"`,
		"[profiles." + codexProfileName + "]",
		`model_provider = "agenthub-egress"`,
		`model_provider = "openai"`,
		`name = "OpenAI"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the written config does not carry %q:\n%s", want, raw)
		}
	}
}
