// Package clientlifecycle is the client's own lifecycle: the verbs that start, stop and inspect the
// local stack (up, compact, compact-run, stop, status, log, ui), ported from cli.py and compact.py.
//
// The supervisor LOOP is ported rather than shelled out to: the Python's `compact` started
// `python -m agenthub_client.compact`, and this client starts its own `compact-run`, so the two halves
// are one binary and the process pattern `status` matches is this binary's own argv.
//
// Every verb that takes a room REQUIRES it. The Python defaulted to a room of the owner's own team
// (DEFAULT_ROOM); a default that names somebody's team is not one a client may ship, so the name is an
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
	"github.com/phamhung075/4genthub-client/internal/clientsync"
	"github.com/phamhung075/4genthub-client/internal/clientteam"
	"github.com/phamhung075/4genthub-client/internal/clientwatch"
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
//
// RoomStartWindow is NOT the runtime's readiness window and must not be read as one: that one is
// `SEAT_READY_WINDOW_SECS` in rust/agenthub-runtime/src/supervisor/mod.rs (60 s, the time a seat is given to
// hand back its own proof), spent inside StartRoom before this poll ever begins. This one is the client's own
// patience afterwards. They were both 20 s by coincidence; the relation that is pinned lives in
// internal/clientruntime (the client's request deadline must outlast the runtime's window), not here.
var (
	DaemonReadyWindow = 10 * time.Second
	RoomStartWindow   = 20 * time.Second
)

const (
	upUsage         = "usage: 4genteam up ROOM [--team DIR] [--state-root DIR] [--room-only] [--respawn] " + clientoffpeak.ModeFlagUsage
	compactUsage    = "usage: 4genteam compact ROOM " + clientoffpeak.ModeFlagUsage
	stopUsage       = "usage: 4genteam stop ROOM"
	statusUsage     = "usage: 4genteam status ROOM [--state-root DIR]"
	logUsage        = "usage: 4genteam log ROOM [--state-root DIR]"
	uiUsage         = "usage: 4genteam ui"
	compactRunUsage = "usage: 4genteam compact-run --room ROOM [--quiet SECONDS] [--every SECONDS] [--once][--state-root DIR]"
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
		idleRunCommand{},
		budgetRunCommand{},
		doctorCommand{},
		agentCommand{},
		overlapCommand{},
		stuckCommand{},
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

// requireRoom is the required-room rule in one place: there is no default room to fall back on, so an
// omitted name is a usage error naming the verb and the argument.
func requireRoom(verb string, args []string, stderr io.Writer) (string, bool) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintf(stderr, "4genteam %s: a room name is required - there is no default room. %s\n", verb, usageFor(verb))
		return "", false
	}
	// A flag this verb does not take is refused rather than read as a room name: `up --some-flag` used to
	// mean a room called "--some-flag", whose failure arrived much later and much less clearly.
	if strings.HasPrefix(args[0], "-") && args[0] != "-" {
		fmt.Fprintf(stderr, "4genteam %s: unknown argument %s. %s\n", verb, args[0], usageFor(verb))
		return "", false
	}
	// And a SECOND argument is refused too: these verbs take one room name, so an argument that was
	// silently dropped is a caller believing something happened that did not.
	if len(args) > 1 {
		fmt.Fprintf(stderr, "4genteam %s: unexpected argument %s - this verb takes one room name. %s\n",
			verb, args[1], usageFor(verb))
		return "", false
	}
	return args[0], true
}

// The steps `up` runs, in order. Each is a variable so a test can assert the order without a daemon, a
// room, herdr or a UI: the Python test replaces the same functions.
//
// startBridge IS GONE FROM THIS LIST, and that is 7.1's removed path: `up` starts agent, and agent starts
// the bridge. Two start paths for one safeguard would be two mechanisms.
// ensureDaemon returns the log file the runtime daemon writes its own output to, and ensureRoom returns the
// room's verdict as this verb's exit code. Their signatures carry the state dir and the definition for one
// reason beyond the work: the acceptance run has to be able to say WHICH of the two answered.
var (
	ensureDaemon    = ensureDaemonImpl
	ensureWorktrees = ensureWorktreesImpl
	ensureRoom      = ensureRoomImpl
	recordRoomFor   = recordRoom
	startAuthority  = startAgent
	waitSafeguards  = waitForSafeguards
	openUI          = openUIImpl
	runWatchView    = runWatchViewImpl
	runHerdr        = runHerdrImpl
)

type upCommand struct{}

func (upCommand) Name() string { return "up" }
func (upCommand) Summary() string {
	return "start the local stack for a room: daemon, seats, agent (which starts the bridge and the supervisor), watch view"
}

func (upCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, upUsage,
			"The runtime daemon, this room's room and its seats, and then the safeguards: agent,\n"+
				"which owns the compaction supervisor, the watchdog, the seat-message deliverer and the status\n"+
				"bridge, and finally the watch view. It does not open the web UI: `4genteam ui` does that.\n"+
				"The room's seats come from the room's OWN record once it exists: --team DIR names the local team\n"+
				"definition that CREATES the room (team.json, no default, no cloud round trip), and without it up\n"+
				"starts the room from its own spec.json - the file the runtime wrote from that definition. That is\n"+
				"one way to name a team, and the second form is what heal's safeguard repair runs.\n"+
				"--room-only stops after the room: it starts the daemon, starts the room from the definition or its\n"+
				"own spec.json, and prints one line per seat. It EXCLUDES agent, the bridge, the watch view, the UI\n"+
				"and herdr, whose failures are not this verb's verdict - and it must not be used to make a red go\n"+
				"away.\n"+
				"--respawn REPLACES the running runtime daemon instead of reusing it: agent is stopped (it is the\n"+
				"single restart authority, so its children stop with it), then the daemon serving this state root\n"+
				"is signalled and waited out, and this same run starts the daemon again - so the binary installed\n"+
				"by scripts/build.sh is the one that serves the room. The room is then read and started as below;\n"+
				"its record, its seats' directories and their worktrees are left in place. A daemon that does not\n"+
				"exit within the window is reported by process id and this run refuses rather than starting a second one.\n"+
				"The acceptance invocation, so a reader re-reads the signature instead of remembering it:\n"+
				"  4genteam up ROOM [--team DIR] [--state-root DIR] --room-only\n"+
				"WHAT IT EXITS WITH (design §2.5), counting only the room's own verdict:\n"+
				"  0  every seat of the room is running. A room that is already up is READ, not restarted: a blind\n"+
				"     start would cycle sessions of live agents.\n"+
				"  2  the daemon started but a seat did not - the seats that are not running are named, with the\n"+
				"     daemon's log path to read next\n"+
				"  3  the invocation is wrong (including: the room has no record and no --team was given), or the\n"+
				"     daemon will not take the definition it was handed\n"+
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
	// THE ROOT IS READ THROUGH THE ONE PARSER, before a flag set exists: it strips `--state-root DIR`
	// (wherever it sits, last wins) and REFUSES the runtime's spelling with exit 2 before this verb has
	// started anything. The flag set below therefore names no root at all.
	rootFlag, rest, rootErr := clientenv.StateRootFromFlags(rest)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam up: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUsage)
	}
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(stderr)
	teamDir := flags.String("team", "", "the team definition directory that holds team.json (optional: a room with its own record starts from it)")
	roomOnly := flags.Bool("room-only", false, "start the daemon and the room, print a line per seat, and stop before agent, the bridge, the watch view and the UI")
	respawn := flags.Bool("respawn", false, "stop agent and replace the runtime daemon serving this state root, so the installed binary serves the room")
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
	name, ok := requireRoom("up", positional, stderr)
	if !ok {
		return clientcmd.ExitRoomRefused
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	// THE PAIR IS PRINTED, NOT RESOLVED SILENTLY. The state dir decides which rooms exist and the socket
	// decides which daemon is asked, and with the flags absent both come from this machine's own
	// environment - so the run SAYS which daemon it is talking to instead of leaving a reader to guess, and
	// the room's own lines below are read against it (design §1).
	fmt.Fprintf(stdout, "state dir: %s\nsocket: %s\n", root, clientruntime.DefaultSocketPath())
	return runUp(name, *teamDir, root, allowPeak, *roomOnly, *respawn, stdout, stderr)
}

// splitFlagsAndPositional separates this verb's flags from its one positional, and puts the positionals
// LAST: the flag package STOPS at the first bare word, so `up ROOM --team DIR` - the order the usage line
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

func upFlagTakesValue(name string) bool { return name == "team" }

// upTakesFlag is this verb's own flag list, so an argument it does not take is refused HERE with the
// argument NAMED: the flag package's own message says `-any-time`, and the shape this replaces was a verb
// that quietly ignored the extra argument. The root is NOT here: StateRootFromFlags has already taken it
// (and refused the runtime's spelling), so a flag set that still named it would be a second reader.
func upTakesFlag(name string) bool {
	switch name {
	case "team", "room-only", "respawn", "h", "help":
		return true
	}
	return false
}

func runUp(name, teamDir, stateDir string, allowPeak, roomOnly, respawn bool, stdout, stderr io.Writer) int {
	// The team is read FIRST: a --team typo is a caller error, and it must be reported before a daemon is
	// started or a seat is touched. The room name must be the room the input names, which is the same rule
	// clientpolicy applies to its own --room.
	// A bad invocation is 3 for this verb, not the CLI-wide ExitUsage (2): 2 is what a PARTIAL START is here
	// (design §2.5), and one number cannot mean both things to a caller reading an exit code.
	spec, err := roomSpecFor(name, teamDir, stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	// THE SEATS' DIRECTORIES ARE THE ROOM'S OWN, and they are settled BEFORE the process probe, the daemon,
	// the worktrees step and the first launch: a spec whose cwds name anything but the derived paths is a
	// definition this verb will not start (seat-worktrees.md - the main checkout takes only the land). The
	// filled spec is the one the worktrees step ensures, the one the daemon is handed, and therefore the one
	// spec.json records, so `doctor`'s F1 compares a pane against the path `up` actually launched it in.
	spec, err = seatCwdsByDerivation(stateDir, name, spec)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: %v\n", err)
		return clientcmd.ExitRoomRefused
	}
	// THE PROCESS PROBE RUNS FIRST, BEFORE THE DAEMON, AGENT OR A SEAT (7.12). Every step below reads or
	// signals processes - `--respawn` stops the daemon and agent, `startAgent` decides whether to start,
	// and the start grace waits on the report - so a machine whose pgrep or pkill cannot run would act on
	// "nothing is running" and start a second copy of what is already there. Refusing here starts nothing.
	if err := clientservices.Probe(); err != nil {
		fmt.Fprintf(stderr, "4genteam up: nothing was started or stopped: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	// --respawn REPLACES THE DAEMON, and it does so BEFORE the reachability read below can reuse it: the
	// daemon is nobody's child and keeps running the binary it was started with, so a build that renamed a
	// new one into place changes nothing until the old process is gone. Agent goes first because it is the
	// restart authority: a watchdog left running would restore the seats of the room being cycled.
	//
	// AND IT RUNS DETACHED (respawn.go): the caller is a seat inside agent, so stopping agent is the
	// caller's own death - measured 2026-10-20 17:29:38Z, when the daemon was replaced and the seats and
	// agent the run was about to start never were. The re-exec happens BEFORE the first signal; this
	// process then answers with the log's path and returns, and the restart continues in its own session.
	if respawn {
		if os.Getenv(respawnDetachedEnv) == "" {
			logPath, err := detachRespawn(stateDir, respawnArgv(name, teamDir, stateDir, allowPeak, roomOnly))
			if err != nil {
				fmt.Fprintf(stderr, "4genteam up: --respawn was not started, and nothing was stopped: %v\n", err)
				return clientcmd.ExitUnavailable
			}
			fmt.Fprintf(stdout, "respawn: the restart runs in its own session; log: %s\n", logPath)
			return clientcmd.ExitOK
		}
		if code := stopAgent("up", name, stdout, stderr); code != clientcmd.ExitOK {
			return code
		}
		if !respawnDaemon(stateDir, stdout, stderr) {
			return clientcmd.ExitUnavailable
		}
	}
	daemonLog, err := ensureDaemon(stateDir, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: %v\n", err)
		return clientcmd.ExitRemote
	}
	// THE GATE RUNS BEFORE THE RESTORE, and after the daemon only because the room's models are read from
	// the daemon. So a refusal starts the daemon and nothing else: no seat is resumed, which is the step
	// that would have started DeepSeek work at the peak rate.
	if code := peakGate("", name, "up", allowPeak, declaredNodes(spec), stderr); code != clientcmd.ExitOK {
		return code
	}
	// THE WORKTREES STEP RUNS BEFORE THE SEATS, and after the gate, which is the order the ruling gives
	// (daemon, gate, worktrees, seats): a seat's own worktree must exist before it launches, and each
	// seat's cwd is now that worktree rather than the room. A refusal here starts the daemon and
	// nothing else, exactly as a gate refusal does.
	if code := ensureWorktrees(stateDir, name, spec, stdout, stderr); code != clientcmd.ExitOK {
		return code
	}
	if code := ensureRoom(name, spec, daemonLog, stdout, stderr); code != clientcmd.ExitOK {
		return code
	}
	// --room-only is an INSTRUMENT: it ends here, with the room's own verdict. The guard is deliberate - a
	// red that comes from the bridge, the UI or herdr is not evidence about the room.
	if roomOnly {
		return clientcmd.ExitOK
	}
	// 7.5: a bridge refused for a missing AGENTHUB_URL or AGENTHUB_TOKEN is a FAILED `up`. The check runs
	// HERE, before agent is started, because the alternative is a bridge that dies at once and a message
	// about a process instead of about the variable that is missing. The value is never printed.
	if missing := missingCloudEnv(); len(missing) > 0 {
		return refuseMissingCloudEnv("up", missing, stderr)
	}

	// The room is recorded as one that is meant to run (7.1), and then the single restart authority is
	// started. From here on agent owns the compaction supervisor, the watchdog and the bridge; `up` starts
	// nothing else for them.
	if !recordRoomFor(name, stderr) {
		return clientcmd.ExitUnavailable
	}
	if !startAuthority(stateDir, stdout, stderr) {
		return ExitStartFailed
	}

	// 7.5: the SAME check `doctor` makes, run after agent's start grace and BEFORE the watch view, the UI
	// and herdr. A safeguard that did not come up is named here, with the line a reader will see in
	// `doctor`, rather than discovered hours later on a quiet room.
	rows, ok, err := waitSafeguards(name, stateDir)
	if err != nil {
		// THE PREFLIGHT ABOVE MAKES THIS A RACE, and it is still refused rather than treated as a report of
		// dead children: the start grace could not decide a row, so it cannot name one as down (7.12).
		fmt.Fprintf(stderr, "4genteam up: no safeguard row was decided: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	if !ok {
		reportDown(rows, stderr)
		return clientcmd.ExitUnavailable
	}

	// 7.5's skills check runs HERE: the room is up, so every seat is launched and the skills each one
	// actually loaded can be named. It is BEFORE the watch view, the UI and herdr, so a seat that loaded
	// something other than the set the runtime installed for it is named before anything else proceeds. It
	// starts, restarts and signals NOTHING: `up` does not kill a working seat to fix its skills.
	//
	// Every line the check produces is reported, failures AND the notes about an input this client could not
	// obtain: a reader must be able to tell "the seats are right" from "the seats could not be looked at".
	// Only the failures make the verb refuse.
	verdict := roomSkillsVerdict(stateDir, name)
	reportSeatSkills(verdict, stderr)
	if len(verdict.Failures) > 0 {
		return clientcmd.ExitUnavailable
	}

	// No browser: `up` runs on every respawn, and the web UI is `4genteam ui`, opened when a person wants it.
	runWatchView(name, stderr)
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
	cmd := runtimeDaemonCommand(runtimeBin, stateDir, client.SocketPath)
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

// runtimeDaemonCommand is the daemon spawn's argv in one place: `--state-dir` is the RUNTIME binary's own
// flag and it does not move when the client's spelling became `--state-root` (architect ruling 2026-10-17,
// R2). A variable rather than an inline exec so the argv is assertable without spawning a daemon.
var runtimeDaemonCommand = func(bin, stateDir, socketPath string) *exec.Cmd {
	return exec.Command(bin, "--state-dir", stateDir, "--socket", socketPath, "daemon")
}

// runtimeBinary is the runtime executable: PATH first, then the per-user install.
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

// ensureRoomImpl makes the room ready from the definition's own spec and reads it back until every seat is
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
// ensureWorktreesImpl is the ruling's worktrees step: every seat works in its own worktree, so each
// seat's cwd must exist before it launches. The derivation, the creation, the two submodule worktrees
// and the reuse/refuse rules all live behind clientworktree.Ensure and clientservices.DerivedWorktrees,
// so `up`, the `worktree` verbs and `doctor`'s row answer from ONE implementation.
//
// The seats are the ones this start is about - the spec `up` already resolved - never a second reading
// of the definition: a room must not be started with one team and have its worktrees derived from
// another. A refusal is an `up` refusal; the alternative is a seat launching in the room and editing
// the shared checkout, which is the defect the ruling removes.
func ensureWorktreesImpl(root, name string, spec clientruntime.RoomSpec, stdout, stderr io.Writer) int {
	roomDir, err := worktreeRoomDir(root, name)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: cannot derive the room's seat worktrees: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	mainRoot, err := worktreeMainRoot("")
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: cannot derive the room's seat worktrees: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	// THE ROOM'S OWN REPOSITORY, and not the one `up` was run in: the definition records each seat's
	// cwd, so the root is read from it rather than inherited from the working directory. Measured
	// 2026-10-18: from a client worktree the inherited answer refused 6 of 6 paths as "not a worktree
	// registered ... of <client repo>", naming a repository the paths never belonged to; from the
	// project root the same command printed six kept lines.
	if root, ok, seatErr := worktreeSeatRoot(roomSeatCwds(spec)); seatErr != nil {
		// Two repositories: the definition is the record, but it cannot name one root, so the working
		// directory's answer stands and the disagreement is said rather than hidden.
		fmt.Fprintf(stderr, "4genteam up: the room's seats do not all live in one repository (%v); deriving from %s\n",
			seatErr, mainRoot)
	} else if ok && filepath.Clean(root) != filepath.Clean(mainRoot) {
		fmt.Fprintf(stdout, "4genteam up: the room's seats live in %s, not in the working directory's %s; using %s\n",
			root, mainRoot, root)
		mainRoot = root
	}
	seats := make([]clientservices.SeatType, 0, len(spec.Seats))
	for _, seat := range spec.Seats {
		seats = append(seats, clientservices.SeatType{Key: seat.SeatKey, Type: seat.SeatType})
	}
	if err := worktreeEnsure(mainRoot, roomDir, name, seats, stdout); err != nil {
		fmt.Fprintf(stderr, "4genteam up: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	return clientcmd.ExitOK
}

// roomSeatCwds is the definition's own answer to "where do this room's seats live": each seat's cwd in
// spec order, with the empty ones dropped. It reads the spec THIS START is about, so the root cannot be
// taken from a second reading of the room.
func roomSeatCwds(spec clientruntime.RoomSpec) []string {
	cwds := make([]string, 0, len(spec.Seats))
	for _, seat := range spec.Seats {
		if seat.Cwd == nil {
			continue
		}
		if cwd := strings.TrimSpace(*seat.Cwd); cwd != "" {
			cwds = append(cwds, cwd)
		}
	}
	return cwds
}

func ensureRoomImpl(name string, spec clientruntime.RoomSpec, daemonLog string, stdout, stderr io.Writer) int {
	client := clientruntime.NewClient("")
	reported, statusErr := client.GetRoomStatus(name)
	restart, sendSpec, planFail := seatStartPlan(reported, statusErr)
	if planFail != nil {
		// REFUSED BEFORE ANY StartRoom: the status answer could not be read, so this process does not know what is
		// running and must not start a room from the definition. Exit 3 is the room contract's number for a request
		// the daemon did not serve, and the message is the one the decoder wrote - the verb, and the shape it got.
		fmt.Fprintln(stderr, planFail)
		return clientcmd.CodeOf(planFail, clientcmd.ExitRoomRefused)
	}
	seats := reported
	// The seats are about to be started, and a seat proves itself by handshake: the runtime gives it a window
	// that has to cover a first launch in a fresh cache, which builds the agent's model cache and is measured in
	// tens of seconds. Say so before going quiet, so a first `up` on a new box does not read as a hang.
	if sendSpec || len(restart) > 0 {
		fmt.Fprintln(stdout, "4genteam up: starting the room's seats and waiting for them to prove themselves "+
			"(a first launch in a fresh cache builds the agent's model cache, so this can take a while)")
	}
	if sendSpec {
		// THE POLICY DOCUMENTS TRAVEL WITH THE SPEC, because only the client talks to the hub: the runtime
		// installs each document as that seat's config.yml before its runner starts. A seat of another runtime
		// is skipped rather than fetched for - its config is not this document's destination - and a render that
		// cannot be read is said and leaves that seat on the room's own record (clientsync.SeatDocumentFor).
		for i := range spec.Seats {
			if !clientsync.ServesRuntime(spec.Seats[i].Runtime) {
				continue
			}
			spec.Seats[i].PolicyDocument = clientsync.SeatDocumentFor(context.Background(), name, spec.Seats[i].SeatKey, stderr)
		}
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
		// A restarted seat is started FROM THE ROOM'S OWN SPEC, so a fresh render has to travel on this frame
		// instead: fetched here for the seat being restarted, the same way a whole-room start carries one per
		// seat. A seat of another runtime is not gated out here - the hub renders this document for an omp seat
		// only, so the answer for any other seat is `no document` and nothing is installed for it.
		document := clientsync.SeatDocumentFor(context.Background(), name, seat, stderr)
		if _, err := client.RestartSeat(name, seat, document); err != nil {
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

// roomSpecFor resolves the room's seats the one way this verb takes them: from the local definition when
// --team names one, and otherwise from the room's own spec.json - the record the runtime wrote from that
// definition when it last started the room.
//
// THE TWO FORMS ARE ONE RULE, NOT TWO FLAGS: a definition is what CREATES a room's record, and after that
// the record is what a start reads. heal's safeguard repair runs `up ROOM` with no definition to name, and
// before this it was refused by up's own usage gate every time, so the finding it reported could never be
// fixed. --team does not go away: giving it is how a room is created, and how its seats are edited.
func roomSpecFor(name, teamDir, stateDir string) (clientruntime.RoomSpec, error) {
	if strings.TrimSpace(teamDir) == "" {
		return clientteam.LoadLiveRoomSpec(stateDir, name)
	}
	spec, err := clientteam.LoadRoomSpec(teamDir)
	if err != nil {
		return clientruntime.RoomSpec{}, err
	}
	if spec.Room != name {
		return clientruntime.RoomSpec{}, fmt.Errorf(
			"%s defines the room %q, not %q - --room must name the room the definition defines. %s",
			teamDir, spec.Room, name, upUsage)
	}
	return spec, nil
}

// seatCwdsByDerivation is where a seat works, and the ruling has ONE answer
// (ai_docs/core-architecture/seat-worktrees.md): <room>/wt/<key>, derived from the room's seats.
//
// It used to be carried forward instead - carrySeatCwds copied whatever cwd the live spec.json held into
// the definition's blank ones - and that is how a seat came to launch in the shared checkout while
// doctor's F1 compared its pane against the derived path and refused it (measured 2026-10-21: the lead at
// /home/daihu/__projects__/4genthub vs <room>/wt/lead, go-dev at wt/go-dev-root vs wt/go-dev). A blank cwd
// is therefore FILLED from the derivation, so the start, the record it writes and F1 cannot disagree, and a
// cwd that names anything else is refused BEFORE the first seat starts, naming the seat, its declared
// path, the derived one and the two ways to settle it.
func seatCwdsByDerivation(root, name string, spec clientruntime.RoomSpec) (clientruntime.RoomSpec, error) {
	roomDir, err := worktreeRoomDir(root, name)
	if err != nil {
		return spec, fmt.Errorf("the seats' worktrees cannot be derived: %w", err)
	}
	seats := make([]clientservices.SeatType, 0, len(spec.Seats))
	for _, seat := range spec.Seats {
		seats = append(seats, clientservices.SeatType{Key: seat.SeatKey, Type: seat.SeatType})
	}
	derived := make(map[string]string, len(seats))
	for _, worktree := range clientservices.DerivedWorktrees(roomDir, name, seats) {
		if worktree.SeatKey == clientservices.Integration {
			continue
		}
		derived[worktree.SeatKey] = worktree.Path
	}
	for i := range spec.Seats {
		want, found := derived[spec.Seats[i].SeatKey]
		if !found {
			// A seat with no key has no address and no path; DerivedWorktrees skips it for the same reason.
			continue
		}
		declared := ""
		if spec.Seats[i].Cwd != nil {
			declared = strings.TrimSpace(*spec.Seats[i].Cwd)
		}
		if declared != "" && filepath.Clean(declared) != filepath.Clean(want) {
			return spec, fmt.Errorf(
				"seat %s works in %s, not in %s, which is the worktree this room derives for it: drop the "+
					"seat's cwd so it is derived, or move what is there to the derived path (%s)",
				spec.Seats[i].SeatKey, declared, want, upUsage)
		}
		cwd := want
		spec.Seats[i].Cwd = &cwd
	}
	return spec, nil
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
func seatStartPlan(reported []clientruntime.SeatStatus, err error) (restart []string, sendSpec bool, fail error) {
	if err != nil {
		// ONLY THE DAEMON'S OWN REFUSAL MAY START A ROOM. A *clientruntime.DaemonRefused means the daemon ANSWERED and
		// will not serve this request - for a status read, a room it does not know, and the definition is then the only
		// source of its seats (U1). EVERY OTHER error means this process learned NOTHING about what is running: an
		// unreadable answer (*UnreadableAnswer), a frame that never parsed, a transport that never answered. Starting
		// from the definition then is a blind start, the one outcome check-then-start exists to prevent, and StartRoom
		// cycles a room of live agents when it finds one running. Both kinds used to leave with exit 3 and be collapsed
		// here; they are told apart by TYPE now, not by the code they share.
		var refused *clientruntime.DaemonRefused
		if errors.As(err, &refused) {
			return nil, true, nil
		}
		return nil, false, err
	}
	if len(reported) == 0 {
		return nil, true, nil
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
		return nil, true, nil
	}
	return restart, false, nil
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

// roomNodes is the ONE reading of a room's node records here, so the gate and the restore cannot decide
// from different things. A read that FAILS gives NO nodes and an error: the caller refuses in those words,
// and an unreadable reading is never read as "this room is safe".
//
// The clientseat package reads the SAME runtime call as roomNodes, with the OPPOSITE contract: there a failed read is
// an ERROR naming the room, because `reseat` has to say why it will not restart a seat. The names differ on purpose -
// two same-named functions with opposite contracts is a trap whose only guard would be a commit message.
//
// The reading itself is clientruntime.RoomNodesAt, in one home so the gate here and the watchdog in
// clientrestore cannot disagree about the same room.
//
// THE SOCKET IS THE CALLER'S, not this machine's default: a verb given `--socket PATH` gates the daemon it is
// about to restart, so the seam takes the resolved socket and passes it down. An empty socket is the default,
// which is what the in-process agent, the supervisor and a restore want.
//
// The three answers are the seam's own, explicit rather than a sentinel: ok with no error is a transport's
// answer (possibly an empty table - a room with no seats), no ok and no error is NO RECORDS - the
// runtime knows no such room, so there is nothing to decide - and an error is a read that FAILED.
var roomNodes = func(socket, name string) ([]map[string]any, bool, error) {
	return clientruntime.RoomNodesAt(socket, name)
}

// PeakGate is the DeepSeek peak gate for every verb in the client that starts or resumes a room's seats
// - `up` and `compact` here, and the restore verbs in clientrestore, which is why it is exported: the
// decision must come from ONE reading of the room's node records, not from a second copy per package.
// The models are in the room's node records, read over the client's own transports, so the gate costs one
// local call and no cloud round trip.
//
// It answers the exit code its verb should return: ExitOK to continue, ExitUnavailable when the reading
// cannot be taken or the setting and the schedule refuse, ExitUsage when the schedule cannot be read at all
// (a malformed OFFEPEAK_UTC is a typo in the environment, and this client fails loudly on those rather than
// guessing a clock).
//
// `up` is the one verb that knows what a room is ABOUT to run: the definition it was given. A first start has
// no records, and gating it on nothing is how a DeepSeek seat started at peak with only a printed apology, so
// `up` passes the definition's own seats as `declared` and the gate decides on those when the room has no
// records of its own.
//
// A READING THAT FAILS IS A REFUSAL, in words: this used to resolve a room tool and return ExitOK when it
// could not, which is how a peak-time resume could proceed with the gate never having decided. A room with
// NO records is different and still proceeds - there are no seats to bill, which is the shape of a room
// being started for the first time - and blocking a start on
// a room the client knows nothing about would be the wrong call. But the gate has NOT decided THAT start,
// and the room may still run a DeepSeek seat, so proceeding is SAID rather than silent: the line below is
// the whole guard on this fail-open case, and it is here, once, so that no verb reaching this gate - `up`,
// `compact`, or a restore - can forget to print it.
func PeakGate(name, verb string, allowPeak bool, stderr io.Writer) int {
	return PeakGateAt("", name, verb, allowPeak, stderr)
}

// PeakGateAt is PeakGate against the daemon at socket: the verb's own `--socket`, when it has one, so the
// gate reads the daemon it is about to stop and restart rather than this machine's default. An empty socket
// is PeakGate exactly - the default - which is what `up`, `compact` and the restore path pass.
//
// THE DEFECT THIS CLOSES (`room reseat --socket`, `heal --socket`): the gate used to read the default socket
// whatever the verb was told, so on a machine with two daemons the gate could refuse a fine room or pass a
// DeepSeek room at peak, and a test pointing the verb at a fake daemon saw the gate dial nothing at all.
func PeakGateAt(socket, name, verb string, allowPeak bool, stderr io.Writer) int {
	return peakGate(socket, name, verb, allowPeak, nil, stderr)
}

// declaredNodes is a definition's seats in the shape of the room's node records, which is what the gate reads.
func declaredNodes(spec clientruntime.RoomSpec) []map[string]any {
	nodes := make([]map[string]any, 0, len(spec.Seats))
	for _, seat := range spec.Seats {
		model := ""
		if seat.Model != nil {
			model = *seat.Model
		}
		nodes = append(nodes, map[string]any{
			"logicalId": fmt.Sprintf("%s.%s", spec.Room, seat.SeatKey),
			"model":     model,
		})
	}
	return nodes
}

func peakGate(socket, name, verb string, allowPeak bool, declared []map[string]any, stderr io.Writer) int {
	nodes, ok, err := roomNodes(socket, name)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam %s: the room's node records could not be read, so the peak gate cannot "+
			"say whether starting this room bills at peak: %v\n", verb, err)
		return clientcmd.ExitUnavailable
	}
	if !ok && len(declared) > 0 {
		nodes, ok = declared, true
	}
	if !ok {
		fmt.Fprintf(stderr, "4genteam %s: the room %s has no node records, so the peak gate did NOT run for "+
			"this start: a DeepSeek seat may be starting on the peak rate\n", verb, name)
		return clientcmd.ExitOK
	}
	proceed, message, err := clientoffpeak.Check(allowPeak, nodes, os.Getenv)
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

// uiURLEnv names the 4genthub web UI to open; the default is the frontend this repository serves in development.
const (
	uiURLEnv  = "AGENTHUB_UI_URL"
	defaultUI = "http://localhost:3800"
)

// openURL hands a URL to the desktop's opener. It is a seam so a test opens nothing.
var openURL = openURLImpl

// openURLImpl uses the first opener the machine has: wslview (WSL), xdg-open (Linux), open (macOS). The opener
// is started and not waited on, because a browser that outlives this verb is the point.
func openURLImpl(url string) error {
	for _, opener := range []string{"wslview", "xdg-open", "open"} {
		if path, err := exec.LookPath(opener); err == nil {
			return exec.Command(path, url).Start()
		}
	}
	return errors.New("none of wslview, xdg-open or open is on PATH")
}

// openUIImpl opens the 4genthub web UI. A failure is a line of stderr and nothing else: reaching for the UI and
// missing is not `up`'s verdict.
func openUIImpl(stderr io.Writer) {
	url := os.Getenv(uiURLEnv)
	if url == "" {
		url = defaultUI
	}
	if err := openURL(url); err != nil {
		fmt.Fprintf(stderr, "4genteam: could not open the 4genthub UI at %s: %v\n", url, err)
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
	cmd := exec.Command(self, "watch", "--room", name)
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
	return "record a room as one meant to run and report its compaction supervisor's row (agent owns the restart)"
}

func (compactCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, compactUsage,
			"Record ROOM as a room that is meant to run, and report its compaction supervisor's row.\n"+
				"7.1 made agent the single restart authority, so this verb SPAWNS NOTHING: the supervisor is agent's\n"+
				"child, it is restarted with backoff when it dies, and the repair verb is `up`.\n"+
				"The DeepSeek peak gate still applies here, unchanged: with "+
				clientoffpeak.ModeEnv+" on (the default) a room whose seats run a deepseek/ model is refused at peak, and "+
				clientoffpeak.FlagAllowPeak+" skips the check for this run.")
		return clientcmd.ExitOK
	}
	allowPeak, rest := clientoffpeak.ModeFromFlags(args)
	name, ok := requireRoom("compact", rest, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	// The gate reads the room's node records itself; there is no room to resolve for this verb, and a
	// reading that cannot be taken is refused in words rather than treated as a safe room.
	if code := PeakGate(name, "compact", allowPeak, stderr); code != clientcmd.ExitOK {
		return code
	}
	if !recordRoomFor(name, stderr) {
		return clientcmd.ExitUnavailable
	}
	// WITH NO AUTHORITY THERE IS NO SUPERVISOR, and saying so is this verb's whole job now: it is exactly
	// the silence the owner's incident was made of. A probe that could not run is refused by name instead
	// (7.12): "cannot see agent" is not "agent is not running".
	pid, err := agentRunning()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam compact: no safeguard row was decided: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	if pid == 0 {
		fmt.Fprintf(stderr, "4genteam compact: agent is not running, and the compaction supervisor is agent's child now - start it with `4genteam up %s`, or compact the room's seats through the runtime with `4genteam room compact %s`.\n", name, name)
		return clientcmd.ExitUnavailable
	}
	root, err := clientenv.ResolveStateRoot("")
	if err != nil {
		fmt.Fprintf(stderr, "4genteam compact: %v\n", err)
		return clientcmd.ExitUsage
	}
	report, err := rowsFor(name, root)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam compact: no safeguard row was decided: %v\n", err)
		return clientcmd.ExitUnavailable
	}
	rows := clientservices.NotRunning(report)
	for _, row := range rows {
		if row.Safeguard == clientservices.Compact {
			fmt.Fprintln(stderr, clientservices.StateLine(row))
			return clientcmd.ExitUnavailable
		}
	}
	fmt.Fprintln(stdout, "compact supervisor: running (supervised by agent)")
	return clientcmd.ExitOK
}

type stopCommand struct{}

func (stopCommand) Name() string { return "stop" }
func (stopCommand) Summary() string {
	return "stop agent and its children (a machine-wide stop: compact, watchdog, messages, bridge)"
}

// closeWatch closes the room's herdr watch workspaces; a var so the tests need no herdr.
var closeWatch = clientwatch.CloseRoomWorkspaces

func (stopCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, stopUsage,
			"Stop agent. Its children - the compaction supervisors, the watchdogs and the bridge - stop with it,\n"+
				"because agent is the single restart authority and a killed agent must not leave orphans behind.\n"+
				"The stop is MACHINE-WIDE: it is not room intent, and the room stays in agent.json's list.\n\n"+
				"This stops the SUPERVISION and nothing else. The seat sessions are left standing (their turns\n"+
				"are not cut), and the runtime daemon is not a child of agent: no verb stops it, so it keeps the\n"+
				"rooms and serves the seats across a stop. `up` starts the supervision again, and `continue`\n"+
				"reports a room whose seats are up while a safeguard is down as NOT restored.")
		return clientcmd.ExitOK
	}
	name, ok := requireRoom("stop", args, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	if code := stopAgent("stop", name, stdout, stderr); code != clientcmd.ExitOK {
		return code
	}
	if err := closeWatch(ctx, name); err != nil {
		fmt.Fprintf(stderr, "4genteam stop: the watch workspaces of %s could not be closed: %v\n", name, err)
	}
	return clientcmd.ExitOK
}

type statusCommand struct{}

func (statusCommand) Name() string { return "status" }
func (statusCommand) Summary() string {
	return "the safeguard rows (--json too) and the supervisor's last log lines"
}

func (statusCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, statusUsage,
			"One line per safeguard row for the room, then the compaction supervisor's process and the last five\n"+
				"lines of its log (shown for reading only: the supervisor logs only when it acts, so its log age is\n"+
				"never evidence of anything). With --json the rows are printed as JSON.\n\n"+
				"This verb only reads. `doctor` is the one that exits non-zero and names what is down.")
		return clientcmd.ExitOK
	}
	asJSON := false
	rootFlag, remaining, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam status: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUsage)
	}
	var rest []string
	for _, arg := range remaining {
		if arg == "--json" {
			asJSON = true
			continue
		}
		rest = append(rest, arg)
	}
	name, ok := requireRoom("status", rest, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam status: %v\n", err)
		return clientcmd.ExitUsage
	}
	rows, err := rowsFor(name, root)
	if err != nil {
		// A PURE READER KEEPS ITS EXIT (7.12): no row was decided, so none is printed and the probe is named
		// on stderr. The supervisor line is skipped too - it is the same probe, and it would name the cause
		// a second time.
		fmt.Fprintf(stderr, "4genteam status: no safeguard row was decided: %v\n", err)
		return clientcmd.ExitOK
	}
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
	SupervisorStatus(name, root, stdout, stderr)
	return clientcmd.ExitOK
}

type logCommand struct{}

func (logCommand) Name() string    { return "log" }
func (logCommand) Summary() string { return "follow the supervisor's log" }

func (logCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, logUsage, "Follow the compaction supervisor's log for a room.")
		return clientcmd.ExitOK
	}
	rootFlag, rest, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam log: %v\n", rootErr)
		return clientcmd.CodeOf(rootErr, clientcmd.ExitUsage)
	}
	name, ok := requireRoom("log", rest, stderr)
	if !ok {
		return clientcmd.ExitUsage
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam log: %v\n", err)
		return clientcmd.ExitUsage
	}
	logPath := LogPath(name, root)
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
func (uiCommand) Summary() string { return "open the 4genthub web UI" }

func (uiCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		printHelp(stdout, uiUsage, "Open the 4genthub web UI (AGENTHUB_UI_URL, default http://localhost:3800).")
		return clientcmd.ExitOK
	}
	openUI(stderr)
	return clientcmd.ExitOK
}

type compactRunCommand struct{}

func (compactRunCommand) Name() string { return "compact-run" }
func (compactRunCommand) Summary() string {
	return "the compaction supervisor loop, in the foreground (what compact starts)"
}

func (compactRunCommand) Run(_ context.Context, args []string, stdout, stderr io.Writer) int {
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
	supervisor := New(options.Room, options.Quiet, options.StateRoot, stdout)
	supervisor.Log(fmt.Sprintf("supervising %s: limit %s, quiet %ds",
		options.Room, seatlog.Kilo(seatlog.CompactLimit), options.Quiet))
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
	Room      string
	Quiet     int
	Every     int
	Once      bool
	StateRoot string
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
	// THE ROOT IS READ THROUGH THE ONE PARSER, so this loop's flag set does not name it: the root is
	// stripped wherever it sits and the runtime's spelling is refused before a pass runs.
	rootFlag, args, rootErr := clientenv.StateRootFromFlags(args)
	if rootErr != nil {
		fmt.Fprintf(stderr, "4genteam compact-run: %v\n", rootErr)
		return options, false
	}
	flags := flag.NewFlagSet("compact-run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	room := flags.String("room", "", "the room to supervise (required)")
	quietFlag := flags.Int("quiet", quiet, "seconds of log silence that mean the job is finished")
	every := flags.Int("every", 10, "seconds between passes")
	once := flags.Bool("once", false, "run one pass and exit")
	if err := flags.Parse(args); err != nil {
		return options, false
	}
	if strings.TrimSpace(*room) == "" {
		fmt.Fprintf(stderr, "4genteam compact-run: --room is required - there is no default room. %s\n", compactRunUsage)
		return options, false
	}
	root, err := clientenv.ResolveStateRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam compact-run: %v\n", err)
		return options, false
	}
	options = CompactRunOptions{
		Room:      *room,
		Quiet:     *quietFlag,
		Every:     *every,
		Once:      *once,
		StateRoot: root,
	}
	return options, true
}
