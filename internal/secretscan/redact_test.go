package secretscan

import (
	"encoding/json"
	"os"
	"testing"
)

// redactCase is one parity case: the input text and known values, and what the REFERENCE
// implementation (scripts/openrig_scrub.py) produces for them. The fixture was generated from that
// script, so this test compares the Go redaction against the Python one rather than against a
// hand-written expectation.
type redactCase struct {
	Name           string            `json:"name"`
	Text           string            `json:"text"`
	Known          map[string]string `json:"known"`
	ExpectText     string            `json:"expect_text"`
	ExpectRedacted bool              `json:"expect_redacted"`
}

func loadRedactCases(t *testing.T) []redactCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/redact_cases.json")
	if err != nil {
		t.Fatalf("read the redaction fixture: %v", err)
	}
	var fixture struct {
		Cases []redactCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse the redaction fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("the redaction fixture carries no cases")
	}
	return fixture.Cases
}

// TestRedactMatchesTheReferenceImplementation is the parity test for the redaction half. It covers
// hits AND misses: a Go implementation that redacts more than the Python one is as wrong as one that
// redacts less, because over-redaction corrupts text and under-redaction leaks.
func TestRedactMatchesTheReferenceImplementation(t *testing.T) {
	for _, c := range loadRedactCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			clean, redacted := Redact(c.Text, c.Known)
			if clean != c.ExpectText {
				t.Errorf("text = %q\nwant       %q", clean, c.ExpectText)
			}
			if redacted != c.ExpectRedacted {
				t.Errorf("redacted = %v, want %v", redacted, c.ExpectRedacted)
			}
		})
	}
}

// TestRedactCoversWhatTheSharedFixtureDoesNot names the three contract points the detection fixture
// carries no case for, so a future reader sees them asserted rather than described.
func TestRedactCoversWhatTheSharedFixtureDoesNot(t *testing.T) {
	// (1) the pair guard: an already-redacted pair is left alone rather than redacted twice.
	if got, redacted := Redact("password=[REDACTED:value]", nil); got != "password=[REDACTED:value]" || redacted {
		t.Errorf("the pair guard let a marker be redacted again: %q redacted=%v", got, redacted)
	}
	// (2) the known-value layer names the VARIABLE, never the value.
	got, redacted := Redact("token is hunter2-hunter2", map[string]string{"MY_SECRET_NAME": "hunter2-hunter2"})
	if got != "token is [REDACTED:MY_SECRET_NAME]" || !redacted {
		t.Errorf("known value = %q redacted=%v, want the variable name in the marker", got, redacted)
	}
	// (3) truncation alone is NOT redaction.
	long := ""
	for i := 0; i < 250; i++ {
		long += "x"
	}
	got, redacted = Redact(long, nil)
	if len([]rune(got)) != RedactedMaxLen || redacted {
		t.Errorf("clean over-long text = %d chars redacted=%v, want %d chars and false",
			len([]rune(got)), redacted, RedactedMaxLen)
	}
}

// TestRedactFailsClosed pins the strongest rule of either scrubber: nothing unscrubbed leaves the
// function, so an internal failure returns an empty text and true. No input can trigger it, which is
// why the cut is a seam and the test replaces it.
func TestRedactFailsClosed(t *testing.T) {
	original := truncate
	defer func() { truncate = original }()
	truncate = func(string, int) string { panic("internal failure") }

	clean, redacted := Redact("token sk-abcdefghijklmnopqrstuvwxyz0123456789ABCD", nil)
	if clean != "" || !redacted {
		t.Fatalf("Redact after an internal failure = %q, %v; want empty text and redacted true", clean, redacted)
	}
}

// TestKnownValuesFromEnv pins the env scan: a secret-looking NAME and a value at least MinSecretLen
// long, keyed by the name because that is what the marker says.
func TestKnownValuesFromEnv(t *testing.T) {
	environ := []string{
		"AGENTHUB_TOKEN=a-long-enough-token",
		"SHORT_TOKEN=abc",
		"UNRELATED=also-a-long-value",
		"NO_EQUALS_SIGN",
		"DEEPSEEK_API_KEY=sk-abcdefghijklmnop",
	}
	got := KnownValuesFromEnv(environ)
	if len(got) != 2 || got["AGENTHUB_TOKEN"] != "a-long-enough-token" || got["DEEPSEEK_API_KEY"] != "sk-abcdefghijklmnop" {
		t.Fatalf("KnownValuesFromEnv = %v", got)
	}
}
