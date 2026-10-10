// Package clientlifecycle is the client's own lifecycle: the verbs that start, stop and inspect the
// local stack (up, compact, compact-run, stop, status, log, ui), ported from cli.py and compact.py.
//
// The supervisor LOOP is ported rather than shelled out to: the Python's `compact` started
// `python -m agenthub_client.compact`, and this client starts its own `compact-run`, so the two halves
// are one binary and the process pattern `status` matches is this binary's own argv.
//
// Every verb that takes a rig REQUIRES it. The Python defaulted to a rig of the owner's own team
// (DEFAULT_RIG); a default that names somebody's team is not one a client may ship, so the name is an
// argument and its absence is a usage error that says so.
package clientlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/seatlog"
)

const (
	// ExitStartFailed is a start that did not survive: the verb ran and the process it started is dead.
	// It shares the number 3 with clientcmd.ExitUnavailable but answers a different question, so it is
	// named here rather than borrowed from the contract package.
	ExitStartFailed = 3
)

// SupervisorStartGrace is how long a started supervisor must survive before the start is called a
// success: long enough for an argument error to end the child, short enough that the verb stays
// snappy. It is a variable so the suite need not wait it out.
var SupervisorStartGrace = 1500 * time.Millisecond

const (
	upUsage         = "usage: 4genteam up RIG"
	compactUsage    = "usage: 4genteam compact RIG"
	stopUsage       = "usage: 4genteam stop RIG"
	statusUsage     = "usage: 4genteam status RIG"
	logUsage        = "usage: 4genteam log RIG"
	uiUsage         = "usage: 4genteam ui"
	compactRunUsage = "usage: 4genteam compact-run --rig RIG [--quiet SECONDS] [--every SECONDS] [--once] [--state-root DIR] [--forcecompact PATH]"
)

// ChildError carries a started child's exit status, so a start that failed can be named without
// depending on exec's own error shape - the suite's fake children have to report a status too.
type ChildError struct{ Status int }

func (e *ChildError) Error() string { return fmt.Sprintf("exit status %d", e.Status) }

// exitStatus is the code a finished child ended with: exec's own status when it has one, a ChildError's
// when the caller supplied it, and 1 otherwise.
func exitStatus(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	var child *ChildError
	if errors.As(err, &child) {
		return child.Status
	}
	return 1
}

// Commands is what the dispatcher registers from this package: one command per top-level verb, because
// these verbs are not a group - the Python dispatched each from main() directly.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{
		upCommand{},
		compactCommand{},
		stopCommand{},
		statusCommand{},
		logCommand{},
		uiCommand{},
		compactRunCommand{},
	}
}

func wantsHelp(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help")
}

func printHelp(out io.Writer, usage, description string) {
	fmt.Fprintf(out, "%s\n\n%s\n", usage, description)
}

func usageFor(verb string) string {
	switch verb {
	case "up":
		return upUsage
	case "compact":
		return compactUsage
	case "stop":
		return stopUsage
	case "status":
		return statusUsage
	case "log":
		return logUsage
	}
	return ""
}

// requireRig is the required-rig rule in one place: there is no default rig to fall back on, so an
// omitted name is a usage error naming the verb and the argument.
func requireRig(verb string, args []string, stderr io.Writer) (string, bool) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintf(stderr, "4genteam %s: a rig name is required - there is no default rig. %s\n", verb, usageFor(verb))
		return "", false
	}
	return args[0], true
}

// The steps `up` runs, in order. Each is a variable so a test can assert the order without a daemon, a
// rig, herdr or a UI: the Python test replaces the same functions.
var (
	ensureDaemon       = ensureDaemonImpl
	ensureRig          = ensureRigImpl
	ensureForcecompact = ensureForcecompactImpl
	startBridge        = startBridgeImpl
	openUI             = openUIImpl
	runWatchView       = runWatchViewImpl
	runHerdr           = runHerdrImpl
)

type upCommand struct{}

func (upCommand) Name() string { return "up" }
func (upCommand) Summary() string {
	return "start the local stack for a rig: daemon, seats, bridge, supervisor, UI"
}
func (upCommand) NeedsRig() bool { return true }

func (upCommand) Run(_ context.Context, rig *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		printHelp(stdout, upUsage,
			"OpenRig daemon, the rig's seats, the status bridge, the compaction supervisor and the OpenRig UI, in that order.")
		return clientcmd.ExitOK
	}
	name, ok := requireRig("up", args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	return runUp(rig, name, stdout, stderr)
}

func runUp(rig *clientcmd.Rig, name string, stdout, stderr io.Writer) int {
	if err := ensureDaemon(rig, stderr); err != nil {
		fmt.Fprintf(stderr, "4genteam: %v\n", err)
		return clientcmd.ExitRemote
	}
	ensureRig(rig, name, stderr)
	ensureForcecompact(stderr)
	startBridge(stdout, stderr)
	if !StartSupervisor(name, stdout, stderr) {
		fmt.Fprintln(stderr, "4genteam: continuing without a running compaction supervisor (see above)")
	}
	runWatchView(name, stderr)
	openUI(rig, stderr)
	if os.Getenv("HERDR_ENV") == "" {
		return runHerdr(stdout, stderr)
	}
	return clientcmd.ExitOK
}

func ensureDaemonImpl(rig *clientcmd.Rig, stderr io.Writer) error {
	if _, err := rig.Run(context.Background(), "daemon", "status"); err == nil {
		return nil
	}
	if _, err := rig.Run(context.Background(), "daemon", "start"); err != nil {
		return errors.New("the OpenRig daemon did not start (see: rig daemon logs)")
	}
	return nil
}

func ensureRigImpl(rig *clientcmd.Rig, name string, stderr io.Writer) {
	out, err := rig.Run(context.Background(), "ps", "--nodes", "--rig", name, "--json")
	if err == nil {
		var nodes []map[string]any
		if json.Unmarshal([]byte(out), &nodes) == nil && len(nodes) > 0 {
			allRunning := true
			for _, node := range nodes {
				if node["sessionStatus"] != "running" {
					allRunning = false
					break
				}
			}
			if allRunning {
				return
			}
		}
	}
	if _, err := rig.Run(context.Background(), "up", name, "--existing"); err != nil {
		fmt.Fprintf(stderr, "4genteam: could not restore rig %s (see: rig up %s --existing)\n", name, name)
	}
}

func ensureForcecompactImpl(stderr io.Writer) {
	helper := ForcecompactPath()
	if helper == "" {
		fmt.Fprintln(stderr,
			"4genteam: the forcecompact helper is not on PATH (set 4GENTEAM_FORCECOMPACT to its path): "+
				"an omp seat past the hard limit cannot be force-compacted")
		return
	}
	out, err := exec.Command("getcap", helper).Output()
	if err != nil || strings.Contains(string(out), "cap_sys_ptrace") {
		return
	}
	fmt.Fprintf(stderr, "4genteam: run once: sudo setcap cap_sys_ptrace+ep %s\n", helper)
}

func startBridgeImpl(stdout, stderr io.Writer) {
	if os.Getenv("AGENTHUB_URL") == "" || os.Getenv("AGENTHUB_TOKEN") == "" {
		fmt.Fprintln(stderr, "4genteam: AGENTHUB_URL and AGENTHUB_TOKEN must be set (in the environment or the client's env file)")
		return
	}
	stopPattern(BridgePattern)
	logPath, err := BridgeLogPath()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: %v\n", err)
		return
	}
	child, err := spawnChild([]string{"bridge", "run"}, logPath)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: could not start the bridge: %v\n", err)
		return
	}
	survived, err := awaitStart(child)
	if survived {
		fmt.Fprintf(stdout, "bridge: pid %d, log %s\n", child.Pid(), logPath)
		return
	}
	fmt.Fprintf(stderr, "4genteam: the bridge exited immediately (status %d); see %s\n", exitStatus(err), logPath)
}

func openUIImpl(rig *clientcmd.Rig, stderr io.Writer) {
	if _, err := rig.Run(context.Background(), "ui", "open"); err != nil {
		fmt.Fprintln(stderr, "4genteam: could not open the OpenRig UI (see: rig daemon status)")
	}
}

func runWatchViewImpl(name string, stderr io.Writer) {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: cannot find this binary to start the watch view: %v\n", err)
		return
	}
	// This same binary, with the watch arguments - the Python called its own module here. The exit code
	// is deliberately ignored: `up`'s job is the stack, and the watch view's failure was never `up`'s.
	cmd := exec.Command(self, "watch", "--rig", name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	_ = cmd.Run()
}

func runHerdrImpl(stdout, stderr io.Writer) int {
	path, err := exec.LookPath("herdr")
	if err != nil {
		fmt.Fprintln(stderr, "4genteam up: herdr is not on PATH; the watch view needs it (set HERDR_ENV to skip this step)")
		return clientcmd.ExitRemote
	}
	cmd := exec.Command(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return exitStatus(err)
	}
	return clientcmd.ExitOK
}

type compactCommand struct{}

func (compactCommand) Name() string { return "compact" }
func (compactCommand) Summary() string {
	return "(re)start the compaction supervisor for a rig, detached"
}
func (compactCommand) NeedsRig() bool { return false }

func (compactCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		printHelp(stdout, compactUsage, "(Re)start the compaction supervisor, detached. A start whose child dies at once returns non-zero.")
		return clientcmd.ExitOK
	}
	name, ok := requireRig("compact", args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	if !StartSupervisor(name, stdout, stderr) {
		return ExitStartFailed
	}
	return clientcmd.ExitOK
}

type stopCommand struct{}

func (stopCommand) Name() string    { return "stop" }
func (stopCommand) Summary() string { return "stop the compaction supervisor and the status bridge" }
func (stopCommand) NeedsRig() bool  { return false }

func (stopCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		printHelp(stdout, stopUsage, "Stop the compaction supervisor and the status bridge for a rig.")
		return clientcmd.ExitOK
	}
	name, ok := requireRig("stop", args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	StopSupervisor(name, false, stdout, stderr)
	stopPattern(BridgePattern)
	return clientcmd.ExitOK
}

type statusCommand struct{}

func (statusCommand) Name() string    { return "status" }
func (statusCommand) Summary() string { return "the supervisor's process and the last log lines" }
func (statusCommand) NeedsRig() bool  { return false }

func (statusCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		printHelp(stdout, statusUsage, "The compaction supervisor's process, or 'not running', and the last five lines of its log.")
		return clientcmd.ExitOK
	}
	name, ok := requireRig("status", args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	SupervisorStatus(name, stdout, stderr)
	return clientcmd.ExitOK
}

type logCommand struct{}

func (logCommand) Name() string    { return "log" }
func (logCommand) Summary() string { return "follow the supervisor's log" }
func (logCommand) NeedsRig() bool  { return false }

func (logCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		printHelp(stdout, logUsage, "Follow the compaction supervisor's log for a rig.")
		return clientcmd.ExitOK
	}
	name, ok := requireRig("log", args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	logPath, err := LogPath(name)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam log: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	tail, err := exec.LookPath("tail")
	if err != nil {
		fmt.Fprintf(stderr, "4genteam log: tail is not on PATH, and this verb follows a log with it\n")
		return clientcmd.ExitUnavailable
	}
	cmd := exec.Command(tail, "-f", logPath)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return exitStatus(err)
	}
	return clientcmd.ExitOK
}

type uiCommand struct{}

func (uiCommand) Name() string    { return "ui" }
func (uiCommand) Summary() string { return "open the OpenRig web UI" }
func (uiCommand) NeedsRig() bool  { return true }

func (uiCommand) Run(_ context.Context, rig *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		printHelp(stdout, uiUsage, "Open the OpenRig web UI.")
		return clientcmd.ExitOK
	}
	openUI(rig, stderr)
	return clientcmd.ExitOK
}

type compactRunCommand struct{}

func (compactRunCommand) Name() string { return "compact-run" }
func (compactRunCommand) Summary() string {
	return "the compaction supervisor loop, in the foreground (what compact starts)"
}
func (compactRunCommand) NeedsRig() bool { return false }

func (compactRunCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		printHelp(stdout, compactRunUsage,
			"The compaction supervisor loop in the foreground. Every pass reads each seat's newest session log and\n"+
				"applies three limits: past COMPACT_LIMIT_TOKENS the seat is told to find its own safe point, past\n"+
				"COMPACT_WARN_TOKENS it is compacted now without waiting for quiet, and past COMPACT_HARD_TOKENS its\n"+
				"running turn is aborted and compaction forced. Each limit is read from the environment, and a BLANK\n"+
				"value means its default (150000, 250000, 300000).")
		return clientcmd.ExitOK
	}
	options, ok := ParseCompactRunOptions(args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	if err := seatlog.ApplyLimits(); err != nil {
		fmt.Fprintf(stderr, "4genteam compact-run: %v\n", err)
		return clientcmd.ExitUsage
	}
	supervisor := New(options.Rig, options.Quiet, options.StateRoot, options.Forcecompact, stdout)
	supervisor.Log(fmt.Sprintf("supervising %s: limit %s, quiet %ds",
		options.Rig, seatlog.Kilo(seatlog.CompactLimit), options.Quiet))
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	for {
		supervisor.Step()
		if options.Once {
			return clientcmd.ExitOK
		}
		select {
		case <-interrupted:
			return clientcmd.ExitOK
		case <-time.After(time.Duration(options.Every) * time.Second):
		}
	}
}

// CompactRunOptions is one `compact-run`'s arguments.
type CompactRunOptions struct {
	Rig          string
	Quiet        int
	Every        int
	Once         bool
	StateRoot    string
	Forcecompact string
}

// ParseCompactRunOptions reads compact-run's arguments. It is separate from Run so the rule that has
// bitten before - a BLANK COMPACT_QUIET_SECONDS is the default, not a parse failure that takes the
// verb down before it supervises anything - is testable on its own, which is exactly what the Python
// test for it asserts.
func ParseCompactRunOptions(args []string, stderr io.Writer) (CompactRunOptions, bool) {
	var options CompactRunOptions
	quiet, err := clientenv.Limit("COMPACT_QUIET_SECONDS", 15)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam compact-run: %v\n", err)
		return options, false
	}
	flags := flag.NewFlagSet("compact-run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rig := flags.String("rig", "", "the rig to supervise (required)")
	quietFlag := flags.Int("quiet", quiet, "seconds of log silence that mean the job is finished")
	every := flags.Int("every", 10, "seconds between passes")
	once := flags.Bool("once", false, "run one pass and exit")
	stateRoot := flags.String("state-root", clientenv.DefaultStateRoot(), "where OpenRig keeps its per-seat state")
	forcecompact := flags.String("forcecompact", ForcecompactPath(), "the forcecompact helper, when this machine has one")
	if err := flags.Parse(args); err != nil {
		return options, false
	}
	if strings.TrimSpace(*rig) == "" {
		fmt.Fprintf(stderr, "4genteam compact-run: --rig is required - there is no default rig. %s\n", compactRunUsage)
		return options, false
	}
	options = CompactRunOptions{
		Rig:          *rig,
		Quiet:        *quietFlag,
		Every:        *every,
		Once:         *once,
		StateRoot:    *stateRoot,
		Forcecompact: *forcecompact,
	}
	return options, true
}
