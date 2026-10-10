// Redaction is the second half of this package: Contains DETECTS credential-shaped text, Redact
// REPLACES it, and both share the labelled pattern layer below so the two can never disagree about
// what a secret looks like. It is the Go home of what scripts/openrig_scrub.py does today, which is
// why the labels, the marker spelling and the limits are pinned here as a CONTRACT: the Python
// scrubber emits the same markers, and its own docstring says it mirrors the server-side scanner and
// shares testdata/scan_cases.json.
//
// THE MARKER NAMES ITS SOURCE, NEVER ITS VALUE: a known value is replaced by [REDACTED:<ENV VAR
// NAME>], so the redacted text says which variable leaked rather than what it held - the difference
// between a scrubber and a second leak.
//
// Contains is deliberately untouched by this file: three packages call it as a guard, and the rule
// for that half is ADD, NEVER MODIFY.
package secretscan

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// The redaction contract, in the numbers the Python scrubber uses.
const (
	// RedactedMaxLen is the length a redacted text is cut to, BY RUNES (the Python slice counts
	// characters, so a byte slice here would cut multi-byte text differently).
	RedactedMaxLen = 200
	// MinSecretLen is the shortest known value worth redacting; below it a value is too common to
	// replace without corrupting unrelated text.
	MinSecretLen = 6
	// markerIntro opens every marker; the label or variable name that follows is the source.
	markerIntro = "[REDACTED:"
)

// redactionPatterns are the labelled patterns, in the order the Python scrubber applies them (pem
// first, because its body is greedy and would otherwise be partly consumed by a later pattern).
var redactionPatterns = []labelledPattern{
	{"pem", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)},
	{"bearer", regexp.MustCompile(`(?i)Bearer[\t\n\f\r ]+[A-Za-z0-9._~+/=-]{20,}`)},
	{"sk", regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)},
	{"aws", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"github", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`)},
	{"url-credentials", regexp.MustCompile(`://[^\t\n\f\r /:@]*:[^\t\n\f\r /]+@`)},
}

// pairPattern is the `password=...` shape, whose marker is `value` and whose key and separator are
// KEPT so the redacted text still reads as a setting.
//
// The Python scrubber guards this one with `(?!\[REDACTED)`, which RE2 cannot express; the guard is
// applied in code below instead - a match whose value already starts with a marker is left alone, so
// redacting twice is a no-op rather than `[REDACTED:value]` inside `[REDACTED:value]`.
var pairPattern = regexp.MustCompile(
	`(?i)((?:password|passwd|secret|token|api[_-]?key)[\t\n\f\r ]*[=:][\t\n\f\r ]*)([^\t\n\f\r ]{6,})`,
)

// secretNamePattern picks the environment names whose VALUES are worth redacting.
var secretNamePattern = regexp.MustCompile(`(?i)TOKEN|SECRET|KEY|PASSWORD|PASSWD|CREDENTIAL`)

type labelledPattern struct {
	label   string
	pattern *regexp.Regexp
}

// truncate is a seam, in the repo's style of a package variable rather than an interface added for
// testing: the fail-closed rule below cannot be exercised by any input this function accepts, so the
// test that proves the rule replaces this.
var truncate = truncateRunes

// KnownValuesFromEnv returns the environment entries worth redacting: a name matching
// secretNamePattern and a value at least MinSecretLen long. The map is keyed by the NAME, because
// the marker names the variable rather than the value.
func KnownValuesFromEnv(environ []string) map[string]string {
	out := map[string]string{}
	for _, entry := range environ {
		name, value, found := strings.Cut(entry, "=")
		if !found || !secretNamePattern.MatchString(name) || len(value) < MinSecretLen {
			continue
		}
		out[name] = value
	}
	return out
}

// Redact replaces known values and credential-shaped substrings in text, returning the redacted text
// and whether anything changed. IT FAILS CLOSED: nothing unscrubbed leaves this function, so an
// internal failure returns an empty text and true.
//
// The order is the Python scrubber's, because the outcome depends on it: known values first, longest
// value first, and within a value its longest form first; then the labelled patterns; then the pair
// pattern; and the length cut LAST, so the marker that replaces a long value is never itself cut off.
func Redact(text string, known map[string]string) (clean string, redacted bool) {
	defer func() {
		if recover() != nil {
			clean, redacted = "", true
		}
	}()

	clean = text
	for _, name := range namesLongestValueFirst(known) {
		value := known[name]
		if len(value) < MinSecretLen {
			continue
		}
		marker := markerIntro + name + "]"
		for _, form := range formsLongestFirst(value) {
			clean = strings.ReplaceAll(clean, form, marker)
		}
	}
	for _, labelled := range redactionPatterns {
		clean = labelled.pattern.ReplaceAllString(clean, markerIntro+labelled.label+"]")
	}
	clean = pairPattern.ReplaceAllStringFunc(clean, func(match string) string {
		parts := pairPattern.FindStringSubmatch(match)
		if len(parts) != 3 || strings.HasPrefix(parts[2], "[REDACTED") {
			return match
		}
		return parts[1] + markerIntro + "value]"
	})
	// The flag is computed BEFORE the cut: truncation alone is not redaction, and a caller that
	// treated it as one would report a change that never happened.
	redacted = clean != text
	return truncate(clean, RedactedMaxLen), redacted
}

// namesLongestValueFirst orders the known values by descending length, ties by name. The Python
// scrubber sorts by value length alone; naming the tie-break here keeps Go deterministic without
// changing any outcome the Python can produce from a dict it built in name order.
func namesLongestValueFirst(known map[string]string) []string {
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(known[names[i]]) != len(known[names[j]]) {
			return len(known[names[i]]) > len(known[names[j]])
		}
		return names[i] < names[j]
	})
	return names
}

// formsLongestFirst is the Python _forms set: the value itself, its standard and URL-safe base64, and
// its URL-encoded form, longest first so a shorter form cannot eat a longer one.
func formsLongestFirst(value string) []string {
	raw := []byte(value)
	forms := []string{
		value,
		base64.StdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		url.QueryEscape(value),
	}
	seen := map[string]bool{}
	unique := forms[:0]
	for _, form := range forms {
		if form == "" || seen[form] {
			continue
		}
		seen[form] = true
		unique = append(unique, form)
	}
	sort.SliceStable(unique, func(i, j int) bool { return len(unique[i]) > len(unique[j]) })
	return unique
}

// truncateRunes cuts s to at most limit characters, counting characters rather than bytes so a
// multi-byte text is cut where the Python slice would cut it.
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
