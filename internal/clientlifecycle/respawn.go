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
	"regexp"
	"time"

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
