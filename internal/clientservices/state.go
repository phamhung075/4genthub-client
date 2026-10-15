// The two files agent keeps that are NOT heartbeats: its local state file, agent.json, and the alert log
// the operator reads with `doctor`.
package clientservices

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientenv"
)

// AgentState is agent's local state file, `<seat store>/agent.json`, mode 0600. 7.1 rules its `rigs` list -
// the rooms that are meant to run, which `up` appends to and `down` removes from - and 7.2's row carries
// two fields no external observer can compute, so agent records its supervision of each safeguard here
// too. `doctor` and `status --json` read them and compute everything else themselves; a row whose
// safeguard agent never started simply carries no restarts and no started_at.
type AgentState struct {
	// Rigs is the list a report walks: one compaction supervisor and one watchdog per rig.
	Rigs []string `json:"rigs"`
	// Safeguards are the rows agent recorded as it supervised its children. Only the fields pgrep and a
	// heartbeat cannot answer are read back from here (restarts, started_at, failing).
	Safeguards []Health `json:"safeguards"`
}

// AgentStatePath is the state file's location. It is a variable so a test drives a temp store instead of
// the machine's own seat store.
var AgentStatePath = defaultAgentStatePath

// defaultAgentStatePath is `<seat store>/agent.json`, the path 7.1 and 3.2 name.
func defaultAgentStatePath() (string, error) {
	store, err := clientenv.ResolveSeatStore("")
	if err != nil {
		return "", err
	}
	return filepath.Join(store, "agent.json"), nil
}

// ReadAgentState reads the rooms that are meant to run and answers TWO things: the state, and the error a
// read that FAILED made. A file that is not there is NOT an error - a machine whose agent has never run has
// no rooms, and `doctor` must be able to say so - while a file that is there and could not be read or
// parsed is exactly an error. That difference is the whole point of the second answer: three callers
// REWRITE this list with what they read (`room stop` removes a room, `up` records one, the supervisor's own
// pass keeps the list beside fresh safeguard rows), so collapsing "nothing is meant to run" with "the list
// could not be read" made one unreadable file erase every room the machine was meant to keep running.
// Measured 2026-10-14: `not json{` written over a two-room list, then a stop, left the empty state behind
// and dropped the other room with it. A caller that writes MUST refuse on an error; a caller that only
// reports may go on, because "no rooms are meant to run" is then still what the file says.
func ReadAgentState() (AgentState, error) {
	path, err := AgentStatePath()
	if err != nil {
		return AgentState{}, fmt.Errorf("the rooms meant to run live at a path this machine cannot resolve: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AgentState{}, nil
		}
		return AgentState{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	var state AgentState
	if err := json.Unmarshal(raw, &state); err != nil {
		return AgentState{}, fmt.Errorf("%s is not readable as the rooms meant to run: %w", path, err)
	}
	return state, nil
}

// WriteAgentState writes the state file atomically and with mode 0600, because this is the file a later
// phase will carry the machine's command consent in (3.2) and a world-readable consent file is not one.
func WriteAgentState(state AgentState) error {
	path, err := AgentStatePath()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".agent.json-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
	}()
	if _, err := temp.Write(append(body, '\n')); err != nil {
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// AddRig records a room as one agent supervises, once: `up` is idempotent, and a rig listed twice would
// be reported twice.
func AddRig(state AgentState, rig string) AgentState {
	if strings.TrimSpace(rig) == "" {
		return state
	}
	for _, listed := range state.Rigs {
		if listed == rig {
			return state
		}
	}
	state.Rigs = append(state.Rigs, rig)
	return state
}

// RemoveRig drops a room from the list. A room that is not listed is stopped on purpose (7.9), so this
// is what `down` does to it.
func RemoveRig(state AgentState, rig string) AgentState {
	kept := make([]string, 0, len(state.Rigs))
	for _, listed := range state.Rigs {
		if listed != rig {
			kept = append(kept, listed)
		}
	}
	state.Rigs = kept
	return state
}

// AlertsTail is how many lines of alerts.log `doctor` prints. The alert itself is the last line, so the
// tail is for the reading and the exit code for the decision.
const AlertsTail = 10

// AlertsLogPath is where every safeguard transition is appended: <LogDir>/alerts.log. It is the one
// surface that reaches an operator without the cloud, and 7.3 states plainly that it reaches nobody
// until someone runs `doctor` - the alert itself still travels on the next `ready`.
func AlertsLogPath() (string, error) {
	dir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "alerts.log"), nil
}

// AppendAlert adds one line to alerts.log. A failure to write is returned rather than swallowed: agent
// reports it in its own log, because an alert that silently failed to land is the incident again.
func AppendAlert(line string) error {
	path, err := AlertsLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(line + "\n"); err != nil {
		return err
	}
	return nil
}

// TailAlerts is the last lines of alerts.log, for `doctor`. A missing file is an empty tail and not an
// error: an alert log nobody has written to yet is exactly what a fresh machine has.
func TailAlerts() []string {
	path, err := AlertsLogPath()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > AlertsTail {
		lines = lines[len(lines)-AlertsTail:]
	}
	return lines
}
