package clientsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

const (
	// respawnPollSeconds is how often the dead reading is re-taken while --after-seconds runs out.
	respawnPollSeconds = 5.0
	// defaultRespawnAfterSeconds is that deadline: a just-launched seat reads EXACTLY like a dead one
	// until its runtime hook attaches, so a single reading is not evidence of anything.
	defaultRespawnAfterSeconds = 30.0
	// defaultRespawnReason is the audit reason recorded on the seat.fresh_launched event.
	defaultRespawnReason = "agent died outside `rig seat stop` (respawn)"
)

const respawnUsage = `usage: 4genteam sync respawn ROOM SEAT [--after-seconds SECONDS] [--reason TEXT]

Respawn a seat whose agent died outside ` + "`rig seat stop`" + `. The seat must READ dead for
--after-seconds (default 30s) before anything is launched, because a seat that has just started reads
the same way until its runtime hook attaches.
`

// rigResult is one `rig` invocation for its output. A non-zero exit is NOT thrown away: `rig seat
// launch` can start the occupant and still exit non-zero, so the caller decides on the seat's state
// rather than on the code.
type rigResult struct {
	stdout string
	stderr string
	err    error
}

// runRigCapture is where THIS file starts `rig`, beside the package's runRig (connectorverb.go), and
// deliberately not the same seam: the package one returns stdout only and folds a non-zero exit into
// its error message, and that is not enough here. `rig seat launch` can start the occupant and still
// exit non-zero, so respawn decides on the seat's own state afterwards and needs the raw stderr (its
// last line is what an operator is shown) with the exit beside it. A var so a test drives the verb
// without a rig.
var runRigCapture = func(args ...string) rigResult {
	path, err := exec.LookPath("rig")
	if err != nil {
		return rigResult{err: errors.New("`rig` executable not found on PATH")}
	}
	cmd := exec.Command(path, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	return rigResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// RunRespawnVerb is `sync respawn`: the deliberate primitive for a seat whose agent died outside
// OpenRig's own stop, since OpenRig has no automatic trigger for that.
func RunRespawnVerb(_ context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			_, _ = io.WriteString(stdout, respawnUsage)
			return clientcmd.ExitOK
		}
	}
	room, seat, after, reason, err := parseRespawnArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitUsage
	}
	return respawn(room, seat, after, reason, stdout, stderr)
}

func parseRespawnArgs(args []string) (room, seat string, after float64, reason string, err error) {
	var positionals []string
	after, reason = defaultRespawnAfterSeconds, defaultRespawnReason
	needValue := func(flag string, index int) error {
		if index+1 >= len(args) {
			return fmt.Errorf("%s needs a value", flag)
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--after-seconds":
			if err := needValue(arg, i); err != nil {
				return "", "", 0, "", err
			}
			i++
			after, err = strconv.ParseFloat(args[i], 64)
			if err != nil {
				return "", "", 0, "", fmt.Errorf("invalid --after-seconds: %s", args[i])
			}
		case arg == "--reason":
			if err := needValue(arg, i); err != nil {
				return "", "", 0, "", err
			}
			i++
			reason = args[i]
		case strings.HasPrefix(arg, "--after-seconds="):
			after, err = strconv.ParseFloat(strings.TrimPrefix(arg, "--after-seconds="), 64)
			if err != nil {
				return "", "", 0, "", fmt.Errorf("invalid --after-seconds: %s", arg)
			}
		case strings.HasPrefix(arg, "--reason="):
			reason = strings.TrimPrefix(arg, "--reason=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			return "", "", 0, "", fmt.Errorf("unknown argument: %s", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 2 {
		return "", "", 0, "", errors.New("missing argument: room, seat")
	}
	return positionals[0], positionals[1], after, reason, nil
}

func respawn(room, seat string, after float64, reason string, stdout, stderr io.Writer) int {
	room, err := ValidateName("room", room)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitUsage
	}
	seat, err = ValidateName("seat", seat)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitUsage
	}
	deadline := time.Now().Add(time.Duration(after * float64(time.Second)))
	for {
		node, err := seatNode(room, seat)
		if err != nil {
			fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
			return clientcmd.ExitRemote
		}
		if node == nil {
			fmt.Fprintf(stderr, "4genteam sync respawn: OpenRig lists no node %s.%s\n", room, seat)
			return clientcmd.ExitUsage
		}
		if !agentIsGone(node) {
			fmt.Fprintf(stderr,
				"4genteam sync respawn: %s.%s is not in the dead-agent state (sessionStatus=%s, "+
					"agentActivity=%s); refusing to respawn\n",
				room, seat, jsonText(node["sessionStatus"]), jsonText(node["agentActivity"]))
			return clientcmd.ExitUsage
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Duration(respawnPollSeconds * float64(time.Second)))
	}
	session, err := canonicalSession(room, seat)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam sync respawn: %v\n", err)
		return clientcmd.ExitRemote
	}
	if session == "" {
		session = room + "." + seat
	}
	result := runRigCapture("seat", "launch", session, "--fresh", "--stop", "--reason", reason)
	if result.err != nil {
		message := lastLine(result.stderr)
		if message == "" {
			message = lastLine(result.stdout)
		}
		if message == "" {
			message = result.err.Error()
		}
		// The launch may have started the occupant and still exited non-zero, so the seat's state
		// decides - an automated caller must not read a successful respawn as a failure.
		node, err := seatNode(room, seat)
		if err != nil {
			node = nil
		}
		if agentIsGone(node) {
			fmt.Fprintf(stderr, "4genteam sync respawn: rig seat launch reported: %s\n", message)
			return clientcmd.ExitRemote
		}
		fmt.Fprintf(stderr, "respawned %s.%s; rig seat launch warned: %s\n", room, seat, message)
		return clientcmd.ExitOK
	}
	if text := strings.TrimSpace(result.stdout); text != "" {
		fmt.Fprintln(stdout, text)
	}
	return clientcmd.ExitOK
}

// seatNode is the `rig ps` node for room.seat, or nil when OpenRig does not list it.
func seatNode(room, seat string) (map[string]any, error) {
	result := runRigCapture("ps", "--json", "--nodes", "--rig", room)
	if result.err != nil && strings.TrimSpace(result.stdout) == "" {
		return nil, fmt.Errorf("rig ps failed: %w", result.err)
	}
	nodes, err := parseRigNodes(result.stdout)
	if err != nil {
		return nil, fmt.Errorf("rig ps returned invalid JSON: %w", err)
	}
	for _, node := range nodes {
		if logicalSeat(node) == seat {
			return node, nil
		}
	}
	return nil, nil
}

// parseRigNodes reads both shapes `rig ps` has been seen to answer: a bare list, and an object whose
// `items` is the list.
func parseRigNodes(text string) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
		return list, nil
	}
	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, err
	}
	return envelope.Items, nil
}

// logicalSeat is the seat part of a node's logical id: `room.seat` keeps everything after the first
// dot, so a seat whose own name carries one is still compared whole.
func logicalSeat(node map[string]any) string {
	logical, _ := node["logicalId"].(string)
	parts := strings.SplitN(logical, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// agentIsGone is the reading a dead agent leaves: the session is up, the runtime hook is not.
func agentIsGone(node map[string]any) bool {
	if node == nil {
		return true
	}
	status, _ := node["sessionStatus"].(string)
	if status == "stopped" || status == "exited" {
		return false
	}
	activity, _ := node["agentActivity"].(map[string]any)
	state, _ := activity["state"].(string)
	reason, _ := activity["reason"].(string)
	return state == "unknown" && reason == "no_runtime_hook"
}

// canonicalSession is the live canonical session name of a seat, or "" when OpenRig lacks it.
func canonicalSession(room, seat string) (string, error) {
	result := runRigCapture("ps", "--json", "--nodes", "--rig", room)
	if result.err != nil && strings.TrimSpace(result.stdout) == "" {
		return "", fmt.Errorf("rig ps failed: %w", result.err)
	}
	nodes, err := parseRigNodes(result.stdout)
	if err != nil {
		return "", fmt.Errorf("rig ps returned invalid JSON: %w", err)
	}
	for _, node := range nodes {
		name, _ := node["canonicalSessionName"].(string)
		named, _ := node["rigName"].(string)
		if named == "" {
			named = room
		}
		if name != "" && named == room && logicalSeat(node) == seat {
			return name, nil
		}
	}
	return "", nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// jsonText renders a node field for the refusal, so an operator sees exactly what the reading was.
func jsonText(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}
