// Package clientrestore puts a stopped rig back. Two verbs live here, and they are one package because
// the second one's action IS the first one's:
//
//   - `continue` restores a rig from its latest snapshot and briefs its seats.
//   - `watchdog` notices when a rig's tmux seats have vanished and runs that same restore.
//
// PORTED FROM the rig-continue.sh and rig-watchdog.sh shell pair. The shell pair had the watchdog invoke the continue SCRIPT
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
//   - the shell restored a rig whose tmux sessions were missing without asking what model its seats run,
//     so a resumed room could start DeepSeek work at the peak rate (clientlifecycle.PeakGate now runs
//     before every restore in this package, the same gate `up` and `compact` obey).
package clientrestore

import (
	"os"
	"os/exec"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// stateSubdir is the ONE place the three directories are derived, under the REAL home
// (clientenv.RealHome): a seat is launched with HOME pointed at the seat's own state directory, so
// resolving these from $HOME would make a machine-level path depend on which seat happens to run the
// client.
func stateSubdir(name string) (string, error) {
	home, err := realHome()
	if err != nil {
		return "", err
	}
	return clientenv.StateSubdir(home, name), nil
}

// briefDir, logDir and stateDir are ~/.config/4genthub/{briefs,logs,state}. They are functions
// rather than package variables because the home can fail to resolve, and a relative path here would
// make the briefs a function of the working directory.
func briefDir() (string, error) { return stateSubdir("briefs") }
func logDir() (string, error)   { return stateSubdir("logs") }
func stateDir() (string, error) { return stateSubdir("state") }

// The seams this package's tests replace - package-level variables rather than an interface added for
// testing, which is the repo's rule.
var (
	// realHome is the account's home, from the passwd entry rather than $HOME.
	realHome = clientenv.RealHome
	// tmuxRead is the ONE tmux read in this package: the session names, as `tmux ls -F '#{session_name}'`
	// printed them, with the command's error (a missing server) reported to the caller.
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
	continueLogName = "rig-continue.log"
	watchdogLogName = "rig-watchdog.log"
)
