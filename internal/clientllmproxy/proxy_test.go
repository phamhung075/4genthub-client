package clientllmproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingUpstream is the fake provider every test counts requests on: a redaction the upstream never
// sees is not a redaction, so the hit count and the bytes that reached it are the assertion surface.
type recordingUpstream struct {
	mu       sync.Mutex
	hits     int
	method   string
	host     string
	path     string
	rawQuery string
	header   http.Header
	body     []byte
}

func (u *recordingUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	u.method, u.host, u.path, u.rawQuery, u.header, u.body = r.Method, r.Host, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ok":true}`)
}

func (u *recordingUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

func (u *recordingUpstream) request() (method, host, path, rawQuery string, header http.Header, body []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.method, u.host, u.path, u.rawQuery, u.header.Clone(), u.body
}

// fixture is one listener in front of one fake provider, with its own state root.
type fixture struct {
	proxy     *proxy
	front     *httptest.Server
	upstream  *recordingUpstream
	stateRoot string
	// upstreamServerURL is the fake provider's own address, so a test can assert the listener did not
	// forward its loopback Host header to the provider.
	upstreamServerURL string
}

func emptyEnviron() []string { return nil }

func newFixture(t *testing.T, row routeRow, environ func() []string) *fixture {
	t.Helper()
	upstream := &recordingUpstream{}
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse the fake upstream's URL %q: %v", server.URL, err)
	}
	root := t.TempDir()
	p := newProxy(row, target, root, environ, io.Discard)
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)
	return &fixture{proxy: p, front: front, upstream: upstream, stateRoot: root, upstreamServerURL: server.URL}
}

// request drives one request through the listener and returns the answer's status, headers and body.
func request(t *testing.T, method, target string, body []byte, header http.Header) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build the request for %s: %v", target, err)
	}
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer to %s %s: %v", method, target, err)
	}
	return response.StatusCode, response.Header, raw
}

func refusalOf(t *testing.T, raw []byte) refusal {
	t.Helper()
	var got refusal
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the refusal is not JSON: %v (%q)", err, raw)
	}
	return got
}

// TestAKnownSecretValueArrivesRedacted is the slice's first acceptance: a body carrying the value of a
// secret-named environment variable reaches the upstream with the marker in its place, and the rest of
// the body - including everything after the secret, which a truncating redactor would drop - is intact.
func TestAKnownSecretValueArrivesRedacted(t *testing.T) {
	const secret = "sk-deepseek-0123456789abcdef"
	fx := newFixture(t, routeTable[0], func() []string { return []string{"DEEPSEEK_API_KEY=" + secret} })
	body := []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"the key is ` + secret +
		` and this sentence must survive"}]}`)
	status, _, raw := request(t, http.MethodPost, fx.front.URL+"/v1/messages?beta=1", body,
		http.Header{"X-Api-Key": []string{"caller-owned"}})
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", status, raw)
	}
	if hits := fx.upstream.count(); hits != 1 {
		t.Fatalf("upstream hits = %d, want 1", hits)
	}
	method, host, path, query, header, forwarded := fx.upstream.request()
	if host != strings.TrimPrefix(fx.upstreamServerURL, "http://") {
		t.Errorf("forwarded Host = %q, want the provider's own address %q", host, fx.upstreamServerURL)
	}
	if method != http.MethodPost || path != "/v1/messages" || query != "beta=1" {
		t.Errorf("forwarded %s %s?%s, want POST /v1/messages?beta=1", method, path, query)
	}
	if got := header.Get("X-Api-Key"); got != "caller-owned" {
		t.Errorf("forwarded X-Api-Key = %q, want the caller's own header unchanged", got)
	}
	if !strings.Contains(string(forwarded), "[REDACTED:DEEPSEEK_API_KEY]") {
		t.Errorf("the forwarded body does not carry the marker: %q", forwarded)
	}
	if strings.Contains(string(forwarded), secret) {
		t.Errorf("the forwarded body still carries the secret value: %q", forwarded)
	}
	if !strings.HasSuffix(string(forwarded), `and this sentence must survive"}]}`) {
		t.Errorf("the forwarded body lost its tail, so it was truncated rather than redacted: %q", forwarded)
	}
}

// TestAbodyThatIsNotScannableIsRefused is the fail-closed table: every scan failure answers 400 with its
// own reason and reaches the upstream ZERO times. The cases are the reasons the decision names, driven
// through the same path a real request takes.
func TestAbodyThatIsNotScannableIsRefused(t *testing.T) {
	const entropyToken = "Zx9Qm2Lp7Rt4Vw8Yb3Nc6Kd1Hf5Jg0Qs"
	cases := []struct {
		name   string
		reason string
		body   []byte
		env    func() []string
	}{
		{
			name:   "a bare high-entropy token with no known value",
			reason: reasonUnsure,
			body:   []byte(`{"prompt":"` + entropyToken + `"}`),
		},
		{
			name:   "a secret-shaped value with no known value",
			reason: reasonUnsure,
			body:   []byte(`{"note":"sk-0123456789abcdefghijklmn"}`),
		},
		{
			name:   "a body larger than the scan bound",
			reason: reasonTooLarge,
			body:   bytes.Repeat([]byte("a"), 33<<20),
		},
		{
			name:   "a body that is not UTF-8",
			reason: reasonNotUTF8,
			body:   []byte(`{"prompt":"` + string([]byte{0xff, 0xfe, 0xfd}) + `"}`),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := c.env
			if env == nil {
				env = emptyEnviron
			}
			fx := newFixture(t, routeTable[0], env)
			status, _, raw := request(t, http.MethodPost, fx.front.URL+"/v1/messages", c.body, nil)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d (%s), want 400", status, raw)
			}
			got := refusalOf(t, raw)
			if got.Error != withheldError || got.Reason != c.reason {
				t.Errorf("refusal = %+v, want error %q reason %q", got, withheldError, c.reason)
			}
			if got.Log == "" {
				t.Error("the refusal names no local record")
			}
			if hits := fx.upstream.count(); hits != 0 {
				t.Errorf("upstream hits = %d, want 0: a withheld request forwards nothing", hits)
			}
		})
	}
}

// TestTheRedactionSourceMustBeReadable pins the env_file reason: the state root's .env is where the
// values to redact come from, so a .env the listener cannot read is a refusal, not a smaller set.
func TestTheRedactionSourceMustBeReadable(t *testing.T) {
	fx := newFixture(t, routeTable[0], emptyEnviron)
	if err := os.Mkdir(filepath.Join(fx.stateRoot, ".env"), 0o700); err != nil {
		t.Fatalf("make the .env unreadable: %v", err)
	}
	status, _, raw := request(t, http.MethodPost, fx.front.URL+"/v1/messages", []byte(`{"prompt":"hello"}`), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", status, raw)
	}
	if got := refusalOf(t, raw); got.Reason != reasonEnvFile {
		t.Errorf("reason = %q, want %q", got.Reason, reasonEnvFile)
	}
	if hits := fx.upstream.count(); hits != 0 {
		t.Errorf("upstream hits = %d, want 0", hits)
	}
}

// TestTheStateRootEnvIsARedactionSource proves the second half of the known-values set: a value that
// lives only in <state-root>/.env - the file seats are handed - is redacted just as an environment one.
func TestTheStateRootEnvIsARedactionSource(t *testing.T) {
	const secret = "env-file-only-0123456789"
	fx := newFixture(t, routeTable[0], emptyEnviron)
	envFile := filepath.Join(fx.stateRoot, ".env")
	contents := "# the client's own env file\nOTHER=unchanged\nSEAT_API_KEY=" + secret + "\n"
	if err := os.WriteFile(envFile, []byte(contents), 0o600); err != nil {
		t.Fatalf("write the state root .env: %v", err)
	}
	body := []byte(`{"prompt":"echo ` + secret + ` please"}`)
	status, _, raw := request(t, http.MethodPost, fx.front.URL+"/v1/messages", body, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", status, raw)
	}
	_, _, _, _, _, forwarded := fx.upstream.request()
	if strings.Contains(string(forwarded), secret) {
		t.Errorf("the value read from .env reached the upstream: %q", forwarded)
	}
	if !strings.Contains(string(forwarded), "[REDACTED:SEAT_API_KEY]") {
		t.Errorf("the .env value was not replaced by its own marker: %q", forwarded)
	}
}

// TestARefusalLeavesAWithheldRecord is 4.4's placeholder, on disk: one 0600 JSON file under
// <state-root>/llm-withheld/<runtime>/, naming the reason and the byte count, and never the body or any
// secret that was in it.
func TestARefusalLeavesAWithheldRecord(t *testing.T) {
	const secret = "sk-known-0123456789abcdef"
	const entropyToken = "Zx9Qm2Lp7Rt4Vw8Yb3Nc6Kd1Hf5Jg0Qs"
	fx := newFixture(t, routeTable[0], func() []string { return []string{"DEEPSEEK_API_KEY=" + secret} })
	body := []byte(`{"prompt":"` + entropyToken + `","note":"the key is ` + secret + `"}`)
	status, _, raw := request(t, http.MethodPost, fx.front.URL+"/v1/messages", body, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", status, raw)
	}
	got := refusalOf(t, raw)

	dir := filepath.Join(fx.stateRoot, recordDirName, routeTable[0].Name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the record directory %s: %v", dir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("record files = %d, want exactly 1", len(entries))
	}
	path := filepath.Join(dir, entries[0].Name())
	if got.Log != path {
		t.Errorf("refusal log = %q, want the record it wrote %q", got.Log, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("record mode = %o, want 600", mode)
	}
	rawRecord, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(rawRecord), secret) || strings.Contains(string(rawRecord), entropyToken) {
		t.Errorf("the record carries the request's secret material: %s", rawRecord)
	}
	var record withheldRecord
	if err := json.Unmarshal(rawRecord, &record); err != nil {
		t.Fatalf("the record is not JSON: %v (%s)", err, rawRecord)
	}
	if record.Runtime != routeTable[0].Name || record.Path != "/v1/messages" ||
		record.UpstreamHost != fx.proxy.upstream.Host || record.Reason != reasonUnsure {
		t.Errorf("record = %+v, want the runtime, path, upstream host and reason", record)
	}
	if !record.Body.Withheld || record.Body.Bytes != len(body) {
		t.Errorf("record body = %+v, want withheld with %d bytes", record.Body, len(body))
	}
	if !strings.Contains(entries[0].Name(), ".json") || strings.Contains(entries[0].Name(), ":") {
		t.Errorf("record name = %q, want <RFC3339 with ':' replaced by '-'>.json", entries[0].Name())
	}
}

// TestAnSSEStreamIsDeliveredIncrementally pins the FlushInterval: -1 contract. The upstream writes one
// chunk, flushes it and then BLOCKS until the client has seen that chunk, so the test cannot pass by
// buffering both chunks and delivering them together.
func TestAnSSEStreamIsDeliveredIncrementally(t *testing.T) {
	var releaseOnce sync.Once
	release := make(chan struct{})
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseUpstream)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("the fake upstream cannot flush, so the incrementality check is meaningless")
			return
		}
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse the fake upstream's URL: %v", err)
	}
	front := httptest.NewServer(newProxy(routeTable[0], target, t.TempDir(), emptyEnviron, io.Discard))
	t.Cleanup(front.Close)

	response, err := http.Get(front.URL + "/v1/messages")
	if err != nil {
		t.Fatalf("GET the stream through the listener: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream intact", got)
	}

	reader := bufio.NewReader(response.Body)
	firstLine := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		firstLine <- line
	}()
	select {
	case line := <-firstLine:
		if line != "data: first\n" {
			t.Fatalf("first line = %q, want the first chunk", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first chunk did not arrive while the upstream held the second: the stream was buffered")
	}
	// Only now may the upstream write the second chunk, and it can only do that because the first one
	// was already on the wire.
	releaseUpstream()
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read the rest of the stream: %v", err)
	}
	if !strings.Contains(string(rest), "data: second") {
		t.Errorf("the second chunk never arrived: %q", rest)
	}
}

// TestTheHealthAnswerNamesTheRuntime is what a second start's reuse decision rests on, so it is asserted
// on its own rather than only through the verb.
func TestTheHealthAnswerNamesTheRuntime(t *testing.T) {
	fx := newFixture(t, routeTable[0], emptyEnviron)
	status, _, raw := request(t, http.MethodGet, fx.front.URL+healthPath, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var answer struct {
		Runtime string `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("the health answer is not JSON: %v (%q)", err, raw)
	}
	if answer.Runtime != routeTable[0].Name {
		t.Errorf("runtime = %q, want %q", answer.Runtime, routeTable[0].Name)
	}
	if hits := fx.upstream.count(); hits != 0 {
		t.Errorf("the health probe reached the upstream %d time(s), want 0", hits)
	}
}

// TestTheTableCarriesOnlyProbedRoutes pins the table's facts that must not drift: a row exists only for a
// route the probe measured; claude-code and omp-anthropic share ONE variable and therefore ONE port (the
// port belongs to the variable, not the route); codex's surface is its config file; and the unproxied
// routes have no row at all.
func TestTheTableCarriesOnlyProbedRoutes(t *testing.T) {
	want := map[string]struct {
		envVar string
		port   int
		status routeStatus
	}{
		"claude-code":   {"ANTHROPIC_BASE_URL", 7391, statusProxied},
		"omp-anthropic": {"ANTHROPIC_BASE_URL", 7391, statusProxied},
		"codex":         {"", 7392, statusProxiedByFile},
	}
	if len(routeTable) != len(want) {
		t.Fatalf("the table carries %d rows, want %d (only the probed routes)", len(routeTable), len(want))
	}
	for _, row := range routeTable {
		expected, ok := want[row.Name]
		if !ok {
			t.Errorf("the table carries %q, which has no probed surface", row.Name)
			continue
		}
		if row.EnvVar != expected.envVar || row.Port != expected.port || row.Status != expected.status {
			t.Errorf("%s = (%q, %d, %q), want (%q, %d, %q)", row.Name, row.EnvVar, row.Port, row.Status,
				expected.envVar, expected.port, expected.status)
		}
		if _, ok := lookupRoute(row.Name); !ok {
			t.Errorf("%s is in the table but lookupRoute misses it", row.Name)
		}
	}
	if string(statusProxiedByFile) != "proxied by file, inert until selected" {
		t.Errorf("codex's status = %q, want the decision's wording", statusProxiedByFile)
	}
	for _, absent := range []string{"omp", "omp-deepseek", "agy"} {
		if _, ok := lookupRoute(absent); ok {
			t.Errorf("%q has a row, but the probe measured no surface for it", absent)
		}
	}
}

// TestTheUsageNamesTheUnproxiedRoutes keeps the refusal readable: a caller asking for a CLI rather than a
// route (or for a route with no surface) must be told the routes that exist and the ones that are
// unproxied, with codex's file status spelled out, rather than handed a default listener.
func TestTheUsageNamesTheUnproxiedRoutes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := llmproxyCommand{}.Run(t.Context(), []string{"--runtime", "omp"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 for an unknown route (stderr: %s)", code, stderr.String())
	}
	for _, want := range []string{"claude-code", "omp-anthropic", "omp-deepseek", "agy", "unproxied",
		"proxied by file, inert until selected"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to name %q", stderr.String(), want)
		}
	}
}

// TestTheListenerBindsLoopbackOnly: a routable address would accept a seat's requests from outside this
// machine, so it is a usage error rather than a bound socket.
func TestTheListenerBindsLoopbackOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := llmproxyCommand{}.Run(t.Context(), []string{"--listen", "0.0.0.0:7391"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 for a non-loopback bind", code)
	}
	if !strings.Contains(stderr.String(), "loopback") {
		t.Errorf("stderr = %q, want the loopback rule named", stderr.String())
	}
}
