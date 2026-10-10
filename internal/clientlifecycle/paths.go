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

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// SupervisorPattern is the argv fragment that identifies this client's compaction supervisor, read from
// the ONE pattern table the health report also uses (clientservices.Pattern): `status` and the report must
// never be able to disagree about which process is the supervisor, which two definitions of the same
// command line eventually would. The Python matched "agenthub_client.compact --rig <rig>", a Python module
// name; the Go supervisor is this binary's own `compact-run`.
func SupervisorPattern(rig string) string {
	return clientservices.Pattern(clientservices.Compact, rig)
}

// LogDir is where the lifecycle verbs keep their logs. It DELEGATES to clientservices.LogDir because the
// log directory has ONE computation in the client: the supervisor's log, the bridge's log, the heartbeat
// files and alerts.log must not be able to land in two places, which is what a second copy of the XDG
// rule here would eventually do.
func LogDir() (string, error) { return clientservices.LogDir() }

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
