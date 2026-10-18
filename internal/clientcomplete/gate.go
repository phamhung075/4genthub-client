package clientcomplete

import (
	"context"
	"fmt"
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

// result is the gate's whole answer. Warnings are advisory: they name criteria whose wording the judge
// cannot read, and they never change the verdict.
type result struct {
	Verdict   clientjev.Verdict `json:"verdict"`
	Reasons   string            `json:"reasons"`
	Checks    checks            `json:"deterministic_checks"`
	Evaluator string            `json:"evaluator"`
	Warnings  []string          `json:"warnings,omitempty"`
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

// any is carries' own rule: each fact stands on its own.
func (f facts) any() bool { return f.command || f.exitCode || f.path || f.sha || f.count }

// carries reports whether the criterion is about a fact the bundle carries. Each fact stands on its own -
// that is the ruling's "a criterion resolving to NONE of these is refused" - so a criterion naming the run's
// exit code or a carried path is judged even when it names neither command nor sha. The bundle's exit-code
// fact is THIS run's own value, so a criterion claiming a different code does not resolve to it (it is still
// judged if it names the command, which is a fact in its own right).
func (r register) carries(criterion string) bool { return r.names(criterion).any() }

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

// pathOnlyWarnings names the criteria the judge will read blind. A criterion that carries a numstat path and
// none of the command, the exit code, a sha or a count is ADMITTED by the register, and then the count-only
// wire (clientjev.RedactForWire, owner ruling O9a) replaces its path with "<path>" before Jev sees it: the
// judge is asked whether "<path> is fixed", a claim whose only fact was removed. These are warnings, not
// refusals - the criterion stays in the judge's set and the verdict is unchanged - and each names the form
// the wire carries: the test command with its exit code, a base/head sha prefix, and the counts.
func (r register) pathOnlyWarnings(criteria []string) []string {
	var warnings []string
	for i, criterion := range criteria {
		f := r.names(criterion)
		if !f.path || f.command || f.exitCode || f.sha || f.count {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("criterion_%d %q names only a carried path, and the wire "+
			"redacts a path to <path> before Jev sees it, so the criterion is judged blind; name a fact the "+
			"judge can read: the test command with its exit code, a base/head sha prefix, or the counts "+
			"(files_changed, lines_changed, lines_added, lines_deleted) with this bundle's value", i+1, criterion))
	}
	return warnings
}

// refusal is the unjudgeable verdict's own words: the criteria it refused, the ones that were judgeable, and
// the register the bundle does carry. R6 of the ruling: a refusal is corrected by REMOVING the claims the
// bundle cannot carry, so the reader must be told both which claims those are and what the bundle does carry.
func (r register) refusal(refused, judgeable []int, criteria []string) string {
	named := make([]string, 0, len(refused))
	for _, i := range refused {
		named = append(named, fmt.Sprintf("criterion_%d %q", i+1, criteria[i]))
	}
	ids := make([]string, 0, len(judgeable))
	for _, i := range judgeable {
		ids = append(ids, fmt.Sprintf("criterion_%d", i+1))
	}
	carries := "nothing this gate can read"
	if len(ids) > 0 {
		carries = strings.Join(ids, ", ")
	}
	return fmt.Sprintf("unjudgeable: %s name no fact this bundle carries, so the task cannot close and Jev was "+
		"not asked; judgeable: %s. The register carries the test command %q with exit code %d, the numstat paths "+
		"(%s) and its counts (files_changed %d, lines_changed %d, lines_added %d, lines_deleted %d), and the "+
		"base/head shas (%s)", strings.Join(named, ", "), carries, r.command, r.exitCode,
		strings.Join(r.pathList(), ", "), r.counts.Files(), r.counts.Total(), r.counts.Added, r.counts.Deleted,
		strings.Join(r.shas, ", "))
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
	warnings := r.pathOnlyWarnings(criteria)
	var refused, judgeable []int
	for i, criterion := range criteria {
		if r.carries(criterion) {
			judgeable = append(judgeable, i)
			continue
		}
		refused = append(refused, i)
	}
	if len(refused) > 0 {
		return result{Verdict: clientjev.Unjudgeable, Reasons: r.refusal(refused, judgeable, criteria),
			Checks: c, Evaluator: "deterministic", Warnings: warnings}
	}
	verdict, reasons, _ := cfg.EvaluateCriteria(ctx, criteria, body)
	return result{Verdict: verdict, Reasons: reasons, Checks: c, Evaluator: "jev", Warnings: warnings}
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
