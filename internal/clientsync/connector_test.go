// The parts of the connector that can be proven without a server: the codec's own invariants and the
// LOCAL REDACTION requirement, which is the one acceptance clause that runs entirely inside the client.
//
// The wire proof (a real session reaching the cloud under the right account) needs the real server's
// ingest path, which needs a database; that is stated in the commit rather than simulated here, because
// a test against a fake socket would prove the fake.
package clientsync

import (
	"strings"
	"testing"
)

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
