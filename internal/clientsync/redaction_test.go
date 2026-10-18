// The redaction boundary's own proofs: the 4.3 pattern list (old and added), and the 4.4 gate's six
// reasons, each of which must WITHHOLD rather than upload. The one case no input can reach - a
// panicking redactor - is proven through the `redactLine` seam, the way secretscan proves its
// fail-closed rule through `truncate`.
package clientsync

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactCoversEveryPatternAndLeavesRealTextAlone(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The six patterns the replaced version already had.
		{"bearer header", "curl -H 'Authorization: Bearer abcdef1234567890' https://x",
			"curl -H 'Authorization: Bearer [redacted]' https://x"},
		{"named env assignment", "export AGENTHUB_TOKEN=supersecretvalue",
			"export AGENTHUB_TOKEN=[redacted]"},
		{"named env with colon", "AGENTHUB_TOKEN: supersecretvalue",
			"AGENTHUB_TOKEN: [redacted]"},
		{"github token", "ghp_abcdefghijklmnopqrst", "[redacted]"},
		{"slack token", "xoxb-1234567890-abcdefghij", "[redacted]"},
		{"openai token", "sk-abcdefghijklmnopqrstuvwx", "[redacted]"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk",
			"[redacted]"},

		// The five ADDED shapes of 4.3.2.
		{"aws access key id AKIA", "aws_key AKIAIOSFODNN7EXAMPLE here",
			"aws_key [redacted] here"},
		{"aws access key id ASIA", "ASIAIOSFODNN7EXAMPLE", "[redacted]"},
		{"pem private key block", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA7Zx\n-----END RSA PRIVATE KEY-----",
			"[redacted]"},
		{"pem private key block without END", "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAB3NzaC1yc2E",
			"[redacted]"},
		{"url userinfo keeps the scheme", "clone https://alice:s3cret@github.com/x.git",
			"clone https://[redacted]@github.com/x.git"},
		{"generic assignment TOKEN", "MY_TOKEN=abc123456", "MY_TOKEN=[redacted]"},
		{"generic assignment API_KEY", "MY_API_KEY=abc123456", "MY_API_KEY=[redacted]"},
		{"generic assignment SECRET colon", "SESSION_SECRET: hunter2xyz",
			"SESSION_SECRET: [redacted]"},
		{"generic assignment PASSWORD", "DB_PASSWORD=correcthorse", "DB_PASSWORD=[redacted]"},
		{"generic assignment PASSWD", "DB_PASSWD=correcthorse", "DB_PASSWD=[redacted]"},
		{"generic assignment PRIVATE_KEY", "PGP_PRIVATE_KEY=zzz999", "PGP_PRIVATE_KEY=[redacted]"},
		{"flag equals form", "deploy --token=abc123xyz", "deploy --token=[redacted]"},
		{"flag space form", "deploy --password hunter2", "deploy --password [redacted]"},
		{"flag api-key", "tool --api-key=abc123xyz", "tool --api-key=[redacted]"},

		// Controls: real text must survive. A redactor that eats content is a defect too.
		{"ordinary route line", "route GET /api/v2/sessions -> 200",
			"route GET /api/v2/sessions -> 200"},
		{"a word that merely looks long", "the function name is buildEverythingFromScratch",
			"the function name is buildEverythingFromScratch"},
		{"a url with no userinfo", "browse https://example.com/a/b?c=d",
			"browse https://example.com/a/b?c=d"},
		{"a host and port with no userinfo", "dial http://localhost:8080/path",
			"dial http://localhost:8080/path"},
		{"a .env file name is not the .env rule here", "the file is named /.env",
			"the file is named /.env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The scan limit is 4.4's `too_large`: a line strictly larger than ScanLimit is withheld, and one at
// the limit still passes.
func TestEventWithholdsOnlyAboveTheScanLimit(t *testing.T) {
	redactor := NewEventRedactor()

	passing := strings.Repeat("a", 63*1024)
	if event, reason := redactor.Event(passing); reason != "" || event["type"] != "message" {
		t.Fatalf("63 KiB was not uploaded: reason=%q event=%v", reason, event)
	}

	atLimit := strings.Repeat("b", ScanLimit)
	if _, reason := redactor.Event(atLimit); reason != "" {
		t.Fatalf("a line exactly at the scan limit was withheld: reason=%q", reason)
	}

	over := strings.Repeat("c", ScanLimit+1)
	event, reason := redactor.Event(over)
	assertWithheld(t, event, reason, reasonTooLarge, len(over))
}

func TestEventWithholdsInvalidUTF8(t *testing.T) {
	line := "hello \xff world"
	event, reason := NewEventRedactor().Event(line)
	assertWithheld(t, event, reason, reasonNotUTF8, len(line))
}

func TestEventWithholdsAJSONValueThatIsNotAnObject(t *testing.T) {
	for name, line := range map[string]string{
		"array":  "[1,2,3]",
		"number": "42",
		"string": `"just a scalar"`,
		"bool":   "true",
	} {
		t.Run(name, func(t *testing.T) {
			event, reason := NewEventRedactor().Event(line)
			assertWithheld(t, event, reason, reasonBadShape, len(line))
		})
	}
}

// A line that does not parse as JSON at all is still uploadable text: `rig transcript` prints text
// lines, and a text line is not the transcript object shape 4.4 withholds for.
func TestEventUploadsAPlainTextLine(t *testing.T) {
	line := "2026-10-11 12:00:00 INFO just a log line, not json"
	event, reason := NewEventRedactor().Event(line)
	if reason != "" {
		t.Fatalf("a plain text line was withheld: reason=%q", reason)
	}
	if event["type"] != "message" || event["payload"].(map[string]any)["text"] != line {
		t.Fatalf("plain text event = %v, want the line as the message text", event)
	}
}

// A JSON object that is not a .env call is uploadable, with its secrets pattern-redacted.
func TestEventUploadsAJSONObjectWithPatternsRedacted(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"text","text":"Authorization: Bearer abcdef1234567890"}]}}`
	event, reason := NewEventRedactor().Event(line)
	if reason != "" {
		t.Fatalf("an ordinary JSON object was withheld: reason=%q", reason)
	}
	text, _ := event["payload"].(map[string]any)["text"].(string)
	if strings.Contains(text, "abcdef1234567890") {
		t.Fatalf("the token survived into the event: %q", text)
	}
	if !strings.Contains(text, redactedWord) {
		t.Fatalf("the redaction marker is absent: %q", text)
	}
}

// The .env rule (4.3.3) and its pairing: the call is withheld, its result is withheld through the id
// the redactor remembered, and an unrelated result is NOT withheld.
func TestEventWithholdsEnvFileCallsAndTheirPairedResults(t *testing.T) {
	redactor := NewEventRedactor()

	call := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_env","name":"Read","input":{"file_path":"/project/.env"}}]}}`
	event, reason := redactor.Event(call)
	assertWithheld(t, event, reason, reasonEnvFile, len(call))

	// The id is remembered, so the result that carries it is withheld too, even though its own text
	// names no path at all.
	paired := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_env","content":"SAFE=1"}]}}`
	event, reason = redactor.Event(paired)
	assertWithheld(t, event, reason, reasonEnvFile, len(paired))

	// A result paired with a DIFFERENT call is not withheld: the pairing is by id, not by shape.
	unrelated := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_other","content":"ok"}]}}`
	if _, reason := redactor.Event(unrelated); reason != "" {
		t.Fatalf("an unrelated tool_result was withheld: reason=%q", reason)
	}

	// A .env dot-variant named through a command is a .env path too.
	command := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_cmd","name":"Bash","input":{"command":"cat /project/.env.local"}}]}}`
	if _, reason := redactor.Event(command); reason != reasonEnvFile {
		t.Fatalf("a .env.local read was not withheld: reason=%q", reason)
	}

	// A call whose input names no .env path is not withheld.
	plain := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_plain","name":"Read","input":{"file_path":"/project/main.go"}}]}}`
	if _, reason := redactor.Event(plain); reason != "" {
		t.Fatalf("a call naming main.go was withheld: reason=%q", reason)
	}
}

// The "unsure" detector (4.4): a 32-plus character high-entropy token that survived redaction
// withholds the line; a long LOW-entropy token does not fire it.
func TestEventWithholdsOnAHighEntropyToken(t *testing.T) {
	high := "token candidate abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN end"
	event, reason := NewEventRedactor().Event(high)
	assertWithheld(t, event, reason, reasonUnsure, len(high))

	low := "token candidate " + strings.Repeat("a", 40) + " end"
	if _, reason := NewEventRedactor().Event(low); reason != "" {
		t.Fatalf("a 40-character low-entropy token was withheld: reason=%q", reason)
	}
}

// A path or a queue-item id is a run of short segments joined by separators; it has high entropy and is
// not a secret, so it must not withhold the line. A secret with the same alphabet and the same length
// still does.
func TestEventKeepsPathsAndIdsButWithholdsSecrets(t *testing.T) {
	for _, name := range []string{
		"/home/daihu/__projects__/4genthub/agenthub_client/internal/clientsync/redaction.go",
		"see qitem-20261015050158-c8120a7112fb for the ruling",
		"agenthub_client/internal/clientseat/sendpolicy_test.go",
		`{"type":"toolCall","id":"call_00_aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5","name":"read"}`,
	} {
		if _, reason := NewEventRedactor().Event(name); reason != "" {
			t.Errorf("%q was withheld: reason=%q", name, reason)
		}
	}
	for _, secret := range []string{
		"the session token is Qw9_xK2mP7vB4nR8tY6uI3oL5aZ1sD0fH",
		"blob Qw9xK2mP7vB4nR8tY6uI3oL5aZ1sD0fHjG-eW3qC/x end",
	} {
		if _, reason := NewEventRedactor().Event(secret); reason != reasonUnsure {
			t.Errorf("%q: reason=%q, want %q", secret, reason, reasonUnsure)
		}
	}
}

// The fail-closed rule for a broken redactor: replacing the seam to panic must yield the placeholder
// and must not leak the raw line.
func TestEventWithholdsWhenTheRedactorPanics(t *testing.T) {
	original := redactLine
	redactLine = func(*EventRedactor, string) (map[string]any, string, error) {
		panic("the redactor is broken")
	}
	defer func() { redactLine = original }()

	secret := "raw-secret-value-0123456789abcdef"
	event, reason := NewEventRedactor().Event(secret)
	assertWithheld(t, event, reason, reasonRedactorError, len(secret))

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("the placeholder is not encodable: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("the raw line survived into the placeholder: %s", encoded)
	}
	if _, present := event["payload"].(map[string]any)["text"]; present {
		t.Fatalf("the placeholder carries content: %v", event)
	}
}

// TestASentinelInTheTokenEnvironmentNeverReachesTheEvent is the ruling's sentinel clause on the EVENT surface -
// rigd-boundaries.md 7.10: a sentinel value in the token environment variable, and a fake `claude` that echoes
// its environment and argv, must appear in NEITHER the log NOR the event. THE SURFACE THIS COVERS IS THE UPLOAD
// GATE: every transcript line goes through Event before the frame is on the wire, and the auto-fix's own log and
// its `auto_fix` event ride with the 7.10 packet, which is not built.
//
// The lines are what such an echo looks like when no pattern can see it: the value BARE (a child that prints
// just the value: no variable name, no separator, no provider prefix), and the value inside a JSON object, where
// the name is followed by `":"` rather than by `=` or `:`, which the name-shaped rule does not match either.
func TestASentinelInTheTokenEnvironmentNeverReachesTheEvent(t *testing.T) {
	const sentinel = "sentinel-token-value-abc"
	t.Setenv("AGENTHUB_TOKEN", sentinel)
	lines := []string{
		"the fake child printed " + sentinel + " with no name around it",
		`{"AGENTHUB_TOKEN":"` + sentinel + `"}`,
		`{"argv":["claude","-p"],"env":{"AGENTHUB_TOKEN":"` + sentinel + `"}}`,
	}
	redactor := NewEventRedactor()
	for _, line := range lines {
		event, reason := redactor.Event(line)
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshalling the event for %q: %v", line, err)
		}
		if strings.Contains(string(encoded), sentinel) {
			t.Errorf("the sentinel reached the event for %q: %s (reason %q)", line, encoded, reason)
		}
	}
}

// TestAClientLineRedactedByNameIsWrittenBackAroundIt drives the same rule through the seam, so the mechanism is
// proven without the real environment and the marker's shape is pinned: the value goes and the text around it
// stays.
func TestAClientLineRedactedByNameIsWrittenBackAroundIt(t *testing.T) {
	const sentinel = "sentinel-token-value-abc"
	original := redactionKnownValues
	redactionKnownValues = func() map[string]string { return map[string]string{"AGENTHUB_TOKEN": sentinel} }
	t.Cleanup(func() { redactionKnownValues = original })

	got := Redact("before " + sentinel + " after")
	if strings.Contains(got, sentinel) {
		t.Fatalf("Redact left the sentinel in place: %q", got)
	}
	for _, want := range []string{"before", "after", "[REDACTED:AGENTHUB_TOKEN]"} {
		if !strings.Contains(got, want) {
			t.Errorf("Redact(%q) = %q, want it to keep %q", "before <sentinel> after", got, want)
		}
	}
}

// assertWithheld checks the one placeholder shape 4.4 rules, for the reason and byte count given.
func assertWithheld(t *testing.T, event map[string]any, reason, wantReason string, wantBytes int) {
	t.Helper()
	if reason != wantReason {
		t.Fatalf("reason = %q, want %q", reason, wantReason)
	}
	if event["type"] != withheldEventType {
		t.Fatalf("event type = %v, want %q", event["type"], withheldEventType)
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v, want a map", event["payload"])
	}
	if payload["reason"] != wantReason {
		t.Fatalf("payload reason = %v, want %q", payload["reason"], wantReason)
	}
	if bytes, ok := payload["bytes"].(int); !ok || bytes != wantBytes {
		t.Fatalf("payload bytes = %v, want %d", payload["bytes"], wantBytes)
	}
}
