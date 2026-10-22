// Package clientllmproxy is `4genteam llmproxy`: the loopback listener one runtime's model requests go
// through, so every request body is scanned and its secrets replaced before the request leaves this
// machine - and a body the scan cannot vouch for is REFUSED rather than forwarded, which is
// agent-boundaries.md 4.4's shape (withhold, never send) applied to model egress.
//
// ONE LISTENER PER REDIRECTED VARIABLE, ON A FIXED PORT, AND THE TABLE'S UNIT IS A PROVIDER ROUTE. The
// probe of record (PROBE-2026-10-21-llm-egress.md) measured one CLI answering differently on two routes -
// omp's anthropic traffic reached a listener, its deepseek traffic did not - so a table keyed by CLI
// would have had to call omp both proxied and unproxied. A route whose surface is an environment
// variable is pointed here by one line in the client's state-root .env, the file the runtime's seat_env
// hands every seat; codex's surface is ~/.codex/config.toml, so that route is pointed here by a provider
// entry (codex.go). The PORT BELONGS TO THE VARIABLE, NOT THE ROUTE: ANTHROPIC_BASE_URL can hold one
// address, so claude-code and omp-anthropic share one listener on 7391 and one .env line. That is why
// the port is fixed rather than picked at random and why a port bound by a stranger is a refusal: .env
// names this port, so a listener that moved would make .env a lie about where a seat's requests go. The
// one exception is this client's OWN listener - GET /__llmproxy answers {"runtime":"..."}, so a restart,
// a second room's `up`, or the SECOND route on the same variable reuses it instead of fighting over the
// port. A route the probe could not redirect (omp-deepseek, agy) has no row: it is reported unproxied,
// never guessed into the table.
//
// THE UPSTREAM is the provider origin the route would otherwise call, and each request keeps its own
// path and query, because a runtime's requests already carry the provider's path shape. The table names
// it per row: api.anthropic.com for both ANTHROPIC_BASE_URL routes, api.openai.com for codex. A caller
// who knows a different upstream passes --upstream, and the listener forwards there unchanged. There is
// no derivation from the listen address: 127.0.0.1:7391 is this listener, never a provider.
//
// THE BODY IS REDACTED WITH secretscan's KNOWN-VALUES HALF, NOT ITS WHOLE Redact, and this is deliberate:
// Redact also cuts its answer to secretscan.RedactedMaxLen (200 runes), a log-snippet contract - a
// request body forwarded at 200 runes would be a malformed request rather than a redacted one. It is the
// same reason clientsync's own Redact composes the known-values half instead of the whole. A credential
// that is not a known value but is still secret-SHAPED cannot be replaced here without a second pattern
// set, so the request is withheld as `unsure`; that is the fail-closed direction, and it is the reason
// `unsure` is the one refusal an ordinary turn can hit.
package clientllmproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

const (
	// MaxScanBytes is the largest request body this listener will scan. A body larger than this is
	// refused `too_large` rather than partially read: a redactor that cannot read the whole request
	// cannot promise it read all of it, which is the same rule as clientsync's ScanLimit, scaled to a
	// whole conversation body instead of one transcript line.
	MaxScanBytes = 32 << 20

	// healthPath is the one path this listener answers itself. It is how a second start tells this
	// client's own listener from a stranger on the same port - the reuse exception in the package
	// comment cannot be checked without it.
	healthPath = "/__llmproxy"

	// withheldError is the error code a refusal carries, and the reasons are 4.4's vocabulary minus
	// `bad_shape` (the event shape, not an HTTP body). The reason is part of the wire contract: a seat
	// that gets a 400 reads which check refused it, and the local record keeps the same word.
	withheldError       = "redaction_withheld"
	reasonRedactorError = "redactor_error"
	reasonNotUTF8       = "not_utf8"
	reasonTooLarge      = "too_large"
	reasonEnvFile       = "env_file"
	reasonUnsure        = "unsure"

	// The "unsure" detector's numbers, restated from agent-boundaries.md 4.4 (and clientsync's
	// detector, which owns the same rule for transcript lines): a token of at least this many
	// characters, drawn from entropyTokenAlphabet, whose Shannon entropy is at or above
	// unsureMinEntropy bits per character is secret-SHAPED even when no pattern named it. A token that
	// is a run of short segments joined by `/`, `-` or `_` is a path or an identifier, not a secret.
	unsureMinTokenLen = 32
	unsureMinEntropy  = 4.0
	unsureMaxSegment  = 16

	// recordDirName is the directory under the state root that holds one JSON file per refusal. The
	// body is never part of the record: the file names WHAT was withheld and how much, not the bytes.
	recordDirName = "llm-withheld"
)

// markerPattern is this package's own output, which is not a secret: `[REDACTED:DEEPSEEK_API_KEY]`
// names the variable, never the value. It is removed before the residue scan, because a body such as
// `"api_key": "[REDACTED:DEEPSEEK_API_KEY]"` would otherwise match secretscan's own pair pattern and be
// withheld immediately after it was correctly redacted.
var markerPattern = regexp.MustCompile(`\[REDACTED:[A-Za-z0-9_]+\]`)

// entropyTokenAlphabet is the character set the unsure detector draws tokens from, and
// entropyTokenPattern is a run of at least unsureMinTokenLen such characters.
const entropyTokenAlphabet = `[A-Za-z0-9+/=_-]`

var entropyTokenPattern = regexp.MustCompile(fmt.Sprintf(`%s{%d,}`, entropyTokenAlphabet, unsureMinTokenLen))

// toolCallIDPattern is a tool-call id as the runtimes mint it (`call_00_<random>`, `toolu_<random>`):
// a pairing key with a random body, not a credential. The same exclusion clientsync's detector makes,
// for the same reason - without it every tool round would be withheld.
var toolCallIDPattern = regexp.MustCompile(`\b(?:call_\d{2}_|toolu_)[A-Za-z0-9]+`)

// routeStatus is what the probe of record measured for a route, in the words the report and the help
// text use. A status is a measurement, never a hope: a route whose surface produced no listener hit is
// absent from the table and named unproxied.
type routeStatus string

const (
	// statusProxied is a route whose base-URL variable was measured reaching a listener.
	statusProxied routeStatus = "proxied"
	// statusProxiedByFile is codex: its config file's provider entry redirects it, but that entry is
	// inert until a profile selects it - so it is not a pass until a seat runs under the profile.
	statusProxiedByFile routeStatus = "proxied by file, inert until selected"
)

// routeRow is one provider ROUTE this listener can front: the surface its seats are pointed at the
// listener with (an environment variable or, for codex, its config file), the fixed port that surface
// names, and the provider origin the route would otherwise call. EnvVar is empty when the surface is a
// file; the port is the variable's, so two rows may share one.
type routeRow struct {
	Name     string
	EnvVar   string
	Port     int
	Upstream string
	Status   routeStatus
}

// routeTable carries ONLY the routes the probe measured, which is why omp-deepseek and agy are absent:
// `OPENAI_BASE_URL` was measured reaching no loopback listener for omp's deepseek route (0 hits on the
// room's real model), and agy's binary names no base-URL variable at all. See the probe of record,
// PROBE-2026-10-21-llm-egress.md. A route with no row is reported `unproxied`; guessing a row for it
// would be the silent fallback the decision calls worse than no proxy.
var routeTable = []routeRow{
	// claude-code is the repo's precedent redirect: the claude-deepseek wrapper and the caveman proxy
	// trial both set this variable. Its live listener hit is slice 1's remaining cell, and the row's
	// status is still proxied because the evidence is a recorded precedent rather than a guess.
	{Name: "claude-code", EnvVar: "ANTHROPIC_BASE_URL", Port: 7391, Upstream: "https://api.anthropic.com", Status: statusProxied},
	// omp-anthropic shares ANTHROPIC_BASE_URL with claude-code, and therefore its PORT: one variable can
	// hold one address, so the second route is the same listener and the same .env line. Only omp's
	// anthropic traffic is verified - its deepseek route measured no knob, which is why `omp` is not a
	// key and `omp-deepseek` is unproxied instead.
	{Name: "omp-anthropic", EnvVar: "ANTHROPIC_BASE_URL", Port: 7391, Upstream: "https://api.anthropic.com", Status: statusProxied},
	// codex's surface is a FILE, not an environment variable: OPENAI_BASE_URL measured 0 hits while
	// model_providers.<id>.base_url measured 2. The entry this client writes is inert until the profile
	// it also writes is selected, so the route's status says exactly that (codex.go owns the writer).
	{Name: "codex", Port: 7392, Upstream: "https://api.openai.com", Status: statusProxiedByFile},
}

// unproxiedRouteNames are the routes the probe found no usable surface for. They have no row, no port
// and no listener; they exist so the help, the refusal and the report can NAME them instead of letting
// a caller believe a script covered them.
var unproxiedRouteNames = []string{"omp-deepseek", "agy"}

// lookupRoute finds a route row by its key. An unknown name is a caller error rather than a default,
// because a listener for a route this table does not carry would answer requests nothing points at it.
func lookupRoute(name string) (routeRow, bool) {
	for _, row := range routeTable {
		if row.Name == name {
			return row, true
		}
	}
	return routeRow{}, false
}

// routeNames is the routes as one line, for a usage error that has to say what DOES exist.
func routeNames() string {
	names := make([]string, 0, len(routeTable))
	for _, row := range routeTable {
		names = append(names, row.Name)
	}
	return strings.Join(names, ", ")
}

// envVarOf is the base-URL variable a route key names, or "" when the key is not a route with an
// environment-variable surface. It is how the reuse decision compares SURFACES rather than route keys.
func envVarOf(name string) string {
	row, ok := lookupRoute(name)
	if !ok {
		return ""
	}
	return row.EnvVar
}

// sharesListener reports whether the route a health answer names is served by the same listener row is:
// an environment-variable route is the same listener exactly when it names the SAME VARIABLE (so
// claude-code and omp-anthropic share 7391), and a file-surface route is the same listener only as
// itself. A route-key match is not enough - the variable is what one .env line and one port mean.
func sharesListener(found string, row routeRow) bool {
	other, ok := lookupRoute(found)
	if !ok {
		return false
	}
	if row.EnvVar != "" || other.EnvVar != "" {
		return row.EnvVar != "" && row.EnvVar == other.EnvVar
	}
	return row.Name == other.Name
}

// proxy is the listener's HTTP handler: it scans and redacts one request body, refuses what it cannot
// vouch for, and otherwise hands the request to the reverse proxy that streams the answer back.
type proxy struct {
	row       routeRow
	upstream  *url.URL
	recordDir string
	envPath   string
	// environ is a seam, not an interface: the one input a test cannot set behind a function that has
	// already read the process environment. Production passes os.Environ.
	environ func() []string
	logw    io.Writer
	reverse *httputil.ReverseProxy
}

// newProxy builds the handler for one runtime. stateRoot is the client's state root: the redaction set
// also reads its .env (the file seats are given) and refusals are recorded under it.
func newProxy(row routeRow, upstream *url.URL, stateRoot string, environ func() []string, logw io.Writer) *proxy {
	// REWRITE, NOT DIRECTOR: SetURL also clears the outbound Host header, so the provider sees its own
	// host instead of 127.0.0.1:<port>. Passing a caller's headers through unchanged is about its
	// credentials, not about claiming to be this loopback listener.
	reverse := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
		},
		// -1 is the SSE contract: flush after every write instead of buffering, so a streamed answer is
		// delivered as the provider sends it rather than in one lump when the response ends.
		FlushInterval: -1,
	}
	p := &proxy{
		row:       row,
		upstream:  upstream,
		recordDir: filepath.Join(stateRoot, recordDirName, row.Name),
		envPath:   filepath.Join(stateRoot, ".env"),
		environ:   environ,
		logw:      logw,
	}
	reverse.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		p.logf("4genteam llmproxy: %s: the upstream %s did not answer: %v\n", row.Name, upstream.Host, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "upstream_unreachable"})
	}
	p.reverse = reverse
	return p
}

// logf writes one diagnostic line. A nil writer drops it: the handler's own answer and the local record
// are the contract, and a missing log destination must not turn a refusal into a panic.
func (p *proxy) logf(format string, args ...any) {
	if p.logw == nil {
		return
	}
	fmt.Fprintf(p.logw, format, args...)
}

// ServeHTTP is the listener's whole surface: the health answer, then one body scan, then the forward.
// The checks run in 4.4's order - the read bound, the UTF-8 test, the environment it redacts with, and
// the scan itself - and every failing branch WITHHOLDS, because the request body is the only thing on
// this path that must never leave unscrubbed.
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == healthPath {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"runtime": p.row.Name})
		return
	}

	if r.ContentLength > MaxScanBytes {
		p.refuse(w, r, reasonTooLarge, int(r.ContentLength))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxScanBytes+1))
	if err != nil {
		// A body that could not be read is a scan that never happened. It is the redactor_error
		// reason: nothing about this request is known to be safe.
		p.logf("4genteam llmproxy: %s: cannot read a request body: %v\n", p.row.Name, err)
		p.refuse(w, r, reasonRedactorError, len(body))
		return
	}
	if len(body) > MaxScanBytes {
		p.refuse(w, r, reasonTooLarge, len(body))
		return
	}
	if !utf8.Valid(body) {
		p.refuse(w, r, reasonNotUTF8, len(body))
		return
	}
	known, err := p.knownValues()
	if err != nil {
		// The .env is a redaction SOURCE, so failing to read it is failing to know what to redact.
		// It is not a reason to forward with a smaller set.
		p.logf("4genteam llmproxy: %s: %v\n", p.row.Name, err)
		p.refuse(w, r, reasonEnvFile, len(body))
		return
	}
	clean, reason := scanBody(string(body), known)
	if reason != "" {
		p.refuse(w, r, reason, len(body))
		return
	}

	scrubbed := []byte(clean)
	r.Body = io.NopCloser(bytes.NewReader(scrubbed))
	r.ContentLength = int64(len(scrubbed))
	p.reverse.ServeHTTP(w, r)
}

// knownValues is the redaction set for one request: the client's state-root .env (the file every seat
// is handed) plus this process's own environment, read per request so a rotated credential takes effect
// without restarting the listener. The process environment wins on a name collision, the same rule
// clientenv.Load applies when it hands the .env to a process.
func (p *proxy) knownValues() (map[string]string, error) {
	fileEntries, err := readEnvEntries(p.envPath)
	if err != nil {
		return nil, err
	}
	return secretscan.KnownValuesFromEnv(append(fileEntries, p.environ()...)), nil
}

// readEnvEntries reads KEY=value lines from path into the entry shape KnownValuesFromEnv takes. Blank
// lines and `#` comments are skipped, an `export ` prefix is dropped, and a quoted value is unquoted -
// the same reading clientenv.Load does, restated here because this package needs the values, not the
// process-wide effect. A missing file is not an error: a machine with no .env redacts its environment
// alone.
func readEnvEntries(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read the redaction source %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var entries []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "export "))
		if name == "" {
			continue
		}
		entries = append(entries, name+"="+unquote(strings.TrimSpace(value)))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read the redaction source %s: %w", path, err)
	}
	return entries, nil
}

// unquote strips one matching pair of surrounding quotes, as clientenv.Load does for the values it sets.
func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}

// scanBody returns the body to forward, or the reason it must be withheld. It is fail closed in the
// strongest sense available: a panic in the scan becomes `redactor_error` with no text, never a crash
// and never the raw bytes.
func scanBody(body string, known map[string]string) (clean, reason string) {
	defer func() {
		if recover() != nil {
			clean, reason = "", reasonRedactorError
		}
	}()
	clean, _ = secretscan.RedactKnownValues(body, known)
	// The markers are this package's own work, so they are not secrets to look for a second time.
	masked := markerPattern.ReplaceAllString(clean, "")
	if secretscan.Contains(masked) {
		return "", reasonUnsure
	}
	if _, found := highEntropyToken(masked); found {
		return "", reasonUnsure
	}
	return clean, ""
}

// highEntropyToken reports the first token in text of at least unsureMinTokenLen characters from
// entropyTokenAlphabet whose Shannon entropy, computed over the token's own character frequencies, is at
// or above unsureMinEntropy bits per character. It is the same rule as clientsync's 4.4 detector, and
// restated here rather than imported because that one is unexported and lives in the upload loop this
// package must not depend on.
func highEntropyToken(text string) (string, bool) {
	text = toolCallIDPattern.ReplaceAllString(text, "")
	for _, token := range entropyTokenPattern.FindAllString(text, -1) {
		if shannonEntropy(token) >= unsureMinEntropy && !isSegmentedName(token) {
			return token, true
		}
	}
	return "", false
}

// isSegmentedName reports whether token is a path or an identifier: every segment between `/`, `-` and
// `_` is short, and the separators are dense enough to say so. Such names carry the entropy of the words
// and ids in them rather than of a random secret, and without this rule a normal request path would be
// withheld.
func isSegmentedName(token string) bool {
	separators := 0
	run := 0
	for _, r := range token {
		if r == '/' || r == '-' || r == '_' {
			separators++
			run = 0
			continue
		}
		run++
		if run > unsureMaxSegment {
			return false
		}
	}
	return separators >= 2 && separators >= len(token)/unsureMaxSegment
}

// shannonEntropy is H = -sum(p * log2 p) over the characters of s, in bits per character.
func shannonEntropy(s string) float64 {
	runes := []rune(s)
	if len(runes) == 0 {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range runes {
		counts[r]++
	}
	total := float64(len(runes))
	var entropy float64
	for _, count := range counts {
		p := float64(count) / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// refusal is the wire shape of a withheld request. `log` is the local record's path, and it is empty
// only when the record itself could not be written - a case the listener still refuses, because a
// missing record must not turn into a forwarded secret.
type refusal struct {
	Error  string `json:"error"`
	Reason string `json:"reason"`
	Log    string `json:"log"`
}

// refuse answers one request without forwarding a byte of it, after writing the local record.
func (p *proxy) refuse(w http.ResponseWriter, r *http.Request, reason string, bytes int) {
	path, err := p.writeRecord(r, reason, bytes)
	if err != nil {
		p.logf("4genteam llmproxy: %s: this refusal was not recorded: %v\n", p.row.Name, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(refusal{Error: withheldError, Reason: reason, Log: path})
}

// withheldRecord is what one refusal leaves on this machine: the request it was, the reason, and the
// body WITHHELD - a marker and a byte count, never the bytes. It is 4.4's placeholder for an event,
// applied to a model request.
type withheldRecord struct {
	Time         string       `json:"time"`
	Runtime      string       `json:"runtime"`
	Path         string       `json:"path"`
	UpstreamHost string       `json:"upstream_host"`
	Reason       string       `json:"reason"`
	Body         withheldBody `json:"body"`
}

// withheldBody is the withheld placeholder: the fact and the size, no content and no hash of it.
type withheldBody struct {
	Withheld bool `json:"withheld"`
	Bytes    int  `json:"bytes"`
}

// writeRecord writes the refusal's local record and returns its path. The file is named after the
// moment in RFC3339 with `:` replaced by `-`, because a colon in a filename is unportable; two
// refusals in the same second get a numeric suffix rather than overwriting one another, which is what
// O_EXCL is for. The directory is 0700 and the file 0600: this machine may be shared, and the record
// names a request that may itself be sensitive.
func (p *proxy) writeRecord(r *http.Request, reason string, bytes int) (string, error) {
	now := time.Now().UTC()
	record := withheldRecord{
		Time:         now.Format(time.RFC3339),
		Runtime:      p.row.Name,
		Path:         r.URL.Path,
		UpstreamHost: p.upstream.Host,
		Reason:       reason,
		Body:         withheldBody{Withheld: true, Bytes: bytes},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("cannot encode the record for %s: %w", r.URL.Path, err)
	}
	if err := os.MkdirAll(p.recordDir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", p.recordDir, err)
	}
	stamp := strings.ReplaceAll(now.Format(time.RFC3339), ":", "-")
	for attempt := range 100 {
		name := stamp + ".json"
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d.json", stamp, attempt)
		}
		path := filepath.Join(p.recordDir, name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", fmt.Errorf("cannot write %s: %w", path, err)
		}
		_, writeErr := file.Write(append(raw, '\n'))
		closeErr := file.Close()
		if writeErr != nil {
			return "", fmt.Errorf("cannot write %s: %w", path, writeErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("cannot write %s: %w", path, closeErr)
		}
		return path, nil
	}
	return "", fmt.Errorf("cannot choose a record name under %s", p.recordDir)
}
