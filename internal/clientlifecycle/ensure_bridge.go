package clientlifecycle

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// verbsWithoutBridge are the verbs that must not start the status bridge: the ones that only print,
// the verb that IS the bridge, the one that stops it, the one `up` starts it from, and the seat-side
// checker, which runs inside a seat on every tool call and must stay as cheap as it is now.
var verbsWithoutBridge = map[string]bool{
	"help": true, "-h": true, "--help": true, "version": true,
	"bridge": true, "stop": true, "up": true, "seatcheck": true,
}

// bridgeRunning is pgrep -f in one place, a variable so a test needs no process table. A missing pgrep
// reads as running: the client then starts nothing rather than a second bridge on every verb.
var bridgeRunning = func() bool {
	path, err := exec.LookPath("pgrep")
	if err != nil {
		return true
	}
	return exec.Command(path, "-f", BridgePattern).Run() == nil
}

// EnsureBridge starts the status bridge, detached, when a verb that needs the cloud runs on a machine
// whose bridge is not running. The machine therefore reports whenever the client is in use; nobody
// runs a service or answers whether it should run. Silent when there is nothing to do, one stderr line
// when the start fails, and never an error to the verb: a verb that reads the cloud must not die
// because the bridge could not start.
func EnsureBridge(args []string, stderr io.Writer) {
	if len(args) == 0 || verbsWithoutBridge[args[0]] || clientcmd.HelpRequested(args[1:]) {
		return
	}
	if os.Getenv("AGENTHUB_URL") == "" || os.Getenv("AGENTHUB_TOKEN") == "" || bridgeRunning() {
		return
	}
	logPath, err := BridgeLogPath()
	if err != nil {
		fmt.Fprintf(stderr, "4genteam: the bridge is not running and cannot start: %v\n", err)
		return
	}
	if _, err := spawnChild([]string{"bridge", "run"}, logPath); err != nil {
		fmt.Fprintf(stderr, "4genteam: could not start the bridge: %v\n", strings.TrimSpace(err.Error()))
	}
}
