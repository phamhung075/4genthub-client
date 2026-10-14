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
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
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

// DaemonReadyWindow is how long a freshly started runtime daemon has to answer on its socket, and
// RoomStartWindow how long a room's seats have to reach Running after StartRoom returns. Both are
// variables for the same reason SupervisorStartGrace is: the suite must not have to wait them out.
var (
	DaemonReadyWindow = 10 * time.Second
	RoomStartWindow   = 20 * time.Second
)

const (
	upUsage         = "usage: 4genteam up RIG --team DIR [--state-dir DIR] [--room-only] " + clientoffpeak.ModeFlagUsage
	compactUsage    = "usage: 4genteam compact RIG " + clientoffpeak.ModeFlagUsage
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
		doctorCommand{},
		rigdCommand{},
	}
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
	// A flag this verb does not take is refused rather than read as a rig name: `up --some-flag` used to
	// mean a room called "--some-flag", whose failure arrived much later and much less clearly.
	if strings.HasPrefix(args[0], "-") && args[0] != "-" {
		fmt.Fprintf(stderr, "4genteam %s: unknown argument %s. %s\n", verb, args[0], usageFor(verb))
		return "", false
	}
	// And a SECOND argument is refused too: these verbs take one rig name, so an argument that was
	// silently dropped is a caller believing something happened that did not.
	if len(args) > 1 {
		fmt.Fprintf(stderr, "4genteam %s: unexpected argument %s - this verb takes one rig name. %s\n",
			verb, args[1], usageFor(verb))
		return "", false
	}
	return args[0], true
}

// The steps `up` runs, in order. Each is a variable so a test can assert the order without a daemon, a
// rig, herdr or a UI: the Python test replaces the same functions.
//
// startBridge IS GONE FROM THIS LIST, and that is 7.1's removed path: `up` starts rigd, and rigd starts
// the bridge. Two start paths for one safeguard would be two mechanisms.
// ensureDaemon returns the log file the runtime daemon writes its own output to, and ensureRig returns the
// room's verdict as this verb's exit code. Their signatures carry the state dir and the definition for one
// reason beyond the work: the acceptance run has to be able to say WHICH of the two answered.
var (
	ensureDaemon       = ensureDaemonImpl
	ensureRig          = ensureRigImpl
	ensureForcecompact = ensureForcecompactImpl
	recordRigFor       = recordRig
	startAuthority     = startRigd
	waitSafeguards     = waitForSafeguards
	openUI             = openUIImpl
	runWatchView       = runWatchViewImpl
	runHerdr           = runHerdrImpl
)

type upCommand struct{}

func (upCommand) Name() string { return "up" }
func (upCommand) Summary() string {
	return "start the local stack for a rig: daemon, seats, rigd (which starts the bridge and the supervisor), UI"
}

// NeedsRig is FALSE, and that is this cutover's point rather than an exemption: the room is named by the
// positional and its seats come from the LOCAL team definition, so starting them needs nothing resolved
// from OpenRig. Requiring one is what made the native start path depend on the CLI exec it replaces.
//
// The dispatcher therefore hands this verb a NIL rig, and the two places that still reach for one must
// take nil: the peak gate's fail-closed activity read already does, and openUIImpl resolves the tool
// itself now. A third that dereferenced it would panic on every `up`.
func (upCommand) NeedsRig() bool { return false }

func (upCommand) Run(_ context.Context, rig *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, upUsage,
			"The runtime daemon, this rig's room and its seats, and then the OpenRig-era safeguards: rigd,\n"+
				"which owns the compaction supervisor, the watchdog, the seat-message deliverer and the status\n"+
				"bridge, and finally the watch view and the UI.\n"+
				"The room's seats come from the LOCAL team definition: --team names the directory that holds\n"+
				"team.json, and there is no default, so starting a room needs no cloud round trip.\n"+
				"--room-only stops after the room: it starts the daemon, starts the room from the definition and\n"+
				"prints one line per seat. It EXCLUDES rigd, the bridge, the watch view, the UI and herdr, whose\n"+
				"failures are not this verb's verdict - and it must not be used to make a red go away.\n"+
				"The acceptance invocation, so a reader re-reads the signature instead of remembering it:\n"+
				"  4genteam up RIG --team DIR [--state-dir DIR] --room-only\n"+
				"WHAT IT EXITS WITH (design §2.5), counting only the room's own verdict:\n"+
				"  0  every seat of the room is running. A room that is already up is READ, not restarted: a blind\n"+
				"     start would cycle sessions of live agents.\n"+
				"  2  the daemon started but a seat did not - the seats that are not running are named, with the\n"+
				"     daemon's log path to read next\n"+
				"  3  the invocation is wrong, or the daemon will not take the definition it was handed\n"+
				"The state dir and the socket this run talks to are printed BEFORE the room is touched: with the\n"+
				"flags absent both are chosen from this machine's own environment, and the room's own lines are\n"+
				"read against that pair.\n"+
				"The DeepSeek peak gate is LOCAL (it reads the room's own node records) and runs BEFORE the seats\n"+
				"are started: with "+clientoffpeak.ModeEnv+" on (the default) a room whose seats run a deepseek/\n"+
				"model is refused at peak with exit 3, and "+clientoffpeak.FlagAllowPeak+" skips the check for\n"+
				"this run.")
		return clientcmd.ExitOK
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(args)
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(stderr)
	teamDir := flags.String("team", "", "the team definition directory that holds team.json (required)")
	stateDir := flags.String("state-dir", "", "where the runtime keeps its state: the flag, else $"+clientenv.StateRootEnv+", else this machine's own")
	roomOnly := flags.Bool("room-only", false, "start the daemon and the room, print a line per seat, and stop before rigd, the bridge, the watch view and the UI")
	flagArgs, positional := splitFlagsAndPositional(rest)
	for _, arg := range flagArgs {
		if name := flagName(arg); name != "" && !upTakesFlag(name) {
			fmt.Fprintf(stderr, "4genteam up: unknown argument %s. %s\n", arg, upUsage)
			return clientcmd.ExitRoomRefused
		}
	}
	if err := flags.Parse(flagArgs); err != nil {
		return clientcmd.ExitRoomRefused
	}
	name, ok := requireRig("up", positional, stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	if strings.TrimSpace(*teamDir) == "" {
		fmt.Fprintf(stderr, "4genteam up: --team DIR is required - the room's seats are read from the team definition it holds, and there is no default. %s\n", upUsage)
		return clientcmd.ExitRoomRefused
	}
	root, err := clientenv.ResolveStateRoot(*stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	// THE PAIR IS PRINTED, NOT RESOLVED SILENTLY. The state dir decides which rooms exist and the socket
	// decides which daemon is asked, and with the flags absent both come from this machine's own
	// environment - so the run SAYS which daemon it is talking to instead of leaving a reader to guess, and
	// the room's own lines below are read against it (design §1).
	fmt.Fprintf(stdout, "state dir: %s\nsocket: %s\n", root, clientruntime.DefaultSocketPath())
	return runUp(rig, name, *teamDir, root, allowPeak, *roomOnly, stdout, stderr)
}

// splitFlagsAndPositional separates this verb's flags from its one positional, and puts the positionals
// LAST: the flag package STOPS at the first bare word, so `up RIG --team DIR` - the order the usage line
// documents, and the one a reader writes - would otherwise parse no flags at all and report the room name's
// neighbour as an unexpected argument. clientpolicy's own parser carries this same warning for this same
// reason, which is exactly why it is spelled out here rather than rediscovered.
func splitFlagsAndPositional(args []string) (flagArgs, positional []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !isFlag(arg) {
			positional = append(positional, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		// A flag that takes a value takes its VALUE with it, so `--team DIR` is never read as a positional
		// named DIR. `--team=DIR` carries its own value and needs no lookahead.
		if upFlagTakesValue(flagName(arg)) && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return flagArgs, positional
}

func isFlag(arg string) bool { return strings.HasPrefix(arg, "-") && arg != "-" }

// flagName is a flag's name without its dashes and without any =value; "" for something that is not a flag.
func flagName(arg string) string {
	if !isFlag(arg) {
		return ""
	}
	name := strings.TrimLeft(arg, "-")
	if at := strings.Index(name, "="); at >= 0 {
		name = name[:at]
	}
	return name
}

func upFlagTakesValue(name string) bool { return name == "team" || name == "state-dir" }

// upTakesFlag is this verb's own flag list, so an argument it does not take is refused HERE with the
// argument NAMED: the flag package's own message says `-any-time`, and the shape this replaces was a verb
// that quietly ignored the extra argument.
func upTakesFlag(name string) bool {
	switch name {
	case "team", "state-dir", "room-only", "h", "help":
		return true
	}
	return false
}

func runUp(rig *clientcmd.Rig, name, teamDir, stateDir string, allowPeak, roomOnly bool, stdout, stderr io.Writer) int {
	// The definition is read FIRST: a --team typo is a caller error, and it must be reported before a daemon
	// is started or a seat is touched. The room name must be the room the file defines, which is the same
	// rule clientpolicy applies to its own --rig.
	// A bad invocation is 3 for this verb, not the CLI-wide ExitUsage (2): 2 is what a PARTIAL START is here
	// (design §2.5), and one number cannot mean both things to a caller reading an exit code.
	spec, err := clientteam.LoadRoomSpec(teamDir)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	if spec.Room != name {
		fmt.Fprintf(stderr, "4genteam up: %s defines the room %q, not %q - --rig must name the room the definition defines. %s\n",
			teamDir, spec.Room, name, upUsage)
		return clientcmd.ExitRoomRefused
	}
	daemonLog, err := ensureDaemon(stateDir, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: %v\n", err)
		return clientcmd.ExitRemote
	}
	// THE GATE RUNS BEFORE THE RESTORE, and after the daemon only because the room's models are read from
	// the daemon. So a refusal starts the daemon and nothing else: no seat is resumed, which is the step
	// that would have started DeepSeek work at the peak rate.
	if code := PeakGate(rig, name, "up", allowPeak, stderr); code != clientcmd.ExitOK {
		return code
	}
	if code := ensureRig(name, spec, daemonLog, stdout, stderr); code != clientcmd.ExitOK {
		return code
	}
	// --room-only is an INSTRUMENT: it ends here, with the room's own verdict. The guard is deliberate - a
	// red that comes from the bridge, the UI or herdr is not evidence about the room.
	if roomOnly {
		return clientcmd.ExitOK
	}
	ensureForcecompact(stderr)

	// 7.5: a bridge refused for a missing AGENTHUB_URL or AGENTHUB_TOKEN is a FAILED `up`. The check runs
	// HERE, before rigd is started, because the alternative is a bridge that dies at once and a message
	// about a process instead of about the variable that is missing. The value is never printed.
	if missing := missingCloudEnv(); len(missing) > 0 {
		return refuseMissingCloudEnv("up", missing, stderr)
	}

	// The room is recorded as one that is meant to run (7.1), and then the single restart authority is
	// started. From here on rigd owns the compaction supervisor, the watchdog and the bridge; `up` starts
	// nothing else for them.
	if !recordRigFor(name, stderr) {
		return clientcmd.ExitUnavailable
	}
	if !startAuthority(stdout, stderr) {
		return ExitStartFailed
	}

	// 7.5: the SAME check `doctor` makes, run after rigd's start grace and BEFORE the watch view, the UI
	// and herdr. A safeguard that did not come up is named here, with the line a reader will see in
	// `doctor`, rather than discovered hours later on a quiet rig.
	rows, ok := waitSafeguards(name)
	if !ok {
		reportDown(rows, stderr)
		return clientcmd.ExitUnavailable
	}

	runWatchView(name, stderr)
	openUI(rig, stderr)
	if os.Getenv("HERDR_ENV") == "" {
		return runHerdr(stdout, stderr)
	}
	return clientcmd.ExitOK
}

// ensureDaemonImpl makes the runtime daemon reachable for a room whose state lives under stateDir, and
// returns the log file the daemon's own output goes to.
//
// The client starts the daemon itself rather than calling clientruntime's EnsureDaemon: that helper spawns
// `agenthub-runtime --socket PATH daemon` with NO --state-dir and both streams dropped, so the daemon would
// land on this machine's default state tree and a start that failed would have nothing to read. `up` that
// cannot name why a seat did not come up is the silence this client exists to remove.
func ensureDaemonImpl(stateDir string, stderr io.Writer) (string, error) {
	logPath := daemonLogPath(stateDir)
	client := clientruntime.NewClient("")
	if client.Reachable() {
		return logPath, nil
	}
	runtimeBin, err := runtimeBinary()
	if err != nil {
		return logPath, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return logPath, fmt.Errorf("cannot create the directory for the daemon's log: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return logPath, fmt.Errorf("cannot write the daemon's log at %s: %w", logPath, err)
	}
	defer logFile.Close()
	cmd := exec.Command(runtimeBin, "--state-dir", stateDir, "--socket", client.SocketPath, "daemon")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return logPath, fmt.Errorf("cannot start agenthub-runtime: %w", err)
	}
	deadline := time.Now().Add(DaemonReadyWindow)
	for time.Now().Before(deadline) {
		if client.Reachable() {
			return logPath, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return logPath, fmt.Errorf("agenthub-runtime did not answer on %s within %s - its own output is in %s",
		client.SocketPath, DaemonReadyWindow, logPath)
}

// daemonLogPath is where the runtime daemon's own output goes: beside the state it serves, so a partial room
// and the reason for it are read from one tree.
func daemonLogPath(stateDir string) string { return filepath.Join(stateDir, "daemon.log") }

// runtimeBinary is the runtime executable: PATH first, then the per-user install the OpenRig CLIs use.
func runtimeBinary() (string, error) {
	if found, err := exec.LookPath("agenthub-runtime"); err == nil {
		return found, nil
	}
	local := filepath.Join(os.Getenv("HOME"), ".local", "bin", "agenthub-runtime")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	return "", errors.New("agenthub-runtime is not on PATH or in ~/.local/bin, and a room cannot start without it")
}

// ensureRigImpl makes the room ready from the definition's own spec and reads it back until every seat is
// running or RoomStartWindow runs out.
//
// CHECK-THEN-START, and the reason is the runtime's own semantics: StartRoom kills and re-creates a session
// of that name when it finds one running (pty::launch_session), so a blind start CYCLES a room that is
// already up - live agents included - and every second `up` would move the started_at of a room that was
// fine. So the room is read first; what that reading says decides the start (seatStartPlan below).
//
// It returns the verb's verdict (design §2.5): ExitOK when every seat runs, ExitPartialStart (2) - naming
// the seats that are not running AND the daemon's log path - when the daemon started but a seat did not,
// and ExitRoomRefused (3) when the daemon will not take the definition it was handed.
func ensureRigImpl(name string, spec clientruntime.RoomSpec, daemonLog string, stdout, stderr io.Writer) int {
	client := clientruntime.NewClient("")
	reported, statusErr := client.GetRoomStatus(name)
	restart, sendSpec := seatStartPlan(reported, statusErr)
	seats := reported
	if sendSpec {
		started, err := client.StartRoom(spec)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam up: could not start room %s: %v\n", name, err)
			fmt.Fprintf(stderr, "4genteam up: the runtime daemon's own output is in %s\n", daemonLog)
			return clientcmd.ExitRoomRefused
		}
		seats = started
	}
	// One seat at a time, and only the ones the reading says are not running: RestartSeat is the per-seat
	// start (stop that seat if it runs, re-read the materialised spec, start it), so it cannot touch a
	// running sibling.
	for _, seat := range restart {
		if _, err := client.RestartSeat(name, seat); err != nil {
			fmt.Fprintf(stderr, "4genteam up: could not start %s/%s: %v\n", name, seat, err)
			fmt.Fprintf(stderr, "4genteam up: the runtime daemon's own output is in %s\n", daemonLog)
			return clientcmd.ExitPartialStart
		}
	}
	deadline := time.Now().Add(RoomStartWindow)
	for {
		seats = roomReadings(client, name, seats)
		if allSeatsRunning(seats) || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(seats) == 0 {
		fmt.Fprintf(stderr, "4genteam up: room %s reported no seats at all\n", name)
		fmt.Fprintf(stderr, "4genteam up: the runtime daemon's own output is in %s\n", daemonLog)
		return clientcmd.ExitPartialStart
	}
	var late []string
	for _, seat := range seats {
		// THE PROOF TRAVELS WITH THE VERDICT. `running` alone never said what it rested on - the agent's own
		// handshake, or a pane that outlived a grace - and the runtime publishes exactly that in `detail`. The
		// line used to drop the field on the floor, which is the honesty requirement this row was filed for:
		// the operator read a state name and had no evidence to weigh.
		line := fmt.Sprintf("%s/%s: %s", name, seat.Seat, seat.State)
		if seat.Detail != nil && strings.TrimSpace(*seat.Detail) != "" {
			line += " (" + strings.TrimSpace(*seat.Detail) + ")"
		}
		fmt.Fprintln(stdout, line)
		if !seatRunning(seat) {
			late = append(late, seat.Seat)
		}
	}
	if len(late) > 0 {
		fmt.Fprintf(stderr, "4genteam up: room %s is not up: %d of %d seat(s) are not running (%s)\n",
			name, len(late), len(seats), strings.Join(late, ", "))
		fmt.Fprintf(stderr, "4genteam up: the runtime daemon's own output is in %s\n", daemonLog)
		return clientcmd.ExitPartialStart
	}
	return clientcmd.ExitOK
}

// seatStartPlan is the check-then-start decision, pure so it can be pinned without a daemon.
//
// sendSpec is true when the WHOLE definition has to be sent: either the daemon does not know the room (the
// reading failed, or it reported no seats), or every seat it reports is stopped - in both cases nothing is
// running, so a whole-spec start has nothing to cycle and it is also the path that materialises a room
// whose spec.json does not exist yet.
//
// Otherwise the seats that are not running are returned and started one at a time. A room where every seat
// already runs returns neither: that is the second `up`, and it must change nothing.
func seatStartPlan(reported []clientruntime.SeatStatus, err error) (restart []string, sendSpec bool) {
	if err != nil || len(reported) == 0 {
		return nil, true
	}
	running := false
	for _, seat := range reported {
		if seatRunning(seat) {
			running = true
			continue
		}
		restart = append(restart, seat.Seat)
	}
	if !running {
		return nil, true
	}
	return restart, false
}

// roomReadings is the room's current seats. A reading that fails keeps the last one the runtime gave: the
// verdict must be about the seats, and an unreadable poll is not news that a seat died.
func roomReadings(client *clientruntime.Client, name string, fallback []clientruntime.SeatStatus) []clientruntime.SeatStatus {
	seats, err := client.GetRoomStatus(name)
	if err != nil || len(seats) == 0 {
		return fallback
	}
	return seats
}

func allSeatsRunning(seats []clientruntime.SeatStatus) bool {
	for _, seat := range seats {
		if !seatRunning(seat) {
			return false
		}
	}
	return len(seats) > 0
}

// seatRunning reads the runtime's state string case-insensitively: "Running" and "running" are the same
// fact, and the runtime's own enum is the only thing that decides it.
func seatRunning(seat clientruntime.SeatStatus) bool {
	return strings.EqualFold(strings.TrimSpace(seat.State), "running")
}

// rigNodes is the ONE reading of a rig's node records here, so the gate and the restore cannot decide
// from different things. A read that fails, or answers something that is not a list, gives NO nodes: the
// caller's next step fails loudly on its own, and an unreadable reading must never be read as "this room
// is safe".
//
// The clientseat package reads the SAME runtime call as roomNodes, with the OPPOSITE contract: there a failed read is
// an ERROR naming the rig, because `reseat` has to say why it will not restart a seat. The names differ on purpose -
// two same-named functions with opposite contracts is a trap whose only guard would be a commit message.
var rigNodes = func(rig *clientcmd.Rig, name string) []map[string]any {
	client := clientruntime.NewClient("")
	seats, err := client.GetRoomStatus(name)
	if err == nil && len(seats) > 0 {
		var nodes []map[string]any
		for _, seat := range seats {
			nodes = append(nodes, map[string]any{
				"rigName":       name,
				"logicalId":     fmt.Sprintf("%s.%s", name, seat.Seat),
				"seat":          seat.Seat,
				"runtime":       seat.Runtime,
				"sessionStatus": seat.State,
				"model":         seat.Model,
				"pid":           seat.Pid,
			})
		}
		return nodes
	}
	if rig != nil {
		out, err := rig.Run(context.Background(), "ps", "--nodes", "--rig", name, "--json")
		if err != nil {
			return nil
		}
		var nodes []map[string]any
		if json.Unmarshal([]byte(out), &nodes) != nil {
			return nil
		}
		return nodes
	}
	return nil
}

// PeakGate is the DeepSeek peak gate for every verb in the client that starts or resumes a room's seats
// - `up` and `compact` here, and the restore verbs in clientrestore, which is why it is exported: the
// decision must come from ONE reading of the room's node records, not from a second copy per package.
// The models are in the room's node records - the reading `rig ps --json --nodes --rig NAME` already gives
// the restore - so the gate costs one local daemon call and no cloud round trip.
//
// It answers the exit code its verb should return: ExitOK to continue, ExitUnavailable when the setting
// and the schedule refuse, ExitUsage when the schedule cannot be read at all (a malformed OFFPEAK_UTC is
// a typo in the environment, and this client fails loudly on those rather than guessing a clock).
//
// A rig tool it cannot resolve is NOT a refusal: the reading is impossible, so there is nothing to
// decide, and the verb's own platform matrix - or its supervisor's start - says so in its own words.
// Guessing "peak" from a missing reading would block a room the client knows nothing about.
func PeakGate(rig *clientcmd.Rig, name, verb string, allowPeak bool, stderr io.Writer) int {
	if rig == nil {
		tool, err := clientcmd.RequireRig()
		if err != nil {
			return clientcmd.ExitOK
		}
		rig = tool
	}
	proceed, message, err := clientoffpeak.Check(allowPeak, rigNodes(rig, name), os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam %s: %v\n", verb, err)
		return clientcmd.ExitUsage
	}
	if !proceed {
		fmt.Fprintf(stderr, "4genteam %s: %s\n", verb, message)
		return clientcmd.ExitUnavailable
	}
	return clientcmd.ExitOK
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

func openUIImpl(rig *clientcmd.Rig, stderr io.Writer) {
	// `up` no longer needs a resolved rig (NeedsRig is false), so this is reached with nil and resolves the
	// tool itself, exactly as the peak gate's own nil branch does. Reaching for `ui open` and failing is not
	// this verb's verdict, so an unresolvable tool is a line of stderr and nothing else.
	if rig == nil {
		tool, err := clientcmd.RequireRig()
		if err != nil {
			fmt.Fprintln(stderr, "4genteam: could not open the OpenRig UI (see: rig daemon status)")
			return
		}
		rig = tool
	}
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
	return "record a rig as one meant to run and report its compaction supervisor's row (rigd owns the restart)"
}
func (compactCommand) NeedsRig() bool { return false }

func (compactCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, compactUsage,
			"Record RIG as a room that is meant to run, and report its compaction supervisor's row.\n"+
				"7.1 made rigd the single restart authority, so this verb SPAWNS NOTHING: the supervisor is rigd's\n"+
				"child, it is restarted with backoff when it dies, and the repair verb is `up`.\n"+
				"The DeepSeek peak gate still applies here, unchanged: with "+
				clientoffpeak.ModeEnv+" on (the default) a room whose seats run a deepseek/ model is refused at peak, and "+
				clientoffpeak.FlagAllowPeak+" skips the check for this run.")
		return clientcmd.ExitOK
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(args)
	name, ok := requireRig("compact", rest, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	// No rig was resolved for this verb by the dispatcher, so the gate resolves one itself; with no rig
	// on the machine there is nothing to read and the refusal says so.
	if code := PeakGate(nil, name, "compact", allowPeak, stderr); code != clientcmd.ExitOK {
		return code
	}
	if !recordRigFor(name, stderr) {
		return clientcmd.ExitUnavailable
	}
	// WITH NO AUTHORITY THERE IS NO SUPERVISOR, and saying so is this verb's whole job now: it is exactly
	// the silence the owner's incident was made of.
	if rigdRunning() == 0 {
		fmt.Fprintf(stderr, "4genteam compact: rigd is not running, and the compaction supervisor is rigd's child now - start it with `4genteam up %s`.\n", name)
		return clientcmd.ExitUnavailable
	}
	rows := clientservices.NotRunning(rowsFor(name))
	for _, row := range rows {
		if row.Safeguard == clientservices.Compact {
			fmt.Fprintln(stderr, clientservices.StateLine(row))
			return clientcmd.ExitUnavailable
		}
	}
	fmt.Fprintln(stdout, "compact supervisor: running (supervised by rigd)")
	return clientcmd.ExitOK
}

type stopCommand struct{}

func (stopCommand) Name() string { return "stop" }
func (stopCommand) Summary() string {
	return "stop rigd and its children (a machine-wide stop: compact, watchdog, messages, bridge)"
}
func (stopCommand) NeedsRig() bool { return false }

func (stopCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, stopUsage,
			"Stop rigd. Its children - the compaction supervisors, the watchdogs and the bridge - stop with it,\n"+
				"because rigd is the single restart authority and a killed rigd must not leave orphans behind.\n"+
				"The stop is MACHINE-WIDE: it is not room intent, and the room stays in rigd.json's list.")
		return clientcmd.ExitOK
	}
	name, ok := requireRig("stop", args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	stopRigd(name, stdout)
	return clientcmd.ExitOK
}

type statusCommand struct{}

func (statusCommand) Name() string { return "status" }
func (statusCommand) Summary() string {
	return "the safeguard rows (--json too) and the supervisor's last log lines"
}
func (statusCommand) NeedsRig() bool { return false }

func (statusCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, statusUsage,
			"One line per safeguard row for the rig, then the compaction supervisor's process and the last five\n"+
				"lines of its log (shown for reading only: the supervisor logs only when it acts, so its log age is\n"+
				"never evidence of anything). With --json the rows are printed as JSON.\n\n"+
				"This verb only reads. `doctor` is the one that exits non-zero and names what is down.")
		return clientcmd.ExitOK
	}
	asJSON := false
	var rest []string
	for _, arg := range args {
		if arg == "--json" {
			asJSON = true
			continue
		}
		rest = append(rest, arg)
	}
	name, ok := requireRig("status", rest, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	rows := rowsFor(name)
	if asJSON {
		body, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "4genteam status: cannot print the rows: %v\n", err)
			return clientcmd.ExitUnavailable
		}
		fmt.Fprintln(stdout, string(body))
		return clientcmd.ExitOK
	}
	for _, row := range rows {
		fmt.Fprintln(stdout, clientservices.StateLine(row))
	}
	SupervisorStatus(name, stdout, stderr)
	return clientcmd.ExitOK
}

type logCommand struct{}

func (logCommand) Name() string    { return "log" }
func (logCommand) Summary() string { return "follow the supervisor's log" }
func (logCommand) NeedsRig() bool  { return false }

func (logCommand) Run(_ context.Context, _ *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
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
	if clientcmd.HelpRequested(args) {
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
	if clientcmd.HelpRequested(args) {
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
	stateRoot := flags.String("state-root", "", "where OpenRig keeps its per-seat state: the flag, else $OPENRIG_STATE_ROOT, else this machine's own")
	forcecompact := flags.String("forcecompact", ForcecompactPath(), "the forcecompact helper, when this machine has one")
	if err := flags.Parse(args); err != nil {
		return options, false
	}
	if strings.TrimSpace(*rig) == "" {
		fmt.Fprintf(stderr, "4genteam compact-run: --rig is required - there is no default rig. %s\n", compactRunUsage)
		return options, false
	}
	root, err := clientenv.ResolveStateRoot(*stateRoot)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam compact-run: %v\n", err)
		return options, false
	}
	options = CompactRunOptions{
		Rig:          *rig,
		Quiet:        *quietFlag,
		Every:        *every,
		Once:         *once,
		StateRoot:    root,
		Forcecompact: *forcecompact,
	}
	return options, true
}
