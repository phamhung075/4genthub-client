// This file owns the redaction boundary: the pattern redactor Redact - kept compatible with the
// version it replaces in connector.go, same signature and same `[redacted]` marker - and Event, the
// per-event gate the agent upload loop puts every transcript line through. It implements
// agent-boundaries.md 4.3 (what is redacted) and 4.4 (fail closed, per event).
//
// THE ORDER OF THE CHECKS IN Event IS THE DOC'S ORDER, and it is load-bearing: a line that is both a
// JSON array and oversized is withheld as bad_shape, and a line the redactor cannot handle is
// withheld as redactor_error before any other test runs. Every failing case WITHHOLDS - the raw bytes
// never reach a frame - which is the whole point of the gate.
package clientsync

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The numbers and words of the contract, named once so the tests and the daemon read the same ones.
const (
	// ScanLimit is the client's per-event scan limit (4.4): a line STRICTLY larger than this is
	// withheld, because a redactor that cannot read the whole event cannot promise it read all of it.
	ScanLimit = 64 * 1024

	// redactedWord is the marker the pattern redactor writes. It is the same word the version this
	// replaces wrote, so text a caller has pinned does not change.
	redactedWord = "[redacted]"

	// keepPrefix is the replacement the version this replaces used: keep capture group 1 - the header
	// word, the variable name and separator, the scheme - and drop the rest of the match. It is empty
	// of a group when the pattern has none, which is how a wholly-secret token is dropped.
	keepPrefix = "${1}" + redactedWord

	// The withheld event's type and its reasons, exactly as 4.4 spells them.
	withheldEventType   = "redaction_withheld"
	reasonRedactorError = "redactor_error"
	reasonNotUTF8       = "not_utf8"
	reasonBadShape      = "bad_shape"
	reasonTooLarge      = "too_large"
	reasonEnvFile       = "env_file"
	reasonUnsure        = "unsure"

	// The "unsure" detector's numbers (4.4): a token of at least this many characters, drawn from
	// entropyTokenAlphabet, whose Shannon entropy is at or above this many bits per character.
	unsureMinTokenLen = 32
	unsureMinEntropy  = 4.0
)

// EventRedactor holds the state one event stream needs BETWEEN lines: the ids of tool calls that
// named a .env path, so the tool result paired with such a call is withheld too. It is per stream
// because a pairing id is only meaningful inside the transcript that carried the call.
type EventRedactor struct {
	envCallIDs map[string]bool
}

// NewEventRedactor returns a redactor that remembers nothing yet.
func NewEventRedactor() *EventRedactor {
	return &EventRedactor{envCallIDs: map[string]bool{}}
}

// redactLine is a seam, in the repo's style of a package-level func variable rather than an interface
// added for testing (see secretscan.Redact's truncate): the fail-closed rule for redactor_error cannot
// be exercised by any line Event accepts, so the test that proves the rule replaces this and makes it
// panic.
var redactLine = redactLineEvent

// Event turns one transcript line into the event the wire takes, or withholds it. It returns exactly
// one of the two shapes 4.4 rules:
//
//	event != nil, reason == "" -> {"type":"message","payload":{"text": <redacted line>}}
//	event != nil, reason != "" -> {"type":"redaction_withheld","payload":{"reason": <reason>, "bytes": <len(line)>}}
//
// The recover is the FIRST check of 4.4: a panic anywhere in the redactor becomes a withheld
// redactor_error, never a crash and never the raw bytes. It is written as a named-return defer so the
// failure path produces the same placeholder every other reason does.
func (r *EventRedactor) Event(line string) (event map[string]any, reason string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			event = withheldEvent(reasonRedactorError, len(line))
			reason = reasonRedactorError
		}
	}()
	event, reason, err := redactLine(r, line)
	if err != nil {
		return withheldEvent(reasonRedactorError, len(line)), reasonRedactorError
	}
	// A seam is free to return neither event nor reason on success; nothing uploadable came back, so
	// the event is withheld rather than passed on as a nil map.
	if event == nil {
		return withheldEvent(reasonRedactorError, len(line)), reasonRedactorError
	}
	return event, reason
}

// Redact removes secret-shaped substrings from a transcript line BEFORE it is uploaded. This is the
// C1 requirement "redact secrets locally" and it runs here, in the client, because after the frame is
// on the wire the secret is already in the cloud.
//
// Each pattern CAPTURES the prefix it must keep - the header word, the variable name and its
// separator, the scheme, the flag name - so the replacement cannot eat a character of real content.
// The provider-token patterns carry NO capture group, because ${1} expands to the empty string when
// there is no group and those tokens are entirely secret.
func Redact(text string) string {
	for _, rule := range redactionPatterns {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	return text
}

// redactionRule is one pattern and the replacement that preserves whatever the pattern captured.
// Almost every rule uses `${1}[redacted]`, the discipline the version this replaces established; the
// URL-userinfo rule is the one exception, because the `@` that ends the authority is part of the
// match and must be written back (`${1}[redacted]@`), and RE2 has no lookahead that could exclude it
// from the match instead.
type redactionRule struct {
	pattern     *regexp.Regexp
	replacement string
}

// redactionPatterns are the shapes a secret takes in a transcript, written literally. A redactor that
// guesses is worse than one that misses: a missed shape is visible in review, while a wrong guess
// silently eats real content. The first six patterns are the set the previous version had; the rest
// are the ADD list of 4.3.2.
var redactionPatterns = []redactionRule{
	// A PEM private-key block, whole, and first because its base64 body could otherwise be partly
	// consumed by a later pattern. `(?s)` lets `.` cross newlines; RE2 has no backreferences, so the
	// non-greedy `.*?` stops at the matching END line, or at the end of the text when END is missing.
	// No capture group: the block is entirely secret.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`), keepPrefix},
	// An HTTP auth header keeps the scheme and the space.
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]{8,}`), keepPrefix},
	// An assignment keeps the variable name and the separator AND any space after it.
	{regexp.MustCompile(`(?i)((?:AGENTHUB_TOKEN|AGENTHUB_API_TOKEN|DEEPSEEK_API_KEY|ANTHROPIC_API_KEY|OPENAI_API_KEY|AGENTHUB_PUBLIC_URL)\s*[=:]\s*)\S+`), keepPrefix},
	// A provider token is entirely secret, so nothing is kept: these four patterns carry NO capture
	// group, because ${1} expands to the empty string when there is no group and the whole match must
	// go. Wrapping them in parentheses first kept the token and redacted the empty string after it.
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), keepPrefix},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`), keepPrefix},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`), keepPrefix},
	// A JWT is three base64url segments separated by dots.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`), keepPrefix},
	// 4.3.2: an AWS access key id is `AKIA` or `ASIA` plus 16 characters, and entirely secret.
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), keepPrefix},
	// 4.3.2: URL userinfo keeps the scheme, so `scheme://user:pass@host` reads as
	// `scheme://[redacted]@host`. The `@` is part of the match - RE2 has no lookahead to leave it out -
	// so the replacement writes it back. The user may not hold `/`, whitespace, `@` or `:`, and the
	// pattern REQUIRES the `@`, so `http://host:8080` (a port, no userinfo) does not match.
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^/\s@:]+:[^/\s@]+@`), keepPrefix + "@"},
	// 4.3.2: a generic assignment whose name ENDS in a secret word keeps the name and the separator,
	// in `=` and `:` form, any case. The value runs to the next whitespace, as the named-env pattern
	// above already does.
	{regexp.MustCompile(`(?i)(\b[A-Za-z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|PRIVATE_KEY)\s*[=:]\s*)\S+`), keepPrefix},
	// 4.3.2: a command-line flag keeps its name and its separator, in the `=` form and the space form.
	// The separator is part of the capture so `--token=abc` and `--token abc` both redact without
	// losing the flag.
	{regexp.MustCompile(`(?i)(--(?:token|password|secret|api-key)(?:=|\s+))\S+`), keepPrefix},
}

// entropyTokenAlphabet is the character set the unsure detector draws tokens from (4.4), and
// entropyTokenPattern is a run of at least unsureMinTokenLen such characters.
const entropyTokenAlphabet = `[A-Za-z0-9+/=_-]`

var entropyTokenPattern = regexp.MustCompile(entropyTokenAlphabet + `{32,}`)

// envPathPattern is 4.3.3's path rule: a path naming a `.env` file or a dot-variant of one, either at
// the start of the string or right after a path separator.
var envPathPattern = regexp.MustCompile(`(^|/)\.env(\..*)?$`)

// redactLineEvent is the body of the gate, in 4.4's order. It returns an event and the reason it was
// withheld (empty when the event is uploadable), or an error for the one case the recover names.
func redactLineEvent(r *EventRedactor, line string) (map[string]any, string, error) {
	// 2. not_utf8: a line that is not valid UTF-8 is withheld, because every later test would be
	// testing a decoding of bytes nobody can vouch for.
	if !utf8.ValidString(line) {
		return withheldEvent(reasonNotUTF8, len(line)), reasonNotUTF8, nil
	}

	// 3. bad_shape: the transcript entries are objects. A line that parses as JSON but is an array or
	// a scalar is not one, so it is withheld. A line that does not parse as JSON at all is NOT
	// withheld: `rig transcript` prints text lines, and those are uploadable text events.
	var parsed any
	isObject := false
	if err := json.Unmarshal([]byte(line), &parsed); err == nil {
		if _, ok := parsed.(map[string]any); !ok {
			return withheldEvent(reasonBadShape, len(line)), reasonBadShape, nil
		}
		isObject = true
	} else {
		parsed = nil
	}

	// 4. too_large: a line strictly over the scan limit is withheld rather than scanned.
	if len(line) > ScanLimit {
		return withheldEvent(reasonTooLarge, len(line)), reasonTooLarge, nil
	}

	// 5. env_file: a tool call that names a .env path, or the tool result paired with such a call, is
	// withheld. Patterns cannot be trusted to recognise every value in an env file.
	if isObject && r.envFileEvent(parsed) {
		return withheldEvent(reasonEnvFile, len(line)), reasonEnvFile, nil
	}

	// 6. unsure: after redaction, a long high-entropy token means the patterns did not catch
	// something; fail closed rather than upload it.
	redacted := Redact(line)
	if _, unsure := highEntropyToken(redacted); unsure {
		return withheldEvent(reasonUnsure, len(line)), reasonUnsure, nil
	}

	return MessageEvent(map[string]any{"text": redacted}), "", nil
}

// withheldEvent is the placeholder 4.4 rules: it carries the reason and the byte count, and no
// content and no hash of the content.
func withheldEvent(reason string, bytes int) map[string]any {
	return map[string]any{
		"type":    withheldEventType,
		"payload": map[string]any{"reason": reason, "bytes": bytes},
	}
}

// highEntropyToken reports the first token in text of at least unsureMinTokenLen characters from
// entropyTokenAlphabet whose SHANNON ENTROPY, computed over the token's own character frequencies,
// is at or above unsureMinEntropy bits per character.
func highEntropyToken(text string) (string, bool) {
	for _, token := range entropyTokenPattern.FindAllString(text, -1) {
		if shannonEntropy(token) >= unsureMinEntropy {
			return token, true
		}
	}
	return "", false
}

// shannonEntropy is H = -sum(p * log2 p) over the characters of s, in bits per character.
func shannonEntropy(s string) float64 {
	runes := []rune(s)
	if len(runes) == 0 {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range runes {
		counts[r]++
	}
	total := float64(len(runes))
	var entropy float64
	for _, count := range counts {
		p := float64(count) / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// envFileEvent walks a parsed transcript entry and reports whether it is a .env-naming tool call or
// the result paired with one. A call that names a .env path has its id REMEMBERED, so the result that
// later carries that id in `tool_use_id` is withheld too - the pairing is the redactor's state.
func (r *EventRedactor) envFileEvent(root any) bool {
	withhold := false
	walkJSONObjects(root, func(object map[string]any) {
		kind, _ := object["type"].(string)
		switch kind {
		case "tool_use":
			id, _ := object["id"].(string)
			if id != "" && namesEnvFile(object["input"]) {
				if r.envCallIDs == nil {
					r.envCallIDs = map[string]bool{}
				}
				r.envCallIDs[id] = true
				withhold = true
			}
		case "tool_result":
			id, _ := object["tool_use_id"].(string)
			if id != "" && r.envCallIDs[id] {
				withhold = true
			}
		}
	})
	return withhold
}

// namesEnvFile reports whether any string value anywhere inside a tool input names a .env path. It
// looks at every string rather than at a list of keys because 4.3.3 says "names a path matching", not
// "puts it in file_path"; a command line and a grep pattern name a path just as a file_path does. Each
// whitespace-separated word is tested as well, because a command such as `cat .env` names the path as
// a word of the command rather than as the whole string.
func namesEnvFile(input any) bool {
	switch value := input.(type) {
	case map[string]any:
		for _, child := range value {
			if namesEnvFile(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if namesEnvFile(child) {
				return true
			}
		}
	case string:
		if envPathPattern.MatchString(value) {
			return true
		}
		for _, word := range strings.Fields(value) {
			if envPathPattern.MatchString(word) {
				return true
			}
		}
	}
	return false
}

// walkJSONObjects visits every JSON object in v, depth first.
func walkJSONObjects(v any, visit func(map[string]any)) {
	switch value := v.(type) {
	case map[string]any:
		visit(value)
		for _, child := range value {
			walkJSONObjects(child, visit)
		}
	case []any:
		for _, child := range value {
			walkJSONObjects(child, visit)
		}
	}
}
