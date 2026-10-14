package clientteam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The tests are OFFLINE by construction: they replace the package's httpClient seam with a fake
// RoundTripper that answers from memory, so no test opens a socket. The Python tests this file mirrors
// ran a BaseHTTPRequestHandler on 127.0.0.1; a fake transport dials nothing at all, which is strictly
// less. A test that really reached a host would fail here, because the fake is the only client
// installed for the duration of the test and it never forwards.

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type recordedRequest struct {
	method string
	path   string
	body   string
}

type fakeResponse struct {
	status int
	body   string
}

// fakeCloud answers like the backend the Python tests stood up, minus the socket.
type fakeCloud struct {
	mu        sync.Mutex
	requests  []recordedRequest
	overrides map[string]fakeResponse
	stored    map[string]storedModule
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{
		overrides: map[string]fakeResponse{},
		stored:    map[string]storedModule{},
	}
}

func (f *fakeCloud) override(method, path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrides[method+" "+path] = fakeResponse{status: status, body: body}
}

func (f *fakeCloud) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeCloud) puts() []recordedRequest {
	out := []recordedRequest{}
	for _, request := range f.recorded() {
		if request.method == "PUT" {
			out = append(out, request)
		}
	}
	return out
}

// storePushes serves every recorded module PUT back as a stored GET, the way the backend would.
func (f *fakeCloud) storePushes() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if request.method != "PUT" {
			continue
		}
		parts := strings.Split(request.path, "/")
		if len(parts) < 8 {
			continue
		}
		slug, version := parts[5], parts[7]
		var body map[string]any
		if err := json.Unmarshal([]byte(request.body), &body); err != nil {
			continue
		}
		kind, _ := body["kind"].(string)
		content, _ := body["content"].(string)
		f.stored[slug+"@"+version] = storedModule{slug: slug, version: version, kind: kind, content: content}
	}
}

// storeSkillBlock installs one stored module body for drift-check to read.
func (f *fakeCloud) storeSkillBlock(slug, version, kind, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored[slug+"@"+version] = storedModule{slug: slug, version: version, kind: kind, content: content}
}

func (f *fakeCloud) respond(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := ""
	if request.Body != nil {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	f.requests = append(f.requests, recordedRequest{method: request.Method, path: request.URL.Path, body: body})
	if override, known := f.overrides[request.Method+" "+request.URL.Path]; known {
		return fakeHTTPResponse(override.status, override.body), nil
	}
	switch {
	case request.Method == "GET" && request.URL.Path == apiPrefix+"/seat-types":
		return fakeHTTPResponse(200, `{"seat_types": []}`), nil
	case request.Method == "GET" && request.URL.Path == apiPrefix+"/modules":
		return fakeHTTPResponse(200, f.moduleListing()), nil
	case request.Method == "GET":
		if module, known := f.storedModuleFor(request.URL.Path); known {
			payload := map[string]any{"success": true, "module": module}
			encoded, _ := json.Marshal(payload)
			return fakeHTTPResponse(200, string(encoded)), nil
		}
		return fakeHTTPResponse(404, `{"detail": "not found"}`), nil
	case request.Method == "POST":
		return fakeHTTPResponse(201, `{"success": true}`), nil
	default:
		return fakeHTTPResponse(200, `{"success": true}`), nil
	}
}

// moduleListing is the newest stored version of every module, without content - the shape the Python's
// _store_blocks serves on GET /modules.
func (f *fakeCloud) moduleListing() string {
	summaries := []map[string]any{}
	for _, module := range f.stored {
		summaries = append(summaries, map[string]any{
			"slug": module.slug, "kind": module.kind, "version": module.version,
		})
	}
	encoded, _ := json.Marshal(map[string]any{"success": true, "modules": summaries})
	return string(encoded)
}

func (f *fakeCloud) storedModuleFor(path string) (map[string]any, bool) {
	// /api/v2/openrig/modules/{slug}/versions/{version}
	parts := strings.Split(path, "/")
	if len(parts) != 8 || parts[4] != "modules" || parts[6] != "versions" {
		return nil, false
	}
	module, known := f.stored[parts[5]+"@"+parts[7]]
	if !known {
		return nil, false
	}
	return map[string]any{
		"slug": module.slug, "version": module.version, "kind": module.kind, "content": module.content,
	}, true
}

func fakeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// installFake puts the fake transport behind the package's seam and restores it afterwards.
func installFake(t *testing.T) *fakeCloud {
	t.Helper()
	fake := newFakeCloud()
	previous := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(fake.respond)}
	t.Cleanup(func() { httpClient = previous })
	return fake
}

// withCredentials sets the one credential pair the client reads.
func withCredentials(t *testing.T, cloud *fakeCloud) {
	t.Helper()
	t.Setenv("AGENTHUB_URL", "https://cloud.example.test")
	t.Setenv("AGENTHUB_TOKEN", "token-value")
}

// runTeam drives the verb through the dispatcher's entry point and returns the exit code with both
// streams, which is what every Python test in the spec asserts on.
func runTeam(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	code := teamCommand{}.Run(context.Background(), args, stdout, stderr)
	return code, stdout.String(), stderr.String()
}
