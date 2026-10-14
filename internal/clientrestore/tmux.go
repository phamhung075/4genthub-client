package clientrestore

import (
	"os/exec"
	"sort"
	"strings"
)

// tmuxSessionNames is the shell's `tmux ls -F '#{session_name}'`, output and error as the shell saw
// them. A machine with no tmux server answers on stderr and lists nothing, and both scripts read that as
// "no sessions" rather than as a failure, so the caller does too - a watchdog that treated a dead tmux
// server as an error would be silent in exactly the case it exists for.
func tmuxSessionNames() (string, error) {
	out, err := exec.Command("tmux", "ls", "-F", "#{session_name}").Output()
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

// rigSessions is the sessions that belong to one rig. the rig orchestrator names a seat's session
// `<rig>-<seat>@<rig>`, and the shell matched both ends of that shape; a name that has the rig's name in
// the middle is another rig's seat and is not counted, which is the bug a plain substring match would
// introduce.
func rigSessions(rig string, names []string) []string {
	prefix, suffix := rig+"-", "@"+rig
	var sessions []string
	for _, name := range names {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			sessions = append(sessions, name)
		}
	}
	return sessions
}

// rigSeats is those sessions' seat names, sorted: the shell sorted them before it briefed, so the order
// the log reports is the order the briefs went out.
func rigSeats(rig string, names []string) []string {
	prefix, suffix := rig+"-", "@"+rig
	seats := make([]string, 0, len(names))
	for _, session := range rigSessions(rig, names) {
		seat := strings.TrimSuffix(strings.TrimPrefix(session, prefix), suffix)
		if seat != "" {
			seats = append(seats, seat)
		}
	}
	sort.Strings(seats)
	return seats
}
