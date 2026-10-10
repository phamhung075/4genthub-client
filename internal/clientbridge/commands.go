package clientbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/secretscan"
)

// Commands is what the dispatcher appends: one verb, `bridge`.
//
// The Python bridge was three top-level commands (run, once, install-service); the Go client
// has one verb per package, so the three are sub-verbs here. Registering is an append in main.go by the
// dispatcher's owner, never an edit of this package's own file into theirs.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{bridgeCommand{}}
}

type bridgeCommand struct{}

func (bridgeCommand) Name() string    { return "bridge" }
func (bridgeCommand) Summary() string { return "report this machine and its rigs to the cloud" }

// NeedsRig is true because run and once read `rig ps`; install-service ignores the tool
// it is handed, which is cheaper than a second verb.
func (bridgeCommand) NeedsRig() bool { return true }

func (c bridgeCommand) Run(ctx context.Context, rig *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		bridgeUsage(stderr)
		return clientcmd.ExitUsage
	}
	switch args[0] {
	case "run":
		return bridgeRun(ctx, rig, args[1:], stdout, stderr, false)
	case "once":
		return bridgeOnce(ctx, rig, args[1:], stdout, stderr)
	case "install-service":
		return bridgeInstallService(stdout, stderr)
	case "help", "-h", "--help":
		bridgeUsage(stdout)
		return clientcmd.ExitOK
	default:
		_, _ = fmt.Fprintf(stderr, "4genteam bridge: unknown subcommand %q\n", args[0])
		bridgeUsage(stderr)
		return clientcmd.ExitUsage
	}
}

func bridgeUsage(w io.Writer) {
	_, _ = io.WriteString(w, "4genteam bridge <subcommand>\n\n"+
		"  run [--interval SECONDS] [--machine-id ID] [--once]   report on a loop (default 20s)\n"+
		"  once [--print] [--machine-id ID]                      report once; --print dumps the payload\n"+
		"  install-service                                       print the systemd unit\n")
}

// bridgeFlags is the small parser these sub-verbs need. The Python used argparse; here a flag is
// either `--name value` or `--name`, and a bare value is a usage error rather than silently ignored.
func bridgeFlags(args []string, accepted map[string]bool, switches map[string]bool) (map[string]string, map[string]bool, error) {
	values := map[string]string{}
	set := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name := args[i]
		if !strings.HasPrefix(name, "--") {
			return nil, nil, fmt.Errorf("unexpected argument %q", name)
		}
		name = strings.TrimPrefix(name, "--")
		if switches[name] {
			set[name] = true
			continue
		}
		if !accepted[name] {
			return nil, nil, fmt.Errorf("unknown flag --%s", name)
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("--%s needs a value", name)
		}
		i++
		values[name] = args[i]
	}
	return values, set, nil
}

func machineIDFrom(values map[string]string) (string, error) {
	if id := values["machine-id"]; id != "" {
		if !namePattern.MatchString(id) {
			return "", fmt.Errorf("invalid machine id %q", id)
		}
		return id, nil
	}
	return HostMachineID(), nil
}

// HostMachineID is this machine's id when none is named: its hostname, made acceptable to the server.
func HostMachineID() string {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return SanitizeMachineID(host)
}

func bridgeRun(ctx context.Context, rig *clientcmd.Rig, args []string, stdout, stderr io.Writer, onceOnly bool) int {
	if clientcmd.HelpRequested(args) {
		bridgeUsage(stdout)
		return clientcmd.ExitOK
	}
	values, switches, err := bridgeFlags(args,
		map[string]bool{"interval": true, "machine-id": true}, map[string]bool{"once": true})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge run: %v\n", err)
		return clientcmd.ExitUsage
	}
	id, err := machineIDFrom(values)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge run: %v\n", err)
		return clientcmd.ExitUsage
	}
	interval := DefaultInterval
	if raw := values["interval"]; raw != "" {
		if _, err := fmt.Sscanf(raw, "%f", &interval); err != nil || interval <= 0 {
			_, _ = io.WriteString(stderr, "4genteam bridge run: --interval must be a positive number\n")
			return clientcmd.ExitUsage
		}
	}
	send, err := senderFromEnv()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge run: %v\n", err)
		return clientcmd.ExitUsage
	}
	pins, err := defaultPinsDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge run: %v\n", err)
		return clientcmd.ExitUsage
	}
	state, err := DefaultStatePath()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge run: %v\n", err)
		return clientcmd.ExitUsage
	}
	bridge := NewBridge(id, time.Duration(interval*float64(time.Second)), send,
		secretscan.KnownValuesFromEnv(os.Environ()), pins, state, noteTo(stderr))
	_, _ = fmt.Fprintf(stderr, "4genteam bridge: reporting as %s\n", id)
	return bridge.Run(ctx, rig, onceOnly || switches["once"])
}

// bridgeOnce reports a single cycle. --print builds the payload and dumps it WITHOUT sending, which is
// the instrument the parity test uses against the Python bridge: the dump carries local_record, the
// report does not, and the difference is exactly the four-versus-five fields.
func bridgeOnce(ctx context.Context, rig *clientcmd.Rig, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		bridgeUsage(stdout)
		return clientcmd.ExitOK
	}
	values, switches, err := bridgeFlags(args, map[string]bool{"machine-id": true}, map[string]bool{"print": true})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge once: %v\n", err)
		return clientcmd.ExitUsage
	}
	id, err := machineIDFrom(values)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge once: %v\n", err)
		return clientcmd.ExitUsage
	}
	known := secretscan.KnownValuesFromEnv(os.Environ())
	pins, err := defaultPinsDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge once: %v\n", err)
		return clientcmd.ExitUsage
	}
	state, err := DefaultStatePath()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge once: %v\n", err)
		return clientcmd.ExitUsage
	}
	if switches["print"] {
		bridge := NewBridge(id, time.Duration(DefaultInterval*float64(time.Second)), nil,
			known, pins, state, noteTo(stderr))
		payload := bridge.BuildPayload(rig)
		payload.LocalRecord = bridge.LocalRecord(payload.Seats)
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "4genteam bridge once: cannot encode the payload: %v\n", err)
			return clientcmd.ExitRemote
		}
		_, _ = fmt.Fprintf(stdout, "%s\n", encoded)
		return clientcmd.ExitOK
	}
	send, err := senderFromEnv()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge once: %v\n", err)
		return clientcmd.ExitUsage
	}
	bridge := NewBridge(id, time.Duration(DefaultInterval*float64(time.Second)), send,
		known, pins, state, noteTo(stderr))
	return bridge.Run(ctx, rig, true)
}

// bridgeInstallService prints the systemd unit. The Python unit named a Python interpreter and a
// script; this one names THE BINARY ITSELF and the sub-verb, which is the whole point of the
// consolidation: one artefact on the machine, no interpreter to be present.
func bridgeInstallService(stdout, stderr io.Writer) int {
	binary, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge install-service: cannot resolve this binary: %v\n", err)
		return clientcmd.ExitRemote
	}
	// The unit reads THE SAME file the client reads, named by the same code, so the service and the
	// runtime cannot drift apart. The phase-1 move carried a PYTHON-ERA file name here - one that
	// nothing ever writes - so an installed unit came up with no credentials while still looking
	// healthy, which is worse than refusing to install.
	envFile, err := clientenv.DefaultPath()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "4genteam bridge install-service: cannot resolve the client's env file: %v\n", err)
		return clientcmd.ExitRemote
	}
	_, _ = fmt.Fprintf(stdout, `[Unit]
Description=4genthub OpenRig status bridge
After=network-online.target

[Service]
EnvironmentFile=%s
ExecStart=%s bridge run
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, envFile, binary)
	return clientcmd.ExitOK
}

// noteTo turns a writer into the bridge's note sink: every line is prefixed and flushed, so an
// operator reading the journal sees the same sentences the Python printed.
func noteTo(stderr io.Writer) func(string) {
	return func(message string) {
		_, _ = fmt.Fprintf(stderr, "openrig-bridge: %s\n", message)
	}
}

// statusClient is the seam the sender's wire contract is pinned through: a test replaces it with a
// client whose Transport answers in-process, so the path, the bearer header and the status handling
// are exercised with NO network at all. This is a push path, and the suite cannot run it live.
var statusClient = &http.Client{Timeout: SendTimeout}

// senderFromEnv builds the sender from the user credential. A missing one is a usage error, because a
// bridge without a token cannot report and must not start quietly.
func senderFromEnv() (Sender, error) {
	baseURL, token := os.Getenv("AGENTHUB_URL"), os.Getenv("AGENTHUB_TOKEN")
	if baseURL == "" || token == "" {
		return nil, fmt.Errorf("AGENTHUB_URL and AGENTHUB_TOKEN must be set")
	}
	client := statusClient
	endpoint := strings.TrimRight(baseURL, "/") + statusPath
	return func(body []byte) (int, []byte) {
		request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return 0, []byte(err.Error())
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return 0, []byte(err.Error())
		}
		defer func() { _ = response.Body.Close() }()
		// The body is read on an error status too: it is small and bounded, and a caller naming what
		// the server said is better than a bare code.
		answer, _ := io.ReadAll(io.LimitReader(response.Body, MaxResponseSize))
		return response.StatusCode, answer
	}, nil
}

// The three locations the Python client used, named here so a test can point them at a scratch tree.
// Both resolve from the ACCOUNT home's seat store and REFUSE rather than guessing a relative path - a
// relative default would follow the working directory, and $HOME is the seat's own state directory.
func defaultPinsDir() (string, error) {
	return clientenv.ResolveSeatStore("")
}

// DefaultStatePath is the sync record the bridge keeps beside the pins.
func DefaultStatePath() (string, error) {
	store, err := clientenv.ResolveSeatStore("")
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(store), "bridge-sync.json"), nil
}
