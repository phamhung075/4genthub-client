package clientjev

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientevidence"
)

// COUNT-ONLY WIRE (owner ruling O9a, 2026-10-16): "No file paths or test names to Jev/TypeSafe:
// counts only." The local decision log keeps the full question, its options and the pick - this
// file shapes only what LEAVES the machine.
//
// The redaction sits at the transport (`call`), not at each caller, so every request this client
// sends - the decide path's questions and the complete/gate path's criteria and state - passes the
// same rule. That is deliberate: a second entry point must not be able to skip it, and a caller that
// passes a criterion naming a file or a test is exactly the case the ruling is about.
//
// The placeholders keep the sentence's SHAPE, so the question a judge is asked keeps its structure
// while losing the names: "the file <path> is fixed and <test> passes". What the judge is told about
// the work is the counts summary in `state` (see countsState), not the names.
//
// What is NOT redacted, on purpose: commit hashes (hex, no separator or suffix), numbers, and ordinary
// prose. A path is a token containing '/'; a file is a token ending in a known source extension; a
// test name is a Test*/Benchmark*/Example* identifier.
var (
	pathToken = regexp.MustCompile(`[A-Za-z0-9_.{}@$~-]*/[A-Za-z0-9_.{}@$~/-]*`)
	fileToken = regexp.MustCompile(`[A-Za-z0-9_.-]+\.(?:go|ts|tsx|js|jsx|rs|py|md|rst|sql|json|yaml|yml|toml|sh|ps1)`)
	testToken = regexp.MustCompile(`\b(?:Test|Benchmark|Example)[A-Z0-9_][A-Za-z0-9_]*\b`)
)

// RedactForWire replaces every file path and test name in s with a placeholder.
func RedactForWire(s string) string {
	s = pathToken.ReplaceAllString(s, "<path>")
	s = fileToken.ReplaceAllString(s, "<path>")
	s = testToken.ReplaceAllString(s, "<test>")
	return s
}

// redactPayload encodes the request with every string shaped by RedactForWire. The walk is over the
// ENCODED payload decoded back to `any`, because the questions are structs and maps of strings by the
// time they reach the transport and a type switch over the caller's values would miss them - measured:
// an option label "fix internal/clientjev/jev.go" went out unshaped while the instruction beside it
// was. Redacting the encoded BYTES instead corrupts JSON (a token straddling an escape sequence), so
// the walk is over decoded values: maps, slices and strings, everything else untouched.
func redactPayload(payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var walk any
	if err := json.Unmarshal(raw, &walk); err != nil {
		return nil, err
	}
	return json.Marshal(redactValue(walk))
}

// redactValue applies RedactForWire to every string in a decoded payload.
func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return RedactForWire(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, inner := range x {
			out[k] = redactValue(inner)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, inner := range x {
			out[i] = redactValue(inner)
		}
		return out
	default:
		return v
	}
}

// countsState is the STATE the criteria request may carry: counts only. The evidence body names files
// in its numstat lines and tests in its command line and failing names, so it is never the payload;
// what the judge is shown is how many files changed, how many lines moved, whether the test command
// ran and how it exited, plus the two commit hashes. Any other state is passed through unchanged -
// redactValue still scrubs it - because the decide path's own questions are not evidence bundles.
func countsState(evidence any) any {
	body, ok := evidence.(clientevidence.Body)
	if !ok {
		return evidence
	}
	files, lines := 0, 0
	for _, line := range strings.Split(body.Numstat, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		files++
		for _, count := range parts[:2] {
			if n, err := strconv.Atoi(count); err == nil {
				lines += n
			}
		}
	}
	return map[string]any{
		"test_command_ran": body.Test.Command != "",
		"test_exit_code":   body.Test.ExitCode,
		"tests_failed":     len(body.Test.Failed),
		"files_changed":    files,
		"lines_changed":    lines,
		"base_sha":         body.BaseSHA,
		"head_sha":         body.HeadSHA,
	}
}
