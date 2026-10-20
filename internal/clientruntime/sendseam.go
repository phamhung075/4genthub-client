// The canonical name of a seat's session. The runtime derives the same name on its own side, which no Go
// constant can reach; this file is its single home on the client side.
package clientruntime

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SeatSessionName is the client's OWN name for a seat's session: `agenthub--<room>--<seat>`, or "" when either half
// is missing (a half-named seat names no session, which is the contract the transport seam relied on).
//
// It is derived, not looked up, because the runtime derives it the same way (`ipc.go`, and the runtime side's
// own session naming): a verb that has a ROOM and a SEAT must be able to address the session without first
// asking the room for its status, which costs a round trip and fails for a seat the room has not reported.
// The room is the ROOM here, which is what `--state-dir`/the socket's own rooms call it.
func SeatSessionName(room, seat string) string {
	if room == "" || seat == "" {
		return ""
	}
	return fmt.Sprintf("agenthub--%s--%s", room, seat)
}

const seatSessionPrefix = "agenthub--"

// ParseSeatSessionName is SeatSessionName's inverse: (room, seat) of `agenthub--<room>--<seat>`, or false
// for any other tmux session. The room ends at the first `--` after the prefix.
func ParseSeatSessionName(name string) (room, seat string, ok bool) {
	rest, found := strings.CutPrefix(name, seatSessionPrefix)
	if !found {
		return "", "", false
	}
	room, seat, found = strings.Cut(rest, "--")
	if !found || room == "" || seat == "" {
		return "", "", false
	}
	return room, seat, true
}

// SeatSessionsDir is where a native room's omp seat writes its transcripts under the state root:
// `<root>/rooms/<room>/seats/<seat>/sessions`.
func SeatSessionsDir(root, room, seat string) string {
	return filepath.Join(SeatDir(root, room, seat), "sessions")
}

// SeatDir is a native room's omp seat directory, `<root>/rooms/<room>/seats/<seat>`. It is the seat's cwd and its
// PI_CODING_AGENT_DIR at once, so its config.yml, skills and sessions all live directly under it.
func SeatDir(root, room, seat string) string {
	return filepath.Join(root, "rooms", room, "seats", seat)
}

// RoomSpecPath is the room's own spec file, `<root>/rooms/<room>/spec.json`: what the runtime wrote when
// the room was last started, and therefore the only record of state a team definition cannot restate -
// each seat's cwd, for one.
func RoomSpecPath(root, room string) string {
	return filepath.Join(root, "rooms", room, "spec.json")
}
