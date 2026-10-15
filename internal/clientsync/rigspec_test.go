package clientsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// rigspecServer answers the rigspec path with whatever the case under test needs, and records the
// request so the headers and path can be asserted rather than assumed.
func rigspecServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Request) {
	t.Helper()
	var seen http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

// TestFetchRigspecPinsTheFourTransportRefusals and the two shape refusals below exist because a port
// that drops a refusal answers "nothing is behind" for a cloud it could not read - the failure mode
// this tree keeps finding in the other direction. Each case asserts the MESSAGE and the CODE, since
// the Python's caller maps codes and a caller reading only prose would keep going on a failure.
func TestFetchRigspecPinsTheFourTransportRefusals(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantCode   int
		wantInText string
	}{
		{"HTTP error carries the status and the body's first 300 characters", 500, strings.Repeat("x", 400), clientcmd.ExitRemote,
			"GET /api/v2/rooms/dev/rigspec failed: HTTP 500 " + strings.Repeat("x", 300)},
		{"invalid JSON", 200, "{not json", clientcmd.ExitRemote, "returned invalid JSON"},
		{"a JSON list is a malformed response, not invalid JSON", 200, `[1,2]`, clientcmd.ExitRemote, "returned a malformed response"},
		{"success false is an error response", 200, `{"success":false}`, clientcmd.ExitRemote, "returned an error response"},
		{"a missing success key is not True either", 200, `{"rigspec":{}}`, clientcmd.ExitRemote, "returned an error response"},
		{"no rigspec object", 200, `{"success":true}`, clientcmd.ExitRemote, "response has no rigspec"},
		{"rigspec is a list", 200, `{"success":true,"rigspec":[]}`, clientcmd.ExitRemote, "response has no rigspec"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server, _ := rigspecServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			_, err := FetchRigspec(context.Background(), server.URL, "tok", "dev")
			if err == nil {
				t.Fatalf("FetchRigspec accepted %s, want a refusal", c.body)
			}
			if !strings.Contains(err.Error(), c.wantInText) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantInText)
			}
			if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != c.wantCode {
				t.Errorf("code = %d, want %d", code, c.wantCode)
			}
		})
	}
}

// TestFetchRigspecSendsTheTokenAndThePath pins the request half: the bearer token, the Accept header and
// the path are the contract with the server, and a port that quietly sent no token would look identical
// against a permissive dev stack and fail against a real one.
func TestFetchRigspecSendsTheTokenAndThePath(t *testing.T) {
	server, seen := rigspecServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"rigspec":{"seats":[]}}`))
	})
	if _, err := FetchRigspec(context.Background(), server.URL+"/", "s3cret", "dev"); err != nil {
		t.Fatalf("FetchRigspec: %v", err)
	}
	if seen.URL.Path != "/api/v2/rooms/dev/rigspec" {
		t.Errorf("path = %q, want the rigspec path (a trailing slash on the base URL must not double)", seen.URL.Path)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want the bearer token", got)
	}
	if got := seen.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
}

// TestCloudHashesKeepsTheCloudOrderAndRefusesAMalformedEntry covers the two things the Python's dict
// comprehension gives and Go does not, plus its failure path: the cloud's ORDER travels beside the map,
// and an entry without seat/hash strings is reported at ExitFailed's number (3, this client's
// ExitUnavailable) rather than skipped - skipping would answer "all in sync" for a rigspec nobody could
// have meant.
func TestCloudHashesKeepsTheCloudOrderAndRefusesAMalformedEntry(t *testing.T) {
	t.Run("order and hashes", func(t *testing.T) {
		server, _ := rigspecServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"success":true,"rigspec":{"seats":[` +
				`{"seat":"writer","hash":"h3"},{"seat":"lead","hash":"h2"},{"seat":"go-dev","hash":"h1"}]}}`))
		})
		order, hashes, err := CloudHashes(context.Background(), server.URL, "tok", "dev")
		if err != nil {
			t.Fatalf("CloudHashes: %v", err)
		}
		if strings.Join(order, ",") != "writer,lead,go-dev" {
			t.Errorf("order = %v, want the cloud's own order", order)
		}
		if hashes["lead"] != "h2" || hashes["go-dev"] != "h1" {
			t.Errorf("hashes = %v", hashes)
		}
	})

	for _, c := range []struct{ name, seats string }{
		{"an entry that is not an object", `["lead"]`},
		{"an entry with no hash", `[{"seat":"lead"}]`},
		{"an entry with a number for hash", `[{"seat":"lead","hash":1}]`},
	} {
		t.Run("malformed: "+c.name, func(t *testing.T) {
			server, _ := rigspecServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"success":true,"rigspec":{"seats":` + c.seats + `}}`))
			})
			_, _, err := CloudHashes(context.Background(), server.URL, "tok", "dev")
			if err == nil {
				t.Fatalf("CloudHashes accepted %s", c.seats)
			}
			if code := clientcmd.CodeOf(err, clientcmd.ExitOK); code != clientcmd.ExitUnavailable {
				t.Errorf("code = %d, want %d (the Python's generic EXIT_FAILED)", code, clientcmd.ExitUnavailable)
			}
		})
	}
}
