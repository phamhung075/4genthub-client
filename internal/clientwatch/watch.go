// Package clientwatch is the `watch` verb group: the live, READ-ONLY view of a room.
//
// It renders what every seat of a rig is doing - each tool call and its result, the agent's own
// reasoning, what it says and what it is told - from the seats' session logs, and it never types into
// a seat from the feed. docs/WATCH.md and skills/client/watch-team/SKILL.md are the contract this code
// must make true; the Python client's watch.py is the behaviour it was ported from.
//
// Four deliberate departures from the Python, all recorded in the phase report:
//
//   - `--rig` is REQUIRED. The Python defaulted it to the owner's own rig name, and the mission
//     forbids an owner default; the doc already says "there is no default room".
//   - the state root is `--state-root`, else the machine's own
//     ~/.config/4genthub (clientenv.DefaultStateRoot) - and a machine where that root is not there, or is without herdr, exits non-zero with ONE
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
	"path/filepath"
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

	// view filters, passed on to every pane
	only       string
	errorsOnly bool
	expand     bool
	changes    bool
}

func (s shared) gridCommandArgs() string {
	args := fmt.Sprintf("--back %d --width %d --lines %d", s.back, s.width, s.lines) + detailFlag(s.detail)
	if s.only != "" {
		args += " --only " + s.only
	}
	if s.changes {
		args += " --changes"
	}
	if s.errorsOnly {
		args += " --errors"
	}
	if s.expand {
		args += " --expand"
	}
	return args
}

// onlyKinds is the --only list as a set; nil keeps every kind.
func (s shared) onlyKinds() map[string]bool {
	if s.changes {
		return map[string]bool{"write": true}
	}
	if s.only == "" {
		return nil
	}
	kinds := map[string]bool{}
	for _, kind := range strings.Split(s.only, ",") {
		kinds[strings.TrimSpace(kind)] = true
	}
	return kinds
}

func detailFlag(detail bool) string {
	if detail {
		return " --detail"
	}
	return ""
}

// resolveStateRoot is the watch group's state root: the ONE resolution every verb shares
// (clientenv.ResolveStateRoot - `--state-root`, else this machine's own
// default), plus the two things this group adds on top. Both are deliberate, and both stay here:
//
//   - the root must EXIST and be a directory, because every pane is about to read session logs under
//     it, and the refusal has to say which path it tried;
//   - a root this group cannot resolve is unavailableError (ExitUnavailable), where the five shared
//     paths answer the shared resolver's coded error (ExitUsage): a machine that cannot watch is a
//     prerequisite to fix, not a command line typed wrong. watchRootError pins that, so the difference
//     stays a decision rather than drifting back into an accident.
func resolveStateRoot(flagValue string) (string, error) {
	root, err := clientenv.ResolveStateRoot(flagValue)
	if err != nil {
		return "", watchRootError(err)
	}
	info, statErr := os.Stat(root)
	if statErr != nil {
		return "", unavailableError(fmt.Sprintf("the omp state root is not there: %s", root))
	}
	if !info.IsDir() {
		return "", unavailableError(fmt.Sprintf("the omp state root is not a directory: %s", root))
	}
	return root, nil
}

// watchRootError is the wrapper's whole decision, as a function so a test can pin it without a machine
// that cannot resolve a home: the shared resolver's failure keeps the shared message (which names both
// escapes) and takes this group's code.
func watchRootError(err error) error {
	if err == nil {
		return nil
	}
	return unavailableError(clientenv.StateRootMessage)
}

// requireRoomAndSeats is the ONE existence check every verb that resolves a room or a seat from --rig
// goes through, so the refusals cannot disagree with each other: the room must be a native room under
// the state root (<root>/rooms/<room>, where `4genteam up` starts it), and, when --seat is given, each
// named seat must be one inside it (<root>/rooms/<room>/seats/<seat>). It checks EXISTENCE and never a
// count - a real room whose seats are all stopped is still a room, and "following 0 seats" is its
// correct output, so refusing on a zero count would break a legitimate case.
func requireRoomAndSeats(root, rig string, seats []string) error {
	room := filepath.Join(root, "rooms", rig)
	if !isDir(room) {
		return usageError(fmt.Sprintf("no such room: %s", rig))
	}
	for _, seat := range seats {
		if !isDir(filepath.Join(room, "seats", seat)) {
			return usageError(fmt.Sprintf("no such seat: %s in %s", seat, rig))
		}
	}
	return nil
}

// isDir is the existence test itself: a room or a seat that was never started here is not one.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
