package clientruntime

import (
	"errors"
	"fmt"
	"strings"
)

// THE ONE ACTIVITY READ. Both idle readers - clientseat's QuietWaiter (through seatActivity) and clientlifecycle's
// roomIdleReason - ask this function and nothing else, so the question "is this seat at its prompt?" has ONE
// implementation. The word is the seat's own activity report as the runtime serves it (`agentActivity.state` on
// the session list), asked over the runtime socket.
//
// SeatActivity is one seat's reading, in the three shapes the callers must tell apart.
//
//   - Read && Word != "": the report answered with a word. Whether it releases a destroyer is the predicate's
//     decision (ActivityReleasesTheWait), never this read's.
//   - Read && Word == "" && !Present: the report answered and this seat has NO activity field at all. That is an
//     explicit diagnostic rather than an empty word, because a missing producer and a seat that said nothing are
//     different facts and only one of them is a bug in the runtime.
//   - !Read: nothing could be asked, and Why names it. AN UNREADABLE ACTIVITY IS NOT THE ANSWER "not running" - it is
//     the absence of an answer, and both callers keep waiting on it rather than reading it as permission to destroy.
type SeatActivity struct {
	Word    string
	Present bool
	Read    bool
	Why     string
}

// ReadSeatActivity answers one seat's activity word from the runtime's session list.
//
// The seat is matched by the logical id the readers already key on (`<room>.<seat>`) or by the record-derived
// session name (SeatSessionName). A list whose rows carry neither refuses rather than answering for the wrong
// seat - a read that cannot say WHICH seat it read is not a reading.
func ReadSeatActivity(room, seat string) SeatActivity {
	entries, err := NewClient("").ListSessions(room)
	if err != nil {
		return SeatActivity{Read: false, Why: "the activity read could not ask the runtime at " + DefaultSocketPath() + ": " + err.Error()}
	}
	canonical := SeatSessionName(room, seat)
	for _, entry := range entries {
		if entry.LogicalID != room+"."+seat && entry.SessionName != canonical && entry.CanonicalSessionName != canonical {
			continue
		}
		if entry.AgentActivity.State == "" {
			// ASKED, AND NO FIELD: a matched seat carrying no activity word at all. It is reported as its own
			// answer so the supervisor can say so.
			return SeatActivity{Read: true, Present: false}
		}
		return SeatActivity{Word: entry.AgentActivity.State, Present: true, Read: true}
	}
	return SeatActivity{Read: false, Why: "the activity read answered no record for " + room + "/" + seat}
}

// RoomNodes is the ONE reading of a room's node records, over the runtime socket: a
// room the local runtime hosts answers GetRoomStatus. The rows are in the node keys every caller already
// parses.
//
// It exists so that reading has ONE home: `up`, `compact`, the restore gate and the watchdog all need the
// same reading, and a second copy per verb is how two verbs come to disagree about the same room.
//
// THE THREE ANSWERS ARE EXPLICIT, because a caller that must refuse needs to know which one it has and
// because an empty string standing for success is exactly the ambiguity this pass exists to remove:
//
//   - ok && err == nil: a transport ANSWERED. nodes may be an empty table, which is a room with no seats.
//   - !ok && err == nil: NO RECORDS. The runtime answered and lists no seat for this room. A caller for
//     which it means "nothing to decide" proceeds; one for which it means "I cannot see the room I was asked
//     about" refuses on its own judgement, which is why this read does not decide that for it.
//   - err != nil: the read was ATTEMPTED and FAILED - the runtime did not answer, or the answer was not the
//     table. A caller that needs the reading refuses in these words rather than proceeding as if the room
//     had no seats.
func RoomNodes(room string) ([]map[string]any, bool, error) {
	seats, err := NewClient("").GetRoomStatus(room)
	if IsUnknownRoom(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(seats) == 0 {
		return nil, false, nil
	}
	nodes := make([]map[string]any, 0, len(seats))
	for _, seat := range seats {
		// model is a plain string ("" when the runtime names none): the peak gate and `sync switch` read it
		// as one, and a *string would silently read as absent.
		model := ""
		if seat.Model != nil {
			model = *seat.Model
		}
		nodes = append(nodes, map[string]any{
			"roomName":  room,
			"logicalId": fmt.Sprintf("%s.%s", room, seat.Seat),
			"seat":      seat.Seat,
			// canonicalSessionName is the session the runtime hosts the seat under.
			"canonicalSessionName": seat.SessionName,
			"runtime":              seat.Runtime,
			"sessionStatus":        seat.State,
			// lifecycleState carries the SAME fact as sessionStatus, under the key the watchdog's
			// expectation count reads.
			"lifecycleState": seat.State,
			"model":          model,
			"pid":            seat.Pid,
		})
	}
	return nodes, true, nil
}

// SeatNode is ONE seat's row of a room's node records: the reading a verb needs when its question is about
// a single seat rather than the whole room - `sync switch` asks which canonical session a seat carries,
// and `sync respawn` asks whether that seat's agent has died. It is RoomNodes narrowed to one row, so a
// caller asks the same question of the same transports and hears the same three answers.
//
// The narrowing is the reason it is its own function rather than a loop at each call site: what a caller
// does with a seat that is NOT there is a decision, and a row lookup copy-pasted into two verbs is how the
// two come to disagree about whether that is a refusal or a shrug. It stays out of the seat's absence:
//
//   - found && err == nil: the room's records list this seat, and row is its row.
//   - !found && err == nil: the records do NOT list this seat - whether they listed other seats or were
//     empty, which for a question about ONE seat is the same answer: this client cannot see that seat. A
//     caller for which that means "there is nothing to act on" proceeds on it.
//   - err != nil: the read was ATTEMPTED and FAILED. That is NOT the answer "no such seat" - the read
//     could not be made at all - so a caller must refuse in those words rather than treat it as a room
//     without that seat.
func SeatNode(room, seat string) (map[string]any, bool, error) {
	nodes, ok, err := RoomNodes(room)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	for _, node := range nodes {
		if name, _ := node["seat"].(string); name == seat {
			return node, true, nil
		}
	}
	return nil, false, nil
}

// IsUnknownRoom reports whether err is the runtime's own refusal for a room it holds no spec for: a room
// that has not been started yet. That is "no records", not a failed read, which is why `up` can run its peak
// gate on a first start.
func IsUnknownRoom(err error) bool {
	var refused *DaemonRefused
	return errors.As(err, &refused) && strings.Contains(refused.Reason, "cannot read room spec")
}
