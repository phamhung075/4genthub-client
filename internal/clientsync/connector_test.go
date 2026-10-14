// The parts of the connector that can be proven without a server: the codec's own invariants and the
// LOCAL REDACTION requirement, which is the one acceptance clause that runs entirely inside the client.
//
// The wire proof (a real session reaching the cloud under the right account) needs the real server's
// ingest path, which needs a database; that is stated in the commit rather than simulated here, because
// a test against a fake socket would prove the fake.
package clientsync

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The seat identity travels as a pair or not at all: a client that cannot name its seat still
// registers its session, and the server is never handed a room it would have to complete itself. The
// state and the source 2.2 adds travel with it, and a transport that knows neither leaves them out
// rather than asserting one.
func TestSessionFrameCarriesTheSeatPairOrNeither(t *testing.T) {
	frame := sessionFrame(SessionRegistration{
		Key: "dev-alice@room-a", Name: "dev-alice@room-a", Room: "room-a", Seat: "alice",
		State: SessionRunning, Source: SourceTranscript,
	})
	if frame["room"] != "room-a" || frame["seat"] != "alice" {
		t.Errorf("frame = %v, want room and seat beside the key", frame)
	}
	if frame["session_key"] != "dev-alice@room-a" || frame["name"] != "dev-alice@room-a" {
		t.Errorf("frame = %v, want the session key and name always", frame)
	}
	if frame["state"] != SessionRunning || frame["source"] != SourceTranscript {
		t.Errorf("frame = %v, want the state and the source", frame)
	}
	for _, c := range []struct{ what, room, seat string }{
		{"a room without a seat", "room-a", ""},
		{"a seat without a room", "", "alice"},
	} {
		half := sessionFrame(SessionRegistration{Key: "k", Name: "n", Room: c.room, Seat: c.seat})
		if _, ok := half["room"]; ok {
			t.Errorf("%s: frame carried a room: %v", c.what, half)
		}
		if _, ok := half["seat"]; ok {
			t.Errorf("%s: frame carried a seat: %v", c.what, half)
		}
	}
	// A caller that knows no state sends none: `seat_state` on the Sessions page distinguishes a
	// reported stop from no report at all, so an empty field is not a default to fill in.
	quiet := sessionFrame(SessionRegistration{Key: "k", Name: "n"})
	for _, field := range []string{"state", "source"} {
		if _, ok := quiet[field]; ok {
			t.Errorf("a registration with no %s still carried one: %v", field, quiet)
		}
	}
}

func TestRedactRemovesSecretShapesAndKeepsTheRest(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bearer header", "curl -H 'Authorization: Bearer abcdef1234567890' https://x", "curl -H 'Authorization: Bearer [redacted]' https://x"},
		{"env assignment", "export AGENTHUB_TOKEN=supersecretvalue", "export AGENTHUB_TOKEN=[redacted]"},
		{"env with colon", "AGENTHUB_TOKEN: supersecretvalue", "AGENTHUB_TOKEN: [redacted]"},
		{"github token", "ghp_abcdefghijklmnopqrst", "[redacted]"},
		{"openai style", "sk-abcdefghijklmnopqrstuvwx", "[redacted]"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk", "[redacted]"},
		{"ordinary line is untouched", "route GET /api/v2/sessions -> 200", "route GET /api/v2/sessions -> 200"},
		{"a word that merely looks long", "the function name is buildEverythingFromScratch", "the function name is buildEverythingFromScratch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The requirement is "redact LOCALLY, BEFORE UPLOAD", so the assertion is about the EVENT that would
// travel, not about the helper: a transcript line whose secret survived into the frame would be the
// defect, and only this shape of test catches it.
func TestTranscriptEventsRedactBeforeTheFrameIsBuilt(t *testing.T) {
	transcript := "line one\nAuthorization: Bearer abcdef1234567890\nline three\n"
	events := transcriptEvents(transcript, 50)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	encoded := ""
	for _, event := range events {
		encoded += strings.Join([]string{
			asString(event["type"]),
			asString(event["payload"].(map[string]any)["text"]),
		}, " ")
	}
	if strings.Contains(encoded, "abcdef1234567890") {
		t.Fatalf("the token survived into the frame that would be uploaded: %q", encoded)
	}
	if !strings.Contains(encoded, "[redacted]") {
		t.Fatalf("the redaction marker is absent from the frame: %q", encoded)
	}
	// The unremarkable lines must survive: a redactor that eats real content is a defect too.
	if !strings.Contains(encoded, "line one") || !strings.Contains(encoded, "line three") {
		t.Fatalf("ordinary transcript lines were lost: %q", encoded)
	}
}

func TestTranscriptEventsKeepsTheNewestLines(t *testing.T) {
	events := transcriptEvents("a\nb\nc\nd\n", 2)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	first := events[0]["payload"].(map[string]any)["text"]
	if first != "c" {
		t.Fatalf("the tail did not keep the newest lines: first is %q, want c", first)
	}
}

func TestTranscriptEventsOfAnEmptyTranscriptIsEmpty(t *testing.T) {
	if events := transcriptEvents("", 50); len(events) != 0 {
		t.Fatalf("an empty transcript produced %d events", len(events))
	}
}

// MessageEvent is the wire shape the server renders: {type: "message", payload: ...}. Pinned because
// the other direction of this assertion lives in the server's own test suite (ws_connector_test.go:149).
func TestMessageEventCarriesTheServersShape(t *testing.T) {
	event := MessageEvent(map[string]any{"text": "hello"})
	if event["type"] != "message" {
		t.Fatalf("event type = %v, want message", event["type"])
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok || payload["text"] != "hello" {
		t.Fatalf("payload = %v, want the payload map passed in", event["payload"])
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// THE TRANSPORT PROOF THE GATE COULD NOT MAKE. DialConnector wrote its upgrade request in PLAINTEXT to
// port 443, so the TLS terminator answered `400 The plain HTTP request was sent to HTTPS port` and no
// rig ever registered a session - while the slice that built it had passed its gate, because every case
// in this file replaced the dialConnector seam and nothing ever dialled. These two cases dial.
//
// The TLS case is the acceptance. Its listener demands TLS, so a plaintext client cannot reach the
// handler at all, and the Sec-WebSocket-Accept the client validates is computed from the key it sent
// inside the tunnel: a handshake this test accepts is a request that arrived over TLS. The plain case is
// the control - a ws:// base URL keeps the transport it always had.
func TestDialConnectorSpeaksTLSOverARealListener(t *testing.T) {
	listener := newUpgradeListener(t, true)
	pool := x509.NewCertPool()
	pool.AddCert(listener.Certificate())
	original := connectorTLS
	t.Cleanup(func() { connectorTLS = original })
	connectorTLS = func(host string) *tls.Config { return &tls.Config{ServerName: host, RootCAs: pool} }

	conn, err := DialConnector(context.Background(), listener.URL, "test-token")
	if err != nil {
		t.Fatalf("DialConnector to a real TLS listener: %v", err)
	}
	defer conn.conn.Close()

	request, tlsSeen := listener.arrived()
	if request == nil {
		t.Fatal("the upgrade handler was never reached")
	}
	if !tlsSeen {
		t.Fatal("the upgrade request arrived OUTSIDE TLS: the listener saw a plain socket")
	}
	if !strings.EqualFold(request.Header.Get("Upgrade"), "websocket") || request.Header.Get("Sec-WebSocket-Key") == "" {
		t.Fatalf("the handler was reached without a websocket upgrade: %v", request.Header)
	}
}

func TestDialConnectorLeavesPlaintextAlone(t *testing.T) {
	listener := newUpgradeListener(t, false)

	conn, err := DialConnector(context.Background(), "http://"+listener.Listener.Addr().String(), "test-token")
	if err != nil {
		t.Fatalf("DialConnector to a plain listener: %v", err)
	}
	defer conn.conn.Close()

	request, tlsSeen := listener.arrived()
	if request == nil || tlsSeen {
		t.Fatalf("a ws:// URL must stay plaintext: reached=%v tlsSeen=%v", request != nil, tlsSeen)
	}
}

// upgradeListener answers one websocket upgrade by hand over whatever transport it was built on, and
// records what reached the handler. Nothing here is a fake socket: the TLS case really is a
// tls.Listener, which is the only shape a plaintext dial can fail.
type upgradeListener struct {
	*httptest.Server

	mu      sync.Mutex
	request *http.Request
	tlsSeen bool
}

func newUpgradeListener(t *testing.T, withTLS bool) *upgradeListener {
	t.Helper()
	listener := &upgradeListener{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		listener.mu.Lock()
		listener.request, listener.tlsSeen = r, r.TLS != nil
		listener.mu.Unlock()

		key := r.Header.Get("Sec-WebSocket-Key")
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
			http.Error(w, "Expected WebSocket upgrade", http.StatusBadRequest)
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		// The 101 is the server's half of the handshake the client checks: the same hash of the key the
		// client sent, with the same GUID, so the client only accepts a response to a request it sent.
		sum := sha1.Sum([]byte(key + wsGUID))
		accept := base64.StdEncoding.EncodeToString(sum[:])
		fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
		if err := buf.Flush(); err != nil {
			t.Errorf("flush the 101: %v", err)
		}
		// Returning without closing the hijacked socket is what keeps it open for the client, which is
		// what a real server does until the session ends.
		_ = conn
	}
	if withTLS {
		listener.Server = httptest.NewTLSServer(http.HandlerFunc(handler))
	} else {
		listener.Server = httptest.NewServer(http.HandlerFunc(handler))
	}
	t.Cleanup(listener.Close)
	return listener
}

func (l *upgradeListener) arrived() (*http.Request, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.request, l.tlsSeen
}
