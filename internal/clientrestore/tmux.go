package clientrestore

import (
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"sort"
	"strings"
)

// tmuxSessionNames is the shell's `tmux ls -F '#{session_name}'`, output and error as the shell saw
// them. A machine with no tmux server answers on stderr and lists nothing, and both scripts read that as
// "no sessions" rather than as a failure, so the caller does too - a watchdog that treated a dead tmux
// server as an error would be silent in exactly the case it exists for.
func tmuxSessionNames() (string, error) {
	cmd, err := clientenv.TmuxCommand("ls", "-F", "#{session_name}")
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	return string(out), err
}

// sessionNames is the raw read, with a missing server read as no sessions. tmux is the ground truth for
// "is this rig up": the daemon's own node records said running while the server hosting ten seats was
// gone (the 2026-10-05 crash both scripts exist for).
func sessionNames() []string {
	out, err := tmuxRead()
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

// rigSessions is the sessions that belong to one rig. A native seat's session is `agenthub--<room>--<seat>`; a
// name with the rig's name elsewhere in it is another room's seat and is not counted.
func rigSessions(rig string, names []string) []string {
	var sessions []string
	for _, name := range names {
		if room, _, ok := clientruntime.ParseSeatSessionName(name); ok && room == rig {
			sessions = append(sessions, name)
		}
	}
	return sessions
}

// rigSeats is those sessions' seat names, sorted: the shell sorted them before it briefed, so the order
// the log reports is the order the briefs went out.
func rigSeats(rig string, names []string) []string {
	seats := make([]string, 0, len(names))
	for _, session := range rigSessions(rig, names) {
		_, seat, _ := clientruntime.ParseSeatSessionName(session)
		seats = append(seats, seat)
	}
	sort.Strings(seats)
	return seats
}
