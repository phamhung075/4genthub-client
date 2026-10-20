package clientrestore

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestALineLoggedWithASecretInTheEnvironmentIsWrittenWithoutIt is the LOG half of the ruling's sentinel clause
// (rigd-boundaries.md 7.10: "a sentinel value in the token environment variable ... appears in neither the log
// nor the event"). THE SURFACE IT COVERS IS THIS PACKAGE'S LOG WRITER - the file a seat can read at
// ~/.config/4genthub/logs/room-continue.log and room-watchdog.log - and it covers it at the writer, because the verb
// that passes the value in is the part that varies: what must hold for every line, whatever the step, is that a
// value which reached the process through its environment is not written down.
//
// The line is shaped like the one a failing step produces when it carries a child's output or its own
// environment: the value with no variable name around it, which no pattern can recognise.
func TestALineLoggedWithASecretInTheEnvironmentIsWrittenWithoutIt(t *testing.T) {
	const sentinel = "sentinel-token-value-abc"
	t.Setenv("AGENTHUB_TOKEN", sentinel)

	file, err := os.CreateTemp(t.TempDir(), "room-continue-*.log")
	if err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	log := &teeLog{out: &printed, file: file}
	log.printf("restore step said: the child printed %s and then exited", sentinel)
	log.close()

	body, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []struct{ name, text string }{
		{"the log file", string(body)},
		{"the printed line", printed.String()},
	} {
		if strings.Contains(surface.text, sentinel) {
			t.Errorf("the sentinel reached %s: %q", surface.name, surface.text)
		}
	}
	// The line itself is still there, with its stamp: the value was replaced, not the record dropped.
	for _, want := range []string{"restore step said:", "the child printed", "and then exited"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the log lost %q: %q", want, body)
		}
	}
	if !strings.HasPrefix(string(body), now().Format("2006-01-02T15:04:05-07:00")) {
		t.Errorf("the log line lost its stamp: %q", body)
	}
}

// TestTheLogWriterLeavesAnOrdinaryLineAlone is the cost of the redaction above: it replaces KNOWN values only,
// so the logs an operator already reads keep reading the same.
func TestTheLogWriterLeavesAnOrdinaryLineAlone(t *testing.T) {
	t.Setenv("AGENTHUB_TOKEN", "sentinel-token-value-abc")

	file, err := os.CreateTemp(t.TempDir(), "room-continue-*.log")
	if err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	log := &teeLog{out: &printed, file: file}
	log.printf("restored %s: %d seats briefed", "room-4genthub-min", 7)
	log.close()

	body, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "restored room-4genthub-min: 7 seats briefed") {
		t.Errorf("an ordinary line was changed: %q", body)
	}
	if strings.Contains(string(body), "[REDACTED") {
		t.Errorf("an ordinary line was redacted: %q", body)
	}
}
