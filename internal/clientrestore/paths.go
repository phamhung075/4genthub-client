// Package clientrestore puts a stopped room back. Two verbs live here, and they are one package because
// the second one's action IS the first one's:
//
//   - `continue` restores a room from its latest snapshot and briefs its seats.
//   - `watchdog` notices when a room's tmux seats have vanished and runs that same restore.
//
// PORTED FROM the room-continue.sh and room-watchdog.sh shell pair. The shell pair had the watchdog invoke the continue SCRIPT
// as a subprocess; here it calls the same function, so "the watchdog restores it" and "a resume restores
// it" can never drift apart.
//
// THREE THINGS THE SHELL DID ARE NOT PORTED, each because the client forbids it or does it better:
//
//   - the watchdog counted running seats by piping `rig ps --json` through python3 (encoding/json now:
//     the client has no Python at all);
//   - both scripts reached `rig` and `tmux` through a shell (`exec.Command` with an argument list now,
//     which is also why the continue script's warning that a brief must not contain backticks,
//     apostrophes or `$` no longer applies: the text is sent as written);
//   - the shell restored a room whose tmux sessions were missing without asking what model its seats run,
//     so a resumed room could start DeepSeek work at the peak rate (clientlifecycle.PeakGate now runs
//     before every restore in this package, the same gate `up` and `compact` obey).
package clientrestore

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// resolveRoot is the verb-side resolution of the client's ONE root: the `--state-root` flag when the verb
// took one, else this machine's own. The home it reads is this package's seam, so a test never touches a
// real home - and the root is resolved ONCE per run, which is what keeps the briefs, the logs, the
// watchdog's server id and the tmux server this verb asks from disagreeing.
func resolveRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	home, err := realHome()
	if err != nil || home == "" {
		return "", &clientcmd.CodedError{Message: clientenv.StateRootMessage, Code: clientcmd.ExitUsage}
	}
	return clientenv.StateSubdir(home, ""), nil
}

// stateSubdir is the ONE place the state subdirectories are derived. The root is the caller's resolved
// root (resolveRoot), so a verb pointed at another tree reads and writes that tree and nothing else.
func stateSubdir(root, name string) string {
	return filepath.Join(root, name)
}

// briefDir, logDir and stateDir are <root>/{briefs,logs,state}.
func briefDir(root string) string { return stateSubdir(root, "briefs") }
func logDir(root string) string   { return stateSubdir(root, "logs") }
func stateDir(root string) string { return stateSubdir(root, "state") }

// The seams this package's tests replace - package-level variables rather than an interface added for
// testing, which is the repo's rule.
var (
	// realHome is the account's home, from the passwd entry rather than $HOME.
	realHome = clientenv.RealHome
	// tmuxRead is the ONE tmux read in this package: the session names of the server under the root it is
	// handed, as `tmux ls -F '#{session_name}'` printed them (the command's error - a missing server -
	// reported to the caller).
	tmuxRead = tmuxSessionNames
	// now stamps the log lines, which the shell wrote as `date -Is` (RFC3339 with the local offset).
	now = time.Now
	// execLookPath and execSelfPath are the two lookups the watch wall needs: whether herdr is there, and
	// this binary's own path. The shell tested `command -v herdr` and `command -v 4genteam` for the same
	// three things, so a machine missing either gets the skip it logged then.
	execLookPath = exec.LookPath
	execSelfPath = os.Executable
	// execCommand starts the watch wall - this same binary, with the watch arguments.
	execCommand = exec.Command
)

// The two log file names are the scripts' own: the owner's existing logs, and any habit or tail built on
// them, point at exactly these paths.
const (
	continueLogName = "room-continue.log"
	watchdogLogName = "room-watchdog.log"
)
