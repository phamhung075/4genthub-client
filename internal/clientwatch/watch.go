// Package clientwatch is the `watch` verb group: the live, READ-ONLY view of a room.
//
// It renders what every seat of a rig is doing - each tool call and its result, the agent's own
// reasoning, what it says and what it is told - from the seats' session logs, and it never types into
// a seat from the feed. docs/WATCH.md and skills/watch-team/SKILL.md are the contract this code must
// make true; the Python client's watch.py is the behaviour it was ported from.
//
// Four deliberate departures from the Python, all recorded in the phase report:
//
//   - `--rig` is REQUIRED. The Python defaulted it to the owner's own rig name, and the mission
//     forbids an owner default; the doc already says "there is no default room".
//   - the state root is `--state-root`, else $OPENRIG_STATE_ROOT, else the machine's own
//     ~/.openrig/state/omp (clientenv.DefaultStateRoot) - the first place docs/WATCH.md says the feed
//     looks - and a machine where that root is not there, or is without herdr, exits non-zero with ONE
//     message naming the path it tried, instead of failing on a missing owner path.
//   - every external process goes through ONE injectable seam (`execCommand`), so the tests need no
//     herdr, no tmux, no rig and no seat, and the refusals are testable by name.
//   - the pane commands run this binary rather than a Python interpreter and module.
package clientwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// Commands is what the dispatcher registers from this package: the umbrella `watch` plus the four
// bare verbs the Python client also accepts as top-level commands (`4genteam feed`, `4genteam grid`,
// `4genteam inputs`, `4genteam input`). The dispatcher keys on Name alone and has no alias table, so
// the aliasing lives here as separate commands rather than as an argv rewrite.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{
		watchCommand{},
		bareCommand{name: "grid", summary: "open the seat grid alone"},
		bareCommand{name: "feed", summary: "one merged stream of every seat, for a plain terminal or ssh"},
		bareCommand{name: "inputs", summary: "open or hide an input pane under each seat pane"},
		bareCommand{name: "input", summary: "read lines and send each to one seat"},
	}
}

// execCommand is the ONE seam every external process goes through. Replacing it is what makes the
// tests able to run without herdr, tmux, rig or a seat - and what lets the refusals be checked by
// name. It is exec.Command with an argument list, never a shell string.
var execCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// lookPath is the second half of the same seam: whether a tool exists at all.
var lookPath = exec.LookPath

// stateRootEnv names the override for the omp state root the logs are read from.
const stateRootEnv = "OPENRIG_STATE_ROOT"

// shared are the flags every reading verb takes. `--rig` has NO default: the doc says there is no
// default room, and an owner's rig name must never be baked in.
type shared struct {
	rig       string
	back      int
	width     int
	lines     int
	detail    bool
	cols      int
	stateRoot string
	seats     []string
}

func (s shared) gridCommandArgs() string {
	return fmt.Sprintf("--back %d --width %d --lines %d", s.back, s.width, s.lines) +
		detailFlag(s.detail)
}

func detailFlag(detail bool) string {
	if detail {
		return " --detail"
	}
	return ""
}

// defaultStateRoot is the third clause: where OpenRig keeps its per-seat state on THIS machine
// (clientenv.DefaultStateRoot, resolved from the real home, so it is the same inside a seat and out).
// It is a variable only so a test can replace it: resolving the real home in a test would make the
// result depend on the machine running it, which the offline test rule forbids.
var defaultStateRoot = clientenv.DefaultStateRoot

// resolveStateRoot is `--state-root`, else $OPENRIG_STATE_ROOT, else clientenv.DefaultStateRoot().
// A root that is not a directory is the caller's to fix, and the message says which path it tried.
func resolveStateRoot(flagValue string) (string, error) {
	root := flagValue
	if root == "" {
		root = os.Getenv(stateRootEnv)
	}
	if root == "" {
		root = defaultStateRoot()
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", unavailableError(fmt.Sprintf("the omp state root is not there: %s", root))
	}
	if !info.IsDir() {
		return "", unavailableError(fmt.Sprintf("the omp state root is not a directory: %s", root))
	}
	return root, nil
}

// requireHerdr is the other prerequisite the doc names: "a missing herdr or tmux is a prerequisite to
// fix rather than work around". One message, naming it.
func requireHerdr() error {
	if _, err := lookPath("herdr"); err != nil {
		return unavailableError("herdr is not installed: the watch view lives in herdr panes")
	}
	return nil
}

// unavailableError is a machine that cannot run the verb: clientcmd.ExitUnavailable, one message.
func unavailableError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: clientcmd.ExitUnavailable}
}

func usageError(message string) error {
	return &clientcmd.CodedError{Message: message, Code: clientcmd.ExitUsage}
}

// herdr runs one herdr API command and returns its "result" object. send-text and send-keys answer
// with nothing, which is an empty object.
func herdr(ctx context.Context, args ...string) (map[string]any, error) {
	out, err := execCommand(ctx, "herdr", args...)
	if err != nil {
		return nil, fmt.Errorf("herdr %s failed: %w", strings.Join(args, " "), err)
	}
	if strings.TrimSpace(out) == "" {
		return map[string]any{}, nil
	}
	var payload struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return nil, fmt.Errorf("herdr %s answered something that is not JSON: %w", strings.Join(args, " "), err)
	}
	if payload.Result == nil {
		return map[string]any{}, nil
	}
	return payload.Result, nil
}

func paneID(result map[string]any) string {
	if pane, ok := result["pane"].(map[string]any); ok {
		if id, ok := pane["pane_id"].(string); ok {
			return id
		}
	}
	if root, ok := result["root_pane"].(map[string]any); ok {
		if id, ok := root["pane_id"].(string); ok {
			return id
		}
	}
	return ""
}

// selfPath is what a pane runs: this binary, not a Python interpreter and module. The pane's shell
// line is the ONE place a command line is built as text, because that is all a pane can be sent.
func selfPath() string {
	if path, err := os.Executable(); err == nil {
		return path
	}
	return "4genteam"
}

// paneCommand is the line typed into a pane: clear the pane, then exec the client so the pane closes
// with it. The doc states this behaviour, so it is not an implementation detail.
func paneCommand(verb string) string {
	return fmt.Sprintf("clear; exec %s %s", selfPath(), verb)
}

// reportError prints the ONE message a refusal carries and returns its exit code.
func reportError(err error, stderr io.Writer) int {
	var coded *clientcmd.CodedError
	if errors.As(err, &coded) {
		fmt.Fprintf(stderr, "4genteam: %s\n", coded.Message)
		return coded.Code
	}
	fmt.Fprintf(stderr, "4genteam: %v\n", err)
	return clientcmd.ExitUnavailable
}
