package clientjev

import (
	"encoding/json"
	"regexp"
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

// IsTestName reports whether s carries a test-name token - the Test*/Benchmark*/Example* shape RedactForWire
// replaces with <test>. The shape is spelled here once: the redaction above and the completion gate's advice
// about a criterion that names a test both read it, so the advice cannot teach a name the wire would drop.
func IsTestName(s string) bool { return TestNameIn(s) != "" }

// TestNameIn returns the test-name token RedactForWire would replace in s, so a caller can print THAT token
// rather than the word around it: a qualifier (a.TestFoo), a comma list (TestFoo,TestBar) and a hyphenated
// suffix (TestFoo-bar) all carry TestFoo, and a command built from the whole word would match nothing.
func TestNameIn(s string) string { return testToken.FindString(s) }

// FileTokenIn returns the path or file token that CONTAINS span, or "" when span sits in no such token.
// RedactForWire replaces a path first and a file name second, so a span inside either reaches the judge as
// <path>: "TestHelper" inside "TestHelper.go" is a file name, while "TestFoo" beside "x.go" is not. A caller
// deciding which placeholder a criterion carries reads this, not any file name elsewhere in the sentence.
func FileTokenIn(s, span string) string {
	if span == "" {
		return ""
	}
	for _, token := range append(pathToken.FindAllString(s, -1), fileToken.FindAllString(s, -1)...) {
		if strings.Contains(token, span) {
			return token
		}
	}
	return ""
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
// what the judge is shown is how many files changed, how many lines moved and how they split, whether the
// test command ran and how it exited, plus the two commit hashes. The per-path rows carry the counts and
// NOT the path, in WireRows order (a rule, so the sequence names nothing). Any other state is passed
// through unchanged - redactValue still scrubs it - because the decide path's own questions are not
// evidence bundles.
//
// A numstat this cannot read is an ERROR, not zeros: a judge shown files_changed 0 for a change of five
// files answers about a bundle that was never measured.
func countsState(evidence any) (any, error) {
	body, ok := evidence.(clientevidence.Body)
	if !ok {
		return evidence, nil
	}
	stats, err := clientevidence.ParseNumstat(body.Numstat)
	if err != nil {
		return nil, err
	}
	changes := make([]map[string]any, 0, len(stats.Rows))
	for _, row := range stats.WireRows() {
		if row.Binary {
			changes = append(changes, map[string]any{"binary": true})
			continue
		}
		changes = append(changes, map[string]any{"added": row.Added, "deleted": row.Deleted})
	}
	return map[string]any{
		"test_command_ran":             body.Test.Command != "",
		"test_exit_code":               body.Test.ExitCode,
		"tests_failed":                 len(body.Test.Failed),
		clientevidence.KeyFilesChanged: stats.Files(),
		clientevidence.KeyLinesChanged: stats.Total(),
		clientevidence.KeyLinesAdded:   stats.Added,
		clientevidence.KeyLinesDeleted: stats.Deleted,
		"changes":                      changes,
		"base_sha":                     body.BaseSHA,
		"head_sha":                     body.HeadSHA,
	}, nil
}
