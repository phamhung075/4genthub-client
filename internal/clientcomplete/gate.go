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

// evaluate runs the gate: the deterministic checks first, which reject without calling Jev, then the
// task's acceptance criteria through Jev. A missing key, a Jev error or an unanswered criterion is
// uncertain, never accept.
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
		return result{Verdict: clientjev.Accept, Reasons: "deterministic checks passed; the task has no acceptance criteria", Checks: c, Evaluator: "deterministic"}
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
