package clientcomplete

import (
	"context"
	"fmt"
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

// result is the gate's whole answer.
type result struct {
	Verdict   clientjev.Verdict `json:"verdict"`
	Reasons   string            `json:"reasons"`
	Checks    checks            `json:"deterministic_checks"`
	Evaluator string            `json:"evaluator"`
}

// register is what the evidence bundle carries, and the only set of facts a criterion may be about: the
// command that was run, the exit code it returned, the paths the numstat lists, and the base/head shas. A
// criterion that resolves to none of them is not weak evidence about the work - it is a claim this gate
// cannot see, and a judge asked about it answers with an opinion and a number on it, which is the confusion
// R1 of seats/architect/RULING-closing-gate-register-and-unjudgeable.md removes.
type register struct {
	command  string
	exitCode int
	paths    []string
	shas     []string
}

// shaPrefix is the shortest sha a criterion may name (the ruling's floor): a shorter run is not a fact about
// a commit, and it is what an ordinary word or number would collide with.
const shaPrefix = 7

// carries reports whether the criterion is about a fact the bundle carries. Each fact stands on its own -
// that is the ruling's "a criterion resolving to NONE of these is refused" - so a criterion naming the run's
// exit code or a carried path is judged even when it names neither command nor sha. The bundle's exit-code
// fact is THIS run's own value, so a criterion claiming a different code does not resolve to it (it is still
// judged if it names the command, which is a fact in its own right).
func (r register) carries(criterion string) bool {
	c := normaliseSpace(criterion)
	if r.command != "" && strings.Contains(c, r.command) {
		return true
	}
	if strings.Contains(c, "exit") && containsInteger(c, strconv.Itoa(r.exitCode)) {
		return true
	}
	for _, path := range r.paths {
		if path != "" && strings.Contains(c, path) {
			return true
		}
	}
	for _, sha := range r.shas {
		if len(sha) >= shaPrefix && strings.Contains(strings.ToLower(c), strings.ToLower(sha[:shaPrefix])) {
			return true
		}
	}
	return false
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
		"(%s) and the base/head shas (%s)", strings.Join(named, ", "), carries, r.command, r.exitCode,
		strings.Join(r.paths, ", "), strings.Join(r.shas, ", "))
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
	paths, lines, err := parseNumstat(body.Numstat)
	switch {
	case err != nil:
		c.D5Numstat = false
		failures = append(failures, "D5: "+err.Error())
	case len(paths) == 0:
		c.D5Numstat = false
		failures = append(failures, "D5: the change touches no file")
	case lines > maxDiffLines:
		c.D5Numstat = false
		failures = append(failures, fmt.Sprintf("D5: the change is %d lines, over %d", lines, maxDiffLines))
	}
	for _, path := range paths {
		if pattern, hit := keptOut(path); hit {
			c.D4Clean = false
			failures = append(failures, fmt.Sprintf("D4: %s is a keep-out path (%s)", path, pattern))
			break
		}
	}
	if len(failures) > 0 {
		return result{Verdict: clientjev.Reject, Reasons: strings.Join(failures, "; "), Checks: c, Evaluator: "deterministic"}
	}
	if len(criteria) == 0 {
		return result{Verdict: clientjev.Uncertain, Reasons: "the task has no acceptance criteria, so Jev has nothing to judge: add criteria to the task, then complete it", Checks: c, Evaluator: "deterministic"}
	}
	r := register{command: normaliseSpace(body.Test.Command), exitCode: body.Test.ExitCode, paths: paths,
		shas: []string{body.BaseSHA, body.HeadSHA}}
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
			Checks: c, Evaluator: "deterministic"}
	}
	verdict, reasons, _ := cfg.EvaluateCriteria(ctx, criteria, body)
	return result{Verdict: verdict, Reasons: reasons, Checks: c, Evaluator: "jev"}
}

// parseNumstat reads `git diff --numstat` lines: added, deleted, path. A binary file counts "-" lines.
func parseNumstat(raw string) (paths []string, lines int, err error) {
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, 0, fmt.Errorf("numstat line %q is not added, deleted, path", line)
		}
		for _, count := range parts[:2] {
			if count == "-" {
				continue
			}
			n, convErr := strconv.Atoi(count)
			if convErr != nil {
				return nil, 0, fmt.Errorf("numstat line %q has a non-numeric count", line)
			}
			lines += n
		}
		paths = append(paths, parts[2])
	}
	return paths, lines, nil
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
