package clientrestore

import (
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"sort"
	"strings"
)

// tmuxSessionNames is the shell's `tmux ls -F '#{session_name}'`, output and error as the shell saw
// them, asked of the server under root - the runtime puts that server under the state dir it was started
// with (clientenv.TmuxRoot), so a verb handed a root asks the server that root's rooms actually run in.
// A machine with no tmux server answers on stderr and lists nothing, and both scripts read that as
// "no sessions" rather than as a failure, so the caller does too - a watchdog that treated a dead tmux
// server as an error would be silent in exactly the case it exists for.
func tmuxSessionNames(root string) (string, error) {
	cmd, err := clientenv.TmuxCommandAt(root, "ls", "-F", "#{session_name}")
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	return string(out), err
}

// sessionNames is the raw read, with a missing server read as no sessions. tmux is the ground truth for
// "is this rig up": the daemon's own node records said running while the server hosting ten seats was
// gone (the 2026-10-05 crash both scripts exist for).
func sessionNames(root string) []string {
	out, err := tmuxRead(root)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

// roomSessions is the sessions that belong to one room. A native seat's session is `agenthub--<room>--<seat>`; a
// name with the room's name elsewhere in it is another room's seat and is not counted.
func roomSessions(room string, names []string) []string {
	var sessions []string
	for _, name := range names {
		if sessionRoom, _, ok := clientruntime.ParseSeatSessionName(name); ok && sessionRoom == room {
			sessions = append(sessions, name)
		}
	}
	return sessions
}

// roomSeats is those sessions' seat names, sorted: the shell sorted them before it briefed, so the order
// the log reports is the order the briefs went out.
func roomSeats(room string, names []string) []string {
	seats := make([]string, 0, len(names))
	for _, session := range roomSessions(room, names) {
		_, seat, _ := clientruntime.ParseSeatSessionName(session)
		seats = append(seats, seat)
	}
	sort.Strings(seats)
	return seats
}
