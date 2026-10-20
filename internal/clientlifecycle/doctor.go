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
//
// It answers (rows, error), and an error means the probe could not run: no row was decided and the verb
// refuses, naming the probe (7.12). A nil error carries every row the report covers.
var rowsFor = safeguardRows

// SafeguardRows is the same computation for a caller outside this package - `continue` reports the rows a
// restore did not bring up - so the second reader cannot drift from `doctor` and `status`.
func SafeguardRows(rig, root string) ([]clientservices.Health, error) { return rowsFor(rig, root) }

// safeguardRows is the ONE computation doctor, `status --json`, `compact`, `agent status` and `up`'s check
// share.
//
// A named rig is reported on its own (with the two machine-scoped rows); no rig means EVERY rig in
// agent.json's list, because that list is what "meant to run" means (7.9). A machine whose agent has never
// run has an empty list and still gets the bridge and agent rows, which is the point: agent's own absence
// is the loudest row there is.
func safeguardRows(rig, root string) ([]clientservices.Health, error) {
	// A REPORT SURFACE MAY GO ON WITHOUT THE LIST. The rigs list decides WHICH rooms get rows; a failed read
	// leaves it empty exactly as a machine whose agent never ran does, and this verb neither writes nor acts,
	// so the rows for the two machine-scoped safeguards still print. `agent status` says in words when the
	// read failed; naming it here needs an error shape in the report seam, which is its own unit.
	//
	// THE PROCESS PROBE IS THE OTHER CASE: a pgrep that could not run decides NO row, so its error is the
	// caller's and is handed on untouched (7.12).
	state, _ := clientservices.ReadAgentState()
	rigs := reportedRigs(state.Rigs, rig)
	return clientservices.Verify(context.Background(), clientservices.Env{
		Root:       root,
		Rigs:       rigs,
		Supervised: state.Safeguards,
	})
}

// reportedRigs is 7.2's list of the rigs a report covers: the named rig alone, or every rig agent.json
// lists, because that list is what "meant to run" means (7.9). The safeguard rows and the orphan row cover
// the same list, so they cannot disagree about which rooms exist.
func reportedRigs(rigs []string, rig string) []string {
	if rig != "" {
		return []string{rig}
	}
	return rigs
}

// runDoctor prints the rows and decides the exit code. `--json` prints the rows instead of the lines, and
// the tail of alerts.log is a reading surface for the human output only: the JSON is the rows 7.5 says it
// is.
// doctorJSONRow is one row of `doctor --json`: 7.2's row, plus the reasons the human report prints beside
// it when it has any. The reasons ride IN the row because --json is the surface a monitor reads, and a row
// that says `orphan_handoffs` without naming the qitems is a row a reader cannot act on. The fields are
// 7.2's exactly: a row with no reasons prints byte for byte what it printed before, the key omitted.
type doctorJSONRow struct {
	clientservices.Health
	Reasons []string `json:"reasons,omitempty"`
}

// doctorJSONRows pairs every row with the lines the human report prints beside it, from the wrapper that
// carries them: ONE rule for the JSON and the lines, so the two surfaces cannot disagree about a row.
func doctorJSONRows(rows []clientservices.Health, worktreeRows []worktreesRow, cloneRows []cloneRow, hooksRows []hooksRow,
	seatRows []seatSkillsRow, orphanRows []orphanRow, identityRows []identityRow) []doctorJSONRow {
	out := make([]doctorJSONRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, doctorJSONRow{Health: row,
			Reasons: rowReasons(row, worktreeRows, cloneRows, hooksRows, seatRows, orphanRows, identityRows)})
	}
	return out
}

// rowReasons finds one row's reasons: the wrapper is matched on (safeguard, rig), the pair the report
// itself matches a row by.
func rowReasons(row clientservices.Health, worktreeRows []worktreesRow, cloneRows []cloneRow, hooksRows []hooksRow,
	seatRows []seatSkillsRow, orphanRows []orphanRow, identityRows []identityRow) []string {
	for _, worktree := range worktreeRows {
		if worktree.Row.Safeguard == row.Safeguard && worktree.Row.Rig == row.Rig {
			return worktreeLines(worktree)
		}
	}
	for _, clone := range cloneRows {
		if clone.Row.Safeguard == row.Safeguard && clone.Row.Rig == row.Rig {
			return cloneLines(clone)
		}
	}
	for _, hooks := range hooksRows {
		if hooks.Row.Safeguard == row.Safeguard && hooks.Row.Rig == row.Rig {
			return hooksLines(hooks)
		}
	}
	for _, seat := range seatRows {
		if seat.Row.Safeguard == row.Safeguard && seat.Row.Rig == row.Rig {
			return seatLines(seat)
		}
	}
	for _, orphan := range orphanRows {
		if orphan.Row.Safeguard == row.Safeguard && orphan.Row.Rig == row.Rig {
			return orphanLines(orphan)
		}
	}
	for _, identity := range identityRows {
		if identity.Row.Safeguard == row.Safeguard && identity.Row.Rig == row.Rig {
			return identityLines(identity)
		}
	}
	return nil
}

func runDoctor(rig, root string, asJSON bool, stdout, stderr io.Writer) int {
	// THE REFUSAL COMES FIRST (7.12): a probe that could not run decides no row, so not one row, reason or
	// note is printed - a report that cannot say "running" must not be readable as "stopped".
	rows, err := rowsFor(rig, root)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam doctor: no safeguard row was decided: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	// The worktrees row (the ruling's Check): where each seat's own worktree is, and whether it is the
	// one this client derives. It reads the same derivation `up`'s worktrees step creates, so the two
	// cannot disagree about a path.
	worktreeRows := worktreesRowFor(rig, root)
	for _, worktreeRow := range worktreeRows {
		rows = append(rows, worktreeRow.Row)
	}
	// The two MACHINE rows (4.1's third failure and 4.4's fifth risk): the changes sitting in the shared
	// checkout that belong to no seat, and whether the git guard is present in the hooks folder git actually
	// runs. Both are read from the checkout `up` and the worktrees row already resolve, and neither carries a
	// rig, so they are appended whatever rig the caller named.
	cloneRows := cloneRowFor(rig)
	for _, clone := range cloneRows {
		rows = append(rows, clone.Row)
		if clone.Note != "" {
			fmt.Fprintf(stderr, "4genteam doctor: %s\n", clone.Note)
		}
	}
	hooksRows := hooksRowFor(rig)
	for _, hooks := range hooksRows {
		rows = append(rows, hooks.Row)
		if hooks.Row.State == clientservices.GuardAbsent {
			for _, line := range hooksReasonLines(hooks) {
				fmt.Fprintln(stderr, line)
			}
		}
	}
	// The seats row (the ruling's one check): `running`, or `skills_stale` when a seat loaded something
	// other than the set the runtime installed for it. It is the SAME function `up` runs, so the two cannot
	// disagree about a seat.
	seatRows := seatsRowFor(rig, root)
	for _, seatRow := range seatRows {
		rows = append(rows, seatRow.Row)
	}
	orphanRows := orphanRowsFor(rig)
	for _, orphan := range orphanRows {
		rows = append(rows, orphan.Row)
		if orphan.Err != nil {
			fmt.Fprintf(stderr, "4genteam doctor: %s could not be read: %v\n", clientservices.OrphanHandoffs, orphan.Err)
		}
	}
	// The identity row (ruling e2948011, 4.6): the rooms whose senders the daemon can name, and the nodes it
	// cannot, whose messages arrive as <unknown sender> with nothing in the text to say so.
	identityRows := identityRowFor(rig)
	for _, identity := range identityRows {
		rows = append(rows, identity.Row)
	}
	if asJSON {
		body, err := json.MarshalIndent(doctorJSONRows(rows, worktreeRows, cloneRows, hooksRows, seatRows, orphanRows, identityRows), "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam doctor: cannot print the rows: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintln(stdout, string(body))
	} else {
		for _, row := range rows {
			fmt.Fprintln(stdout, clientservices.StateLine(row))
		}
		for _, orphan := range orphanRows {
			for _, line := range orphanLines(orphan) {
				fmt.Fprintln(stdout, line)
			}
		}
		// The worktrees row's reasons and details ride beside its row the same way.
		for _, worktreeRow := range worktreeRows {
			for _, line := range worktreeLines(worktreeRow) {
				fmt.Fprintln(stdout, line)
			}
		}
		// So do the two machine rows': the paths a dirty shared checkout holds are the actionable part of
		// the clone row, and the folder git uses is the actionable part of the hooks row.
		for _, clone := range cloneRows {
			for _, line := range cloneLines(clone) {
				fmt.Fprintln(stdout, line)
			}
		}
		for _, hooks := range hooksRows {
			for _, line := range hooksLines(hooks) {
				fmt.Fprintln(stdout, line)
			}
		}
		for _, seatRow := range seatRows {
			for _, line := range seatLines(seatRow) {
				fmt.Fprintln(stdout, line)
			}
		}
		for _, identity := range identityRows {
			for _, line := range identityLines(identity) {
				fmt.Fprintln(stdout, line)
			}
		}
		for _, line := range clientservices.TailAlerts(root) {
			fmt.Fprintln(stdout, line)
		}
	}
	// The inputs the check could not read, on stderr, whether or not any row is down.
	for _, line := range seatNoteLines(seatRows) {
		fmt.Fprintln(stderr, line)
	}
	down := clientservices.NotRunning(rows)
	if len(down) == 0 {
		return clientcmd.ExitOK
	}
	// The SAME line the report prints, on stderr, so a caller that only reads stderr still learns which
	// safeguard is down, in which room, and for how long. An orphan row names its qitems there too: the
	// refusal is only actionable if it says which handoff is stranded.
	for _, row := range down {
		fmt.Fprintln(stderr, clientservices.StateLine(row))
		for _, orphan := range orphanRows {
			if orphan.Row.Safeguard == row.Safeguard && orphan.Row.Rig == row.Rig {
				for _, line := range orphanLines(orphan) {
					fmt.Fprintln(stderr, line)
				}
			}
		}
		// The worktrees row's own reasons, the same way: the paths that are wrong are why the row is
		// down, and an input this client could not read is reported whether or not it is.
		for _, worktreeRow := range worktreeRows {
			if worktreeRow.Row.Rig != row.Rig {
				continue
			}
			for _, line := range worktreeReasonLines(worktreeRow) {
				fmt.Fprintln(stderr, line)
			}
		}
		// And the two machine rows' reasons: a dirty shared checkout's paths, and the hooks folder with no
		// guard in it, are why those rows are down.
		for _, clone := range cloneRows {
			if clone.Row.Safeguard == row.Safeguard {
				for _, line := range cloneReasonLines(clone) {
					fmt.Fprintln(stderr, line)
				}
			}
		}
		for _, hooks := range hooksRows {
			if hooks.Row.Safeguard == row.Safeguard {
				for _, line := range hooksReasonLines(hooks) {
					fmt.Fprintln(stderr, line)
				}
			}
		}
		// The seats row's reasons, on stderr: the differences WHEN THE ROW IS DOWN (they are why it is down,
		// and a refusal that does not name the seat and the code is not actionable), and the inputs the check
		// could not read whether or not it is - a reader must be able to tell "the seats are right" from "the
		// seats could not be looked at".
		for _, seatRow := range seatRows {
			if seatRow.Row.Rig != row.Rig || seatRow.Row.Safeguard != row.Safeguard {
				continue
			}
			for _, line := range seatReasonLines(seatRow) {
				fmt.Fprintln(stderr, line)
			}
		}
		// An identity row that is down is only actionable with the node that cannot be named.
		for _, identity := range identityRows {
			if identity.Row.Safeguard == row.Safeguard && identity.Row.Rig == row.Rig {
				for _, line := range identityReasonLines(identity) {
					fmt.Fprintln(stderr, line)
				}
			}
		}
	}
	return clientcmd.ExitUnavailable
}

// orphanLines is one orphan row's names, in report order: the row line says the state, these say which
// qitems are stranded and where they were addressed.
func orphanLines(row orphanRow) []string {
	lines := make([]string, 0, len(row.Orphans))
	for _, orphan := range row.Orphans {
		lines = append(lines, orphanLine(orphan))
	}
	return lines
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
// `doctor`. A probe error is answered as itself: nothing was decided, so there is no report to wait on and
// `up` refuses rather than calling the safeguards down (7.12).
func waitForSafeguardsImpl(rig, root string) ([]clientservices.Health, bool, error) {
	started := time.Now()
	for {
		rows, err := rowsFor(rig, root)
		if err != nil {
			return nil, false, err
		}
		if clientservices.AllRunning(rows) {
			return rows, true, nil
		}
		elapsed := time.Since(started)
		for _, row := range clientservices.NotRunning(rows) {
			if elapsed >= clientservices.DefaultSilentAfter(row.Safeguard) {
				return rows, false, nil
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
