// respawn: `up --respawn` replaces the runtime daemon so a binary installed by scripts/build.sh is the
// one that serves the room.
//
// THE DAEMON IS NOBODY'S CHILD. `stop` stops agent and its children, and the runtime daemon is not one of
// them: it keeps the rooms and serves the seats across an agent stop, which is what makes `up` idempotent.
// The daemon also never re-reads its own binary - `scripts/build.sh` builds beside the target and renames
// it into place, so a running daemon holds the old inode - and no verb stops it. Before this step the
// restart was manual, and a seat that rebuilt the client (skills/client/permanent-change) left the
// machine serving the previous runtime.
//
// SO THE REPLACEMENT IS BY PATTERN, the same pgrep/pkill pair the safeguards use, matched on the argv the
// daemon is spawned with (runtimeDaemonCommand): `<agenthub-runtime> --state-dir <root> --socket <path>
// daemon`. The root is part of the match because one machine may serve several state trees, and a daemon
// for another root is not this run's to stop.
//
// WHAT IS KEPT: nothing here writes the room. The room's record (spec.json), its seats' directories, their
// worktrees and their transcripts stay where they are; the calling run then reads the room and starts the
// seats that are not running, exactly as any other `up`.
package clientlifecycle

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientoffpeak"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// respawnWindow is how long a signalled daemon has to exit before `up --respawn` refuses. A daemon that
// outlives it is reported rather than killed harder and started beside: two daemons on one socket is the
// state this whole step exists to avoid.
var respawnWindow = 10 * time.Second

// daemonPattern matches the runtime daemon serving ONE state root, in the argv runtimeDaemonCommand
// builds. It is anchored on the binary's name because pgrep -f matches the whole command line, which
// contains every seat's argv too.
func daemonPattern(stateDir string) string {
	return "^[^ ]*agenthub-runtime --state-dir " + regexp.QuoteMeta(stateDir) + "( |$)"
}

// respawnDaemon stops the daemon serving stateDir, if one is running, and waits until it is gone. It
// answers whether the run may go on: false means the daemon could not be signalled or did not exit, and
// the caller must not start a second one.
func respawnDaemon(stateDir string, stdout, stderr io.Writer) bool {
	pattern := daemonPattern(stateDir)
	// A PROBE THAT COULD NOT RUN IS NOT "the daemon is not running" (7.12). The empty-pids branch below is
	// what `up --respawn` used to walk on a missing pgrep, reaching `return true` and starting the second
	// daemon this whole step exists to prevent; the refusal is on stderr and touches nothing.
	pids, err := clientservices.Live(pattern)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: the runtime daemon was not touched: %v\n", err)
		return false
	}
	if len(pids) == 0 {
		fmt.Fprintln(stdout, "daemon: was not running")
		return true
	}
	fmt.Fprintf(stdout, "daemon: stopping %v so the installed binary serves %s\n", pids, stateDir)
	stopped, err := stopPattern(pattern)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: the runtime daemon was not touched: %v\n", err)
		return false
	}
	if !stopped {
		fmt.Fprintf(stderr, "4genteam up: the runtime daemon %v did not accept the stop signal; "+
			"refusing to start a second daemon for %s\n", pids, stateDir)
		return false
	}
	deadline := time.Now().Add(respawnWindow)
	for time.Now().Before(deadline) {
		live, err := clientservices.Live(pattern)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam up: the runtime daemon was not touched: %v\n", err)
			return false
		}
		if len(live) == 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	live, err := clientservices.Live(pattern)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam up: the runtime daemon was not touched: %v\n", err)
		return false
	}
	fmt.Fprintf(stderr, "4genteam up: the runtime daemon %v is still running %s after the stop signal; "+
		"refusing to start a second daemon for %s\n", live, respawnWindow, stateDir)
	return false
}

// A REPLACEMENT MUST OUTLIVE ITS CALLER. `up --respawn` stops agent, and a seat runs this verb from a
// process agent owns: the seat that called it is signalled by its own command mid-run, so the daemon is
// replaced and the seats and the agent the run was about to start never are (measured 2026-10-20
// 17:29:38Z: daemon SIGTERM at 17:30:02Z, the caller gone, four seats and agent left down; the same shape
// on 10-18 21:38Z and 10-19 05:06Z). So the verb RE-EXECS ITSELF in a NEW SESSION with its own log before
// it signals anything, prints that log's path, and returns at once: the caller gets an answer and the
// restart keeps running after the caller is gone.
//
// The re-exec is marked by this variable rather than by a flag, because it is not an argument a person
// writes: a caller that passed it by hand would be asking for a restart that does nothing.
const respawnDetachedEnv = "FOURGENTEAM_UP_RESPAWN_DETACHED"

// respawnLogPath is where the detached run's own output goes: beside the state it serves, the way the
// runtime daemon's log is.
func respawnLogPath(stateDir string) string { return filepath.Join(stateDir, "up-respawn.log") }

// respawnArgv is the detached run's argv: this verb, this room, and every flag this run was given. The
// state root is named EXPLICITLY because the child is a new session rather than a continuation of the
// caller's shell, and the root must not be re-derived from an environment the caller set for itself.
var respawnArgv = func(name, teamDir, stateDir string, allowPeak, roomOnly bool) []string {
	argv := []string{"up", name}
	if teamDir != "" {
		argv = append(argv, "--team", teamDir)
	}
	argv = append(argv, "--state-root", stateDir)
	if allowPeak {
		argv = append(argv, clientoffpeak.FlagAllowPeak)
	}
	if roomOnly {
		argv = append(argv, "--room-only")
	}
	return append(argv, "--respawn")
}

// respawnExecutable is the binary the detached run re-execs: this one. A variable so the suite can point it
// at a shell and read the child's own session and output rather than starting a second client.
var respawnExecutable = os.Executable

// detachRespawnImpl starts argv in a NEW SESSION with both streams on respawnLogPath, and returns at once.
// The child is not waited on - it is the restart, and it must outlive this process. A start that fails is
// an error and nothing has been signalled yet.
var detachRespawn = detachRespawnImpl

func detachRespawnImpl(stateDir string, argv []string) (string, error) {
	exe, err := respawnExecutable()
	if err != nil {
		return "", fmt.Errorf("this executable cannot be named: %w", err)
	}
	if err := RefuseSelfExecUnderTest(exe); err != nil {
		return "", err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	logPath := respawnLogPath(stateDir)
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer log.Close()
	child := exec.Command(exe, argv...)
	child.Stdout, child.Stderr = log, log
	child.Env = append(os.Environ(), respawnDetachedEnv+"=1")
	// Setsid: its own session and process group, so a signal aimed at the caller's group - or the caller's
	// session being torn down - is not the restart's.
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return "", err
	}
	return logPath, child.Process.Release()
}
