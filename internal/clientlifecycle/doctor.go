// doctor: the read-only report the owner asked for, and the ONE check `up` fails on.
//
// ITS OUTPUT IS 7.2's ROWS, THEN THE TAIL OF alerts.log - one line per row, then the last lines of the
// alert log. It NEVER starts, restarts or signals anything; the lead's lean on that is ruled, and it is
// why this verb and `up` share the computation rather than the action: `doctor` observes, `up` repairs,
// and they agree on what "alive" means because they run the same rows.
//
// THE ROWS ARE COMPUTED HERE, NOT READ FROM AGENT (7.2). A dead agent must still be reported, so the reader
// cannot depend on the process it is checking: the files and pgrep are the sources, and clientservices is
// where that computation lives.
package clientlifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

const doctorUsage = "usage: 4genteam doctor [RIG] [--json] [--state-root DIR]"

// doctorCommand is the report verb. It needs no rig tool: it reads files and the process table, and a
// machine where the rig tool is missing is exactly a machine whose report matters.
type doctorCommand struct{}

func (doctorCommand) Name() string { return "doctor" }
func (doctorCommand) Summary() string {
	return "report every safeguard (compact, watchdog, messages, bridge, agent); read-only"
}

func (doctorCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, doctorUsage,
			"One line per safeguard row (rig, safeguard, state, age), then the tail of alerts.log.\n"+
				"Exits 0 only when every row is running; otherwise 3, naming every row that is not on stderr as\n"+
				"`<rig> <safeguard>: <state>, age <n>s`. It is read-only: it starts nothing, restarts nothing\n"+
				"and signals nothing. The repair verb is `up`.")
		return clientcmd.ExitOK
	}
	asJSON := false
	rootFlag, remaining, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam doctor: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUsage)
	}
	var rest []string
	for _, arg := range remaining {
		switch {
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-") && arg != "-":
			fmt.Fprintf(stderr, "4genteam doctor: unknown argument %s. %s\n", arg, doctorUsage)
			return clientcmd.ExitUsage
		default:
			rest = append(rest, arg)
		}
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "4genteam doctor: unexpected argument %s - this verb takes one rig name, or none for every listed rig. %s\n", rest[1], doctorUsage)
		return clientcmd.ExitUsage
	}
	rig := ""
	if len(rest) == 1 {
		rig = rest[0]
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam doctor: %v\n", err)
		return clientcmd.ExitUsage
	}
	return runDoctor(rig, root, asJSON, stdout, stderr)
}

// rowsFor is the row computation as a seam, in the repo's style of a package-level variable rather than
// an interface added for testing: every verb that READS the report goes through it, so a test drives a
// verb's decision without producing processes or heartbeat files.
var rowsFor = safeguardRows

// safeguardRows is the ONE computation doctor, `status --json`, `compact`, `agent status` and `up`'s check
// share.
//
// A named rig is reported on its own (with the two machine-scoped rows); no rig means EVERY rig in
// agent.json's list, because that list is what "meant to run" means (7.9). A machine whose agent has never
// run has an empty list and still gets the bridge and agent rows, which is the point: agent's own absence
// is the loudest row there is.
func safeguardRows(rig, root string) []clientservices.Health {
	// A REPORT SURFACE MAY GO ON WITHOUT THE LIST. The rigs list decides WHICH rooms get rows; a failed read
	// leaves it empty exactly as a machine whose agent never ran does, and this verb neither writes nor acts,
	// so the rows for the two machine-scoped safeguards still print. `agent status` says in words when the
	// read failed; naming it here needs an error shape in the report seam, which is its own unit.
	state, _ := clientservices.ReadAgentState()
	rigs := state.Rigs
	if rig != "" {
		rigs = []string{rig}
	}
	return clientservices.Verify(context.Background(), clientservices.Env{
		Root:       root,
		Rigs:       rigs,
		Supervised: state.Safeguards,
	})
}

// runDoctor prints the rows and decides the exit code. `--json` prints the rows instead of the lines, and
// the tail of alerts.log is a reading surface for the human output only: the JSON is the rows 7.5 says it
// is.
func runDoctor(rig, root string, asJSON bool, stdout, stderr io.Writer) int {
	rows := rowsFor(rig, root)
	if asJSON {
		body, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam doctor: cannot print the rows: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintln(stdout, string(body))
	} else {
		for _, row := range rows {
			fmt.Fprintln(stdout, clientservices.StateLine(row))
		}
		for _, line := range clientservices.TailAlerts(root) {
			fmt.Fprintln(stdout, line)
		}
	}
	down := clientservices.NotRunning(rows)
	if len(down) == 0 {
		return clientcmd.ExitOK
	}
	// The SAME line the report prints, on stderr, so a caller that only reads stderr still learns which
	// safeguard is down, in which room, and for how long.
	for _, row := range down {
		fmt.Fprintln(stderr, clientservices.StateLine(row))
	}
	return clientcmd.ExitUnavailable
}

// safeguardWaitGap is how often `up` re-reads the report while it waits for the safeguards to come up.
var safeguardWaitGap = 200 * time.Millisecond

// waitForSafeguards is 7.5's start grace, as a seam: a test must not wait a threshold out, and the
// thresholds are tens of seconds by design.
var waitForSafeguards = waitForSafeguardsImpl

// waitForSafeguardsImpl polls the row computation until every row is running, and gives up when a row that
// is not running has already had its own 7.4 threshold since `up` started - that row's own grace is the
// longest `up` may reasonably attribute to a slow start.
//
// It returns the last report either way, so the caller names the very rows the reader will see in
// `doctor`.
func waitForSafeguardsImpl(rig, root string) ([]clientservices.Health, bool) {
	started := time.Now()
	for {
		rows := rowsFor(rig, root)
		if clientservices.AllRunning(rows) {
			return rows, true
		}
		elapsed := time.Since(started)
		for _, row := range clientservices.NotRunning(rows) {
			if elapsed >= clientservices.DefaultSilentAfter(row.Safeguard) {
				return rows, false
			}
		}
		time.Sleep(safeguardWaitGap)
	}
}

// missingCloudEnv names the credentials this machine cannot report without, and NEVER their values. 7.5
// makes a bridge refused for a missing AGENTHUB_URL, AGENTHUB_TOKEN or AGENTHUB_CONNECTOR_TOKEN a FAILED `up`, so the check runs
// before agent is started rather than surfacing as a bridge that died at once.
var missingCloudEnv = func() []string {
	var missing []string
	for _, name := range []string{"AGENTHUB_URL", "AGENTHUB_TOKEN", "AGENTHUB_CONNECTOR_TOKEN"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// refuseMissingCloudEnv is the one message for that refusal. It names the variable and never prints its
// value: the value is a credential, and a failure message is the last place it belongs.
func refuseMissingCloudEnv(verb string, missing []string, stderr io.Writer) int {
	fmt.Fprintf(stderr,
		"4genteam %s: %s is not set, and this machine cannot report without it - set it in the environment "+
			"or in the client's env file (the value is never printed).\n",
		verb, strings.Join(missing, " and "))
	return clientcmd.ExitUnavailable
}

// reportDown prints the same lines 7.5 requires of `doctor` for every row that is not running.
func reportDown(rows []clientservices.Health, stderr io.Writer) {
	for _, row := range clientservices.NotRunning(rows) {
		fmt.Fprintln(stderr, clientservices.StateLine(row))
	}
}
