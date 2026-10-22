package clientcomplete

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientevidence"
	"github.com/phamhung075/4genthub-client/internal/clientjev"
)

// maxDiffLines is the sanity bound of check D5: a change of more lines than this is not reviewable.
const maxDiffLines = 20000

// keepOut are paths a task's change must not touch (check D4). A pattern with a slash is a path prefix
// (or a directory anywhere in the path); one without matches a path segment exactly.
var keepOut = []string{".claude/", "agenthub_go/seatcheck", "seatcheck", ".env", ".git/"}

// checks are the deterministic results; Jev is asked only when all of them hold.
type checks struct {
	D3Tests   bool `json:"d3_tests"`
	D4Clean   bool `json:"d4_clean_diff"`
	D5Numstat bool `json:"d5_numstat_sanity"`
}

// result is the gate's whole answer. A criterion whose only fact is a redacted path is a refusal, not a
// warning: the gate refuses it before the judge sees it, with the same words it would print as advice.
type result struct {
	Verdict   clientjev.Verdict `json:"verdict"`
	Reasons   string            `json:"reasons"`
	Checks    checks            `json:"deterministic_checks"`
	Evaluator string            `json:"evaluator"`
}

// register is what the evidence bundle carries, and the only set of facts a criterion may be about: the
// command that was run, the exit code it returned, the counted change the numstat describes (its paths, its
// file and line counts), and the base/head shas. A criterion that resolves to none of them is not weak
// evidence about the work - it is a claim this gate cannot see, and a judge asked about it answers with an
// opinion and a number on it, which is the confusion R1 of
// seats/architect/RULING-closing-gate-register-and-unjudgeable.md removes.
type register struct {
	command  string
	exitCode int
	counts   clientevidence.NumstatStats
	shas     []string
}

// pathList is the numstat's paths, which is what the refusal quotes as the carried change.
func (r register) pathList() []string {
	paths := make([]string, 0, len(r.counts.Rows))
	for _, row := range r.counts.Rows {
		paths = append(paths, row.Path)
	}
	return paths
}

// shaPrefix is the shortest sha a criterion may name (the ruling's floor): a shorter run is not a fact about
// a commit, and it is what an ordinary word or number would collide with.
const shaPrefix = 7

// facts is which register facts a criterion names. carries is the OR of them; the criteria warning needs the
// parts, because a path is a fact this gate reads and the wire drops.
type facts struct {
	command, exitCode, path, sha, count bool
}

// any reports whether the criterion names any register fact. Each fact stands on its own - that is the
// ruling's "a criterion resolving to NONE of these is refused" - so a criterion naming the run's exit code or
// a carried path is judged even when it names neither command nor sha. The bundle's exit-code fact is THIS
// run's own value, so a criterion claiming a different code does not resolve to it (it is still judged if it
// names the command, which is a fact in its own right). Whether the named facts survive the wire is
// pathOnly's question, and the gate refuses on both.
func (f facts) any() bool { return f.command || f.exitCode || f.path || f.sha || f.count }

// pathOnly reports whether a carried path is the criterion's ONLY fact. The register can read such a
// criterion and the judge cannot: the count-only wire (clientjev.RedactForWire, owner ruling O9a) replaces
// the path with "<path>" before Jev sees it, so the judge would be asked whether "<path> is fixed" - a
// claim whose only fact was removed. The gate knows that in the same millisecond it resolves the criterion,
// so it refuses instead of spending a judge call and a completion cycle on a verdict it cannot defend.
// A criterion that carries a path AND another fact stays admitted: the claim then survives the redaction
// (precedent 35431928: "go -C agenthub_go test <path> -count=1 exits 0" closed at 0.96-0.98).
func (f facts) pathOnly() bool { return f.path && !f.command && !f.exitCode && !f.sha && !f.count }

// names says which of the register's facts a criterion names.
func (r register) names(criterion string) facts {
	c := normaliseSpace(criterion)
	var f facts
	f.command = r.command != "" && strings.Contains(c, r.command)
	f.exitCode = strings.Contains(c, "exit") && containsInteger(c, strconv.Itoa(r.exitCode))
	for _, p := range r.pathList() {
		if p != "" && strings.Contains(c, p) {
			f.path = true
			break
		}
	}
	for _, s := range r.shas {
		if len(s) >= shaPrefix && strings.Contains(strings.ToLower(c), strings.ToLower(s[:shaPrefix])) {
			f.sha = true
			break
		}
	}
	f.count = r.namesACount(c)
	return f
}

// countKeys is the wire's count vocabulary and the value each key states. A key is a fact only when the
// criterion spells it and the number in the same clause is THIS bundle's number: a count the bundle does
// not carry is refused exactly as an unlisted path or an absent sha is.
var countKeys = []struct {
	key   string
	value func(clientevidence.NumstatStats) int
}{
	{clientevidence.KeyFilesChanged, func(s clientevidence.NumstatStats) int { return s.Files() }},
	{clientevidence.KeyLinesChanged, func(s clientevidence.NumstatStats) int { return s.Total() }},
	{clientevidence.KeyLinesAdded, func(s clientevidence.NumstatStats) int { return s.Added }},
	{clientevidence.KeyLinesDeleted, func(s clientevidence.NumstatStats) int { return s.Deleted }},
}

// namesACount reports whether the criterion names a wire count key and THAT key's value: the key occurrence
// and the value must share a clause - the window runs from the end of the key to the next count-key
// occurrence (either spelling) or the end of the criterion, and the FIRST integer in it must equal the
// bundle's number for that key. A match anywhere in the text is REJECTED: the sha tokenizer splits
// "2c34515" into the tokens 2 and 34515, so a global match would admit "files_changed is 2" on a
// three-file change whose sha carries a 2. No fixed radius either: "files_changed across the whole change
// is 7" is still the key's own clause.
func (r register) namesACount(c string) bool {
	c = strings.ToLower(c)
	type occurrence struct {
		start, end, value int
	}
	var keys []occurrence
	for _, k := range countKeys {
		value := k.value(r.counts)
		for _, form := range []string{k.key, strings.ReplaceAll(k.key, "_", " ")} {
			for from := 0; from < len(c); {
				i := strings.Index(c[from:], form)
				if i < 0 {
					break
				}
				keys = append(keys, occurrence{start: from + i, end: from + i + len(form), value: value})
				from += i + len(form)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].start < keys[j].start })
	for i, key := range keys {
		windowEnd := len(c)
		for _, next := range keys[i+1:] {
			if next.start >= key.end {
				windowEnd = next.start
				break
			}
		}
		if n, ok := firstInteger(c[key.end:windowEnd]); ok && n == key.value {
			return true
		}
	}
	return false
}

// firstInteger is the first whole number in the text, tokenised exactly as containsInteger does (split on
// non-[0-9.], trim dots): "is 4" is 4, "is 0.5" carries no integer, and "" carries none.
func firstInteger(text string) (int, bool) {
	isNumberRune := func(r rune) bool { return (r >= '0' && r <= '9') || r == '.' }
	for _, run := range strings.FieldsFunc(text, func(r rune) bool { return !isNumberRune(r) }) {
		token := strings.Trim(run, ".")
		if token == "" || strings.Contains(token, ".") {
			continue
		}
		n, err := strconv.Atoi(token)
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}

// shaToken is a sha a criterion may name: the register's 7-char floor, and at most one 40-hex commit sha.
// Either case is accepted, because a criterion may spell a sha the way it was pasted.
var shaToken = regexp.MustCompile(`(?i)\b[0-9a-f]{7,40}\b`)

// fullSHA is rev-parse's answer for a commit: 40 lowercase hex digits and nothing else.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// commitNamed resolves a hex token to the full commit sha it names, or "" when it names no commit at all
// (an ordinary word that happens to be hex, a sha this repository does not hold). `^{commit}` refuses a
// tree or a blob, and --end-of-options keeps a ref that begins with `-` from being read as a flag. The
// FULL sha is what is wanted here, not cat-file's yes/no: an abbreviated token must compare equal to the
// canonical --base the evidence bundle carries.
func commitNamed(ctx context.Context, token string) string {
	command := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", token+"^{commit}")
	var out bytes.Buffer
	command.Stdout = &out
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return ""
	}
	sha := strings.TrimSpace(out.String())
	if !fullSHA.MatchString(sha) {
		return ""
	}
	return sha
}

// baseMismatchWarnings names the criteria that state a base the gate is not measuring. The gate judges
// <--base>..HEAD, and a criterion whose only base is another commit measures something else: the verdict
// then refuses it (unjudgeable) AFTER the reviewer passed and the work is pushed, when the seat can only
// rewrite the criterion. The verb knows the mismatch the moment it reads --base, so it says it first.
//
// A criterion is excluded from this warning when its sha is --base or the bundle's head_sha: both are diff
// facts the bundle carries. Resolution is one `git rev-parse` per distinct token, cached; a token that
// names no commit in this repository (a word that looks like hex, a sha from another repository) is not a
// base the verb can call wrong. One line per criterion, naming its first foreign sha; exit codes unchanged.
func baseMismatchWarnings(ctx context.Context, criteria []string, base, head string) []string {
	resolved := make(map[string]string)
	var warnings []string
	for i, criterion := range criteria {
		for _, token := range shaToken.FindAllString(normaliseSpace(criterion), -1) {
			full, seen := resolved[token]
			if !seen {
				full = commitNamed(ctx, token)
				resolved[token] = full
			}
			if full == "" || full == base || full == head {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("criterion_%d names %s, --base is %s; the gate measures %s..HEAD",
				i+1, full, base, base))
			break
		}
	}
	return warnings
}

// refusal is the unjudgeable verdict's own words: the criteria it REFUSED, split into the ones naming no
// fact the bundle carries (silent) and the ones whose only fact is a path the wire redacts (blind), the ones
// that were judgeable, and the register the bundle does carry. R6 of the ruling: a refusal is corrected by
// REMOVING the claims the bundle cannot carry, so the reader must be told both which claims those are and
// what the bundle does carry. The blind criterion's advice is the vocabulary the wire does carry - the test
// command with its exit code, a base/head sha prefix, the counts - not the path that was just taken away.
func (r register) refusal(silent, blind, judgeable []int, criteria []string) string {
	isSilent := make([]bool, len(criteria))
	for _, i := range silent {
		isSilent[i] = true
	}
	isBlind := make([]bool, len(criteria))
	for _, i := range blind {
		isBlind[i] = true
	}
	named := make([]string, 0, len(silent)+len(blind))
	for i, criterion := range criteria {
		switch {
		case isSilent[i]:
			named = append(named, silentClaim(i, criterion))
		case isBlind[i]:
			named = append(named, fmt.Sprintf("criterion_%d %q names only a carried path, and the wire redacts a "+
				"path to <path> before Jev sees it, so the criterion would be judged blind", i+1, criterion))
		}
	}
	ids := make([]string, 0, len(judgeable))
	for _, i := range judgeable {
		ids = append(ids, fmt.Sprintf("criterion_%d", i+1))
	}
	carries := "nothing this gate can read"
	if len(ids) > 0 {
		carries = strings.Join(ids, ", ")
	}
	return fmt.Sprintf("unjudgeable: %s. The task cannot close and Jev was not asked; judgeable: %s. A criterion "+
		"must name a fact the judge can read: the test command %q with its exit code %d, a base/head sha prefix "+
		"(%s), or the counts (files_changed %d, lines_changed %d, lines_added %d, lines_deleted %d) with this "+
		"bundle's value. The register also carries the numstat paths (%s), but the wire replaces a path with "+
		"<path>, so a path is not enough on its own", strings.Join(named, "; "), carries, r.command, r.exitCode,
		strings.Join(r.shas, ", "), r.counts.Files(), r.counts.Total(), r.counts.Added, r.counts.Deleted,
		strings.Join(r.pathList(), ", "))
}

// silentClaim words a criterion that names no register fact. A criterion naming a test is the one silent case
// with a known replacement: the count-only wire sends the name as <test> (clientjev.RedactForWire), so the
// criterion is corrected by quoting the narrow command that runs that test and passing it as --test, which the
// register carries as the command and its exit code. Both the name and the placeholder come from clientjev
// (TestNameIn, TestNameClaim), the same shapes the redaction uses, so the advice cannot teach a name the wire
// would drop or a selector that matches nothing: the advised pattern is the TOKEN the redaction matched, not
// the word around it (a.TestFoo, TestFoo,TestBar and TestFoo-bar all advise '^TestFoo$'), and a benchmark takes
// -bench because -run selects tests only.
//
// The placeholder is decided POSITIONALLY, by whether the match itself sits inside a path or file token, and
// not by any file name elsewhere in the criterion: a test name beside a path is still a <test> ("TestFoo
// passes; x.go is fixed" advises -run '^TestFoo$'), and so is a test named beside a file whose path carries its
// own name ("TestFoo passes; TestFoo_test.go is created" - reviewer's counterexample, 2026-10-22). When the
// match sits INSIDE a file token ("TestHelper" in "TestHelper.go") the criterion names a file, and its stem runs
// no test: the advice keeps the <path> reason and the names-no-fact correction, and teaches no selector built
// from that stem - `go test -run '^TestHelper$'` matches nothing and exits 0, which is the green-having-run-
// nothing this advice exists to prevent.
func silentClaim(i int, criterion string) string {
	name, file := clientjev.TestNameClaim(criterion)
	if name == "" {
		return fmt.Sprintf("criterion_%d %q names no fact this bundle carries", i+1, criterion)
	}
	if file != "" {
		return fmt.Sprintf("criterion_%d %q: %s is a file name, and a file name reaches the judge as <path>, so "+
			"its stem runs no test and the criterion names no fact this bundle carries; name the command that "+
			"measures that file and pass that same command as --test, e.g. `go test <pkg> -count=1` exits 0",
			i+1, criterion, file)
	}
	flag := "-run"
	if strings.HasPrefix(name, "Benchmark") {
		flag = "-bench"
	}
	return fmt.Sprintf("criterion_%d %q: a test name reaches the judge as <test>, so name the command that runs "+
		"it and pass that same command as --test, e.g. `go test <pkg> %s '^%s$' -count=1` exits 0",
		i+1, criterion, flag, name)
}

// normaliseSpace folds every run of whitespace into one space, which is the form the ruling defines the
// command match on: a criterion wrapped across lines still names the command it cites.
func normaliseSpace(text string) string { return strings.Join(strings.Fields(text), " ") }

// containsInteger reports whether the text carries n as a whole number of its own: "exits 0" and "exit code
// 0." do, while "10" and "0.5" do not.
func containsInteger(text, n string) bool {
	isNumberRune := func(r rune) bool { return (r >= '0' && r <= '9') || r == '.' }
	for _, run := range strings.FieldsFunc(text, func(r rune) bool { return !isNumberRune(r) }) {
		if strings.Trim(run, ".") == n {
			return true
		}
	}
	return false
}

// evaluate runs the gate: the deterministic checks first, which reject without calling Jev, then the register
// gate, which REFUSES a criterion the bundle carries no fact about instead of scoring it, and only then the
// criteria that survive through Jev. A missing key, a Jev error, an unanswered criterion or a task with no
// criteria at all is uncertain, never accept: passing the deterministic checks alone is not a verdict.
func evaluate(ctx context.Context, cfg clientjev.Config, criteria []string, body clientevidence.Body) result {
	c := checks{D3Tests: true, D4Clean: true, D5Numstat: true}
	var failures []string
	if body.Test.Command == "" {
		c.D3Tests = false
		failures = append(failures, "D3: no test command was run (pass --test)")
	} else if body.Test.ExitCode != 0 || len(body.Test.Failed) > 0 {
		c.D3Tests = false
		failures = append(failures, "D3: the test run failed")
	}
	stats, err := clientevidence.ParseNumstat(body.Numstat)
	switch {
	case err != nil:
		c.D5Numstat = false
		failures = append(failures, "D5: "+err.Error())
	case stats.Files() == 0:
		c.D5Numstat = false
		failures = append(failures, "D5: the change touches no file")
	case stats.Total() > maxDiffLines:
		c.D5Numstat = false
		failures = append(failures, fmt.Sprintf("D5: the change is %d lines, over %d", stats.Total(), maxDiffLines))
	}
	for _, row := range stats.Rows {
		if pattern, hit := keptOut(row.Path); hit {
			c.D4Clean = false
			failures = append(failures, fmt.Sprintf("D4: %s is a keep-out path (%s)", row.Path, pattern))
			break
		}
	}
	if len(failures) > 0 {
		return result{Verdict: clientjev.Reject, Reasons: strings.Join(failures, "; "), Checks: c, Evaluator: "deterministic"}
	}
	if len(criteria) == 0 {
		return result{Verdict: clientjev.Uncertain, Reasons: "the task has no acceptance criteria, so Jev has nothing to judge: add criteria to the task, then complete it", Checks: c, Evaluator: "deterministic"}
	}
	r := register{command: normaliseSpace(body.Test.Command), exitCode: body.Test.ExitCode, counts: stats,
		shas: []string{body.BaseSHA, body.HeadSHA}}
	var silent, blind, judgeable []int
	for i, criterion := range criteria {
		f := r.names(criterion)
		switch {
		case f.pathOnly():
			blind = append(blind, i)
		case f.any():
			judgeable = append(judgeable, i)
		default:
			silent = append(silent, i)
		}
	}
	if len(silent) > 0 || len(blind) > 0 {
		return result{Verdict: clientjev.Unjudgeable, Reasons: r.refusal(silent, blind, judgeable, criteria),
			Checks: c, Evaluator: "deterministic"}
	}
	verdict, reasons, _ := cfg.EvaluateCriteria(ctx, criteria, body)
	return result{Verdict: verdict, Reasons: reasons, Checks: c, Evaluator: "jev"}
}

func keptOut(path string) (string, bool) {
	segments := strings.Split(path, "/")
	for _, pattern := range keepOut {
		dir := strings.TrimSuffix(pattern, "/")
		if strings.Contains(dir, "/") {
			if path == dir || strings.HasPrefix(path, dir+"/") {
				return pattern, true
			}
			continue
		}
		for i, segment := range segments {
			isDir := i < len(segments)-1
			if segment == dir && (isDir || !strings.HasSuffix(pattern, "/")) {
				return pattern, true
			}
		}
	}
	return "", false
}
