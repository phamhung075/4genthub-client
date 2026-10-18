// Package retiredverbs pins one class of defect: TEXT THIS CLIENT DELIVERS MUST NOT TELL A READER TO RUN
// the orchestrator this cutover removes.
//
// The sweep that closed C3 fixed the printed lines one at a time, and nothing stopped the next edit from
// writing one back, because no test read command text. This package is that test. It reads the three
// surfaces a reader acts on - what the client PRINTS (Go string literals in non-test files), the SKILLS it
// hands to a seat, and this repo's own docs - extracts the command-looking spans, and fails on a span that
// names a retired program unless an allowlist entry carries it WITH A REASON.
//
// THE READ SET HAS A BOUNDARY, and it is stated here rather than left to be inferred from the code: the
// non-test `*.go` files of this module, every file under `docs/`, and every file under `skills/`. The root
// README.md and the runtime crate's own usage strings are OUTSIDE it. A repo-wide search for this class
// previously left the parked deny patterns in internal/clientpolicy/policy.go, which escaped this pin
// because their sub-verb carried a `*` - incidentally, not by an allowlist entry; those patterns were
// deleted with the client's own policy table (2026-10-16, the seat policy is the hub's render), so what
// the search leaves now is the allowlisted spans the table below carries, each with its reason.
//
// COMMENTS ARE NOT SCANNED, deliberately. The register of this pod's comments is the record of its own
// removals ("this used to exec `rig ps`", "THE CLI REACH IS RETIRED RATHER THAN WRAPPED"), so a scan over
// comments would fill the allowlist with the history of the fix instead of finding the defect. What a
// reader is INSTRUCTED to run is what this pins; a comment is not delivered to anyone.
//
// THE SCANNER DOES NOT JUDGE INTENT - it cannot, and a heuristic that guessed would be the "regex that skips
// the file" this design exists to avoid. It extracts spans; the allowlist decides which of them are
// legitimate, one by one, in words.
//
// THE TABLE AND THE ALLOWLIST ARE WRITTEN AS (PROGRAM, SUB-VERB) FIELDS, not as one joined command string,
// and that is deliberate: this package is itself a delivered Go file, so a literal that read "rig ps" in
// code position would be a command span by this package's own rules and would have to be allowlisted. Two
// single-word fields are data about a command; the joined form is the command. The one exception is a
// program that IS the whole command (`pi-runner.js`), and it carries an allowlist entry with its reason.
package retiredverbs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Retired is a command a reader must never be told to run: the program, and the sub-verb it took.
type Retired struct {
	// Program is the first word of the command, matched case-insensitively ("rig", "pi-runner.js",
	// "node").
	Program string
	// Subverb is the word (or two words, for "policy list") that must follow the program for the span to be
	// this command. Empty means the program alone is the command.
	Subverb string
	// Why says which tool this was, because the reader of a failure needs that and not just the token.
	Why string
}

// RetiredCommands is the class, and it is data rather than a pattern so that the list is reviewable: each
// line is a command that existed before the cutover and must not be printed as an instruction after it.
var RetiredCommands = []Retired{
	{Program: "rig", Subverb: "up", Why: "the predecessor orchestrator's up"},
	{Program: "rig", Subverb: "ps", Why: "the predecessor orchestrator's seat listing"},
	{Program: "rig", Subverb: "send", Why: "the predecessor orchestrator's delivery"},
	{Program: "rig", Subverb: "create", Why: "the predecessor orchestrator's room creation"},
	{Program: "rig", Subverb: "launch", Why: "the predecessor orchestrator's seat launch"},
	{Program: "rig", Subverb: "grow", Why: "the predecessor orchestrator's seat growth"},
	{Program: "rig", Subverb: "capture", Why: "the predecessor orchestrator's pane capture"},
	{Program: "rig", Subverb: "daemon", Why: "the predecessor orchestrator's daemon"},
	{Program: "rig", Subverb: "policy list", Why: "the predecessor orchestrator's policy listing"},
	{Program: "pi-runner.js", Why: "the per-seat runner the runtime's native runner replaced"},
}

// Allow carries ONE deliberate survivor: a span that names a retired command in order to say it is gone, to
// forbid it, or to name the thing a client now needs instead.
//
// The entry names the MENTION, not the file: Program and Subverb are the command, and Match must appear in
// the text the span was found in, so a reworded line stops being covered and has to be read again. An entry
// is a statement about a sentence, never a licence for a path.
type Allow struct {
	// File is the repo-relative path, with forward slashes.
	File string
	// Program and Subverb are the retired command's own fields, not a joined string.
	Program string
	Subverb string
	// Match is a fragment of the text that identifies THIS mention - the words that negate or forbid it.
	Match string
	// Reason is the negation, or the prohibition, in the reader's own words.
	Reason string
}

// Allowed is filled from what the scanner finds, never the other way round: a new entry is a decision about
// a sentence, and it says why in words.
var Allowed = []Allow{
	{
		File:    "internal/retiredverbs/retiredverbs.go",
		Program: "pi-runner.js",
		Match:   "pi-runner.js",
		Reason: "the class's own data: the table and this allowlist write each retired command down in order " +
			"to forbid it, so the pin's data names what the pin forbids",
	},
	{
		File:    "skills/client/rig-runtime-switch/SKILL.md",
		Program: "rig",
		Subverb: "ps",
		Match:   "not the predecessor's",
		Reason: "a contrast: this client's own JSON is not the predecessor's node shape, which is the " +
			"distinction the skill exists to draw. The quotation is prose on purpose - a reason that " +
			"re-quoted a command in code position would itself be a command to this scanner",
	},
	{
		File:    "skills/client/spawn-team/SKILL.md",
		Program: "rig",
		Subverb: "up",
		Match:   "NEVER launch or resume with the predecessor",
		Reason: "a prohibition: it names what NOT to run, beside the native up that is what the reader is " +
			"told to run",
	},
	{
		File:    "skills/client/spawn-team/SKILL.md",
		Program: "rig",
		Subverb: "create",
		Match:   "This client has no",
		Reason: "a negation: this client has no such verb, said so an operator does not go looking for the " +
			"predecessor's room creation",
	},
	{
		File:    "skills/client/spawn-team/SKILL.md",
		Program: "rig",
		Subverb: "up",
		Match:   "and no",
		Reason: "a negation: the sentence that names the retired form says this client has no such verb - " +
			"and no such way to create a room - and gives the native creation that replaces it. The Match " +
			"is the negating words only, so an instruction that writes the command without them is not " +
			"covered by this entry",
	},
	{
		File:    "skills/client/spawn-team/SKILL.md",
		Program: "rig",
		Subverb: "up",
		Match:   "never the predecessor's",
		Reason: "a prohibition: never the predecessor's --existing form, beside the native continue as the " +
			"way to bring a stopped room back",
	},
}

// Finding is a command-looking span that names a retired program and is not carried by an allowlist entry.
type Finding struct {
	File    string
	Line    int
	Program string
	Subverb string
	// Span is the command as the reader meets it.
	Span string
	// Text is the text the span was found in - the printed line, or the literal's own line - so an
	// allowlist entry can name the mention rather than the file.
	Text string
	Why  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: `%s` (%s)\n      %s", f.File, f.Line, f.Span, f.Why, f.Text)
}

// Scan reads the delivered text under root and returns every command span that names a retired program.
//
// It reads Go string literals in non-test files (the printed surface) and every file under docs/ and
// skills/ (the handed-to-a-seat and read-by-a-person surfaces). CHANGELOG/ is skipped: an entry is the
// record of what was, and its text is quotation rather than instruction.
func Scan(root string) ([]Finding, error) {
	findings := []Finding{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // a linked skill is a vendored submodule's text, not this repository's
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "target", "CHANGELOG":
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(rel, ".go"):
			if strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			return scanGo(path, rel, &findings)
		case strings.HasPrefix(rel, "docs/"), strings.HasPrefix(rel, "skills/"):
			return scanText(path, rel, &findings)
		}
		return nil
	})
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Span < findings[j].Span
	})
	return findings, err
}

// scanGo reads the string literals of one file: those are what it prints. Comments are skipped by parsing
// without them, which is the whole reason this walk is not a grep.
func scanGo(path, rel string, out *[]Finding) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return err
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		start := fset.Position(lit.Pos()).Line
		for i, line := range strings.Split(text, "\n") {
			for _, span := range spans(line) {
				*out = append(*out, finding(rel, start+i, span, line))
			}
		}
		return true
	})
	return nil
}

// scanText reads one delivered file line by line.
func scanText(path, rel string, out *[]Finding) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for i, line := range strings.Split(string(raw), "\n") {
		for _, span := range spans(line) {
			*out = append(*out, finding(rel, i+1, span, line))
		}
	}
	return nil
}

func finding(rel string, line int, span spanMatch, text string) Finding {
	return Finding{
		File:    rel,
		Line:    line,
		Program: span.Program,
		Subverb: span.Subverb,
		Span:    span.Text,
		Text:    strings.TrimSpace(text),
		Why:     span.Why,
	}
}

// spanMatch is a command span the scanner extracted, with the table entry that matched it.
type spanMatch struct {
	Program string
	Subverb string
	Text    string
	Why     string
}

// spans returns the command-looking spans of ONE line of delivered text. Two shapes count:
//
//   - a backticked span whose first word is a retired program (`rig ps --json`, `pi-runner.js`), because
//     that is how this repo writes a command inline; and
//   - the start of the line itself, after an optional `$ ` prompt, because that is how a command is written
//     in a code block and in a printed procedure.
//
// Everything else is prose. "the rig up the tree was stale" is therefore not a span, and neither is
// "cannot read the seat identity (no runtime, no rig daemon …)": a word that happens to follow the program
// is not a command, and this is where the scanner refuses to guess.
func spans(line string) []spanMatch {
	var out []spanMatch
	for _, quoted := range backticked(line) {
		if m, ok := matchCommand(quoted); ok {
			out = append(out, m)
		}
	}
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "$ ")
	if m, ok := matchCommand(trimmed); ok {
		out = append(out, m)
	}
	return out
}

// backticked returns every backticked span of a line, without its backticks.
func backticked(line string) []string {
	var out []string
	rest := line
	for {
		open := strings.Index(rest, "`")
		if open < 0 {
			return out
		}
		rest = rest[open+1:]
		close := strings.Index(rest, "`")
		if close < 0 {
			return out
		}
		out = append(out, rest[:close])
		rest = rest[close+1:]
	}
}

// matchCommand reports whether a candidate span begins with a retired command, and returns the command it
// matched when it does.
func matchCommand(candidate string) (spanMatch, bool) {
	fields := strings.Fields(candidate)
	if len(fields) == 0 {
		return spanMatch{}, false
	}
	for _, retired := range RetiredCommands {
		if !strings.EqualFold(fields[0], retired.Program) {
			continue
		}
		if retired.Subverb == "" {
			return spanMatch{Program: retired.Program, Text: fields[0], Why: retired.Why}, true
		}
		want := strings.Fields(retired.Subverb)
		if len(fields) < 1+len(want) {
			continue
		}
		got := strings.Join(fields[1:1+len(want)], " ")
		if !strings.EqualFold(got, retired.Subverb) {
			continue
		}
		return spanMatch{
			Program: retired.Program,
			Subverb: retired.Subverb,
			Text:    strings.Join(fields[:1+len(want)], " "),
			Why:     retired.Why,
		}, true
	}
	return spanMatch{}, false
}
