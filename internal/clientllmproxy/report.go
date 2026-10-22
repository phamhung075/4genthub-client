// The report a supervisor and doctor print: what the route table says is proxied, what is not, and how
// many requests were withheld today. It is one function on purpose - a caller that renders its own
// version of these counts is how a report and the table it describes drift apart.
package clientllmproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// codexUnselectedStatus is codex's state until a seat runs under the profile that selects this client's
// provider entry: the entry is present and inert, which is unproxied - not a pass.
const codexUnselectedStatus = "unproxied (provider entry present, not selected)"

// Report renders the detail line doctor prints beside its safeguard rows:
//
//	llm: 2 proxied routes, 3 unproxied (omp-deepseek, codex-unselected, agy), 5 withheld today
//
// The count is of ROUTES, not CLIs: the probe measured one CLI answering both ways, so a per-CLI count
// would be false twice. A route counts as proxied only while a listener on its surface answers
// /__llmproxy - the shared ANTHROPIC_BASE_URL listener makes claude-code and omp-anthropic proxied
// together, and a stopped listener makes both read unproxied. codex never counts as proxied here: its
// provider entry is inert until selected, so it is listed with that qualifier once the entry exists.
// stateRoot is read for the withheld count only; pass "" when there is no state root to count.
func Report(stateRoot string) string {
	return report(stateRoot, func(port int) (string, bool) {
		return probeListener("127.0.0.1", strconv.Itoa(port))
	}, codexEntryPresent(os.Environ))
}

// CodexStatus is codex's own detail line, for a caller that prints each route: the entry present and not
// selected until a seat runs under the profile, otherwise the bare unproxied state.
func CodexStatus() string {
	if codexEntryPresent(os.Environ) {
		return codexUnselectedStatus
	}
	return "unproxied"
}

// report is Report with the two inputs a test must control handed in: how a port answers, and whether
// codex's provider entry is present.
func report(stateRoot string, listen func(port int) (string, bool), codexPresent bool) string {
	live := make(map[string]bool)
	for _, row := range routeTable {
		if row.EnvVar == "" {
			continue
		}
		if _, known := live[row.EnvVar]; known {
			continue
		}
		// The answering route must be on the SAME variable: a listener on this port that belongs to
		// another surface is not this route's proxy.
		found, answered := listen(row.Port)
		live[row.EnvVar] = answered && envVarOf(found) == row.EnvVar
	}

	proxied := 0
	var unproxied []string
	for _, row := range routeTable {
		if row.EnvVar == "" {
			continue
		}
		if live[row.EnvVar] {
			proxied++
			continue
		}
		unproxied = append(unproxied, row.Name)
	}
	codex := "codex"
	if codexPresent {
		codex = "codex-unselected"
	}
	unproxied = append(unproxied, unproxiedRouteNames[0], codex, unproxiedRouteNames[1])

	return fmt.Sprintf("llm: %d proxied %s, %d unproxied (%s), %d withheld today",
		proxied, plural(proxied, "route", "routes"), len(unproxied), strings.Join(unproxied, ", "),
		withheldCount(stateRoot))
}

// plural picks the singular word for exactly one.
func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// withheldCount is the number of refusal records under <state-root>/llm-withheld/<route>/. The records
// are pruned at seven days, so this is the recent count the detail line calls today's; an unreadable
// state root reads as zero rather than turning the report into an error the rows already carry.
func withheldCount(stateRoot string) int {
	if stateRoot == "" {
		return 0
	}
	paths, err := filepath.Glob(filepath.Join(stateRoot, recordDirName, "*", "*.json"))
	if err != nil {
		return 0
	}
	return len(paths)
}
