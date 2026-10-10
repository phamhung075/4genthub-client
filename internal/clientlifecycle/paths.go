// The paths the lifecycle verbs own: where the supervisor's log goes, the argv fragment that names a
// running supervisor, and the args the client needs an external program for.
//
// The Python derived all of these from its own checkout (paths.py: CLIENT_DIR/"logs", rust/forcecompact/
// target/release/forcecompact). A compiled binary has no checkout, so the log directory comes from the
// XDG state location under the REAL user's home, and every external program is resolved by name.
package clientlifecycle

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// SupervisorPattern is the argv fragment that identifies this client's compaction supervisor. The
// Python matched "agenthub_client.compact --rig <rig>", a Python module name; the Go supervisor is this
// binary's own `compact-run`, so this is what `status` finds and `stop` kills.
func SupervisorPattern(rig string) string { return "compact-run --rig " + rig }

// BridgePattern identifies the status bridge's process.
const BridgePattern = "bridge run"

// LogDir is where the lifecycle verbs keep their logs.
func LogDir() (string, error) {
	home, err := clientenv.RealHome()
	if err != nil {
		return "", err
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "4genthub", "logs"), nil
}

// LogPath is the supervisor's log for one rig.
func LogPath(rig string) (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "compact-supervisor-"+rig+".log"), nil
}

// BridgeLogPath is the status bridge's log.
func BridgeLogPath() (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bridge.log"), nil
}

// ForcecompactPath is the helper that force-compacts an omp seat, which needs CAP_SYS_PTRACE. The
// Python built it from the checkout with cargo (and warned about `setcap`); this client does not build
// the helper, so the path is named by 4GENTEAM_FORCECOMPACT or found on PATH, and an empty result means
// the machine cannot force-compact an omp seat at all - which the caller says out loud.
func ForcecompactPath() string {
	if named := os.Getenv("4GENTEAM_FORCECOMPACT"); named != "" {
		return named
	}
	path, err := exec.LookPath("forcecompact")
	if err != nil {
		return ""
	}
	return path
}
