// The paths the lifecycle verbs own: where the supervisor's log goes, the argv fragment that names a
// running supervisor, and the args the client needs an external program for.
//
// The Python derived all of these from its own checkout (paths.py: CLIENT_DIR/"logs"). A compiled
// binary has no checkout, so the log directory comes from the state root its caller RESOLVED once per
// run (clientenv.ResolveStateRoot), and every external program is resolved by name.
package clientlifecycle

import (
	"path/filepath"

	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// SupervisorPattern is the argv fragment that identifies this client's compaction supervisor, read from
// the ONE pattern table the health report also uses (clientservices.Pattern): `status` and the report must
// never be able to disagree about which process is the supervisor, which two definitions of the same
// command line eventually would. The Python matched "agenthub_client.compact --room <rig>", a Python module
// name; the Go supervisor is this binary's own `compact-run`.
func SupervisorPattern(rig string) string {
	return clientservices.Pattern(clientservices.Compact, rig)
}

// LogDir is where the lifecycle verbs keep their logs. It DELEGATES to clientservices.LogDir because the
// log directory has ONE computation in the client: the supervisor's log, the bridge's log, the heartbeat
// files and alerts.log must not be able to land in two places, which is what a second copy of the rule
// here would eventually do. The root is the caller's resolved root, so a verb pointed at another tree
// writes its logs there.
func LogDir(root string) string { return clientservices.LogDir(root) }

// LogPath is the supervisor's log for one rig.
func LogPath(rig, root string) string {
	return filepath.Join(LogDir(root), "compact-supervisor-"+rig+".log")
}

// MessagesLogPath is the seat-message deliverer's log for one rig: what every pass printed, so a message
// that would not land stays readable after the fact rather than only in the operator's scrollback.
func MessagesLogPath(rig, root string) string {
	return filepath.Join(LogDir(root), "messages-"+rig+".log")
}

// BridgeLogPath is the status bridge's log.
func BridgeLogPath(root string) string {
	return filepath.Join(LogDir(root), "bridge.log")
}
