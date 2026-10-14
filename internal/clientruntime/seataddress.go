package clientruntime

import (
	"fmt"
	"os"
	"strings"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// SEAT ADDRESS RESOLUTION: which name a seat's pane actually carries, read from the runtime that owns it.
//
// The client must never COMPOSE this name. Two names describe one seat - the runtime's native
// `agenthub--<room>--<seat>` and the canonical `<room>-<seat>@<room>` - and for a room whose name carries a
// dash the canonical string cannot be split back into its halves without guessing: `4genthub-min-lead@4genthub-min`
// parses equally as (4genthub, min-lead) and as (4genthub-min, lead). So the name is READ as a field from the
// runtime's room record, never converted from the other form.

// SeatAddress is where one seat's delivery goes, and what proved it.
type SeatAddress struct {
	// SessionName is the name the runtime can address, exactly as the record spells it. It is never built
	// here; an empty one is a resolution failure, not a name to fill in.
	SessionName string
	// Source names the record the name was read from, for the report a send owes its caller.
	Source string
}

// CanonicalRigSeat splits the canonical session form `<room>-<seat>@<room>` - the string every seat's own
// session name carries, the form this client's help documents and the form the envelope's reply hint tells a
// recipient to use - into the room and the seat.
//
// ok is false for anything that is not that form, and that includes the runtime's OWN session name
// (`agenthub--<room>--<seat>`), which has no `@` and is therefore passed through untouched. The room is the
// part after the LAST `@` and must also be the prefix of the part before it, because a room name may itself
// contain a dash: `4genthub-min-lead@4genthub-min` is room `4genthub-min`, seat `lead`. A string that carries
// an `@` without that agreement is nobody's canonical name and is left alone rather than guessed at.
func CanonicalRigSeat(session string) (rig, seat string, ok bool) {
	at := strings.LastIndex(session, "@")
	if at <= 0 || at == len(session)-1 {
		return "", "", false
	}
	left, room := session[:at], session[at+1:]
	prefix := room + "-"
	if !strings.HasPrefix(left, prefix) || len(left) == len(prefix) {
		return "", "", false
	}
	return room, left[len(prefix):], true
}

// ResolveSeatAddress reads the session name a seat's pane actually carries from the room's runtime. The name
// is READ, never composed - a convention is what produced `agenthub--RIG--SEAT`, a string no seat carries - so a
// seat the runtime does not list is a refusal that names it, not a name built from the caller's two arguments.
func ResolveSeatAddress(rig, seat string) (SeatAddress, error) {
	if session := seatAddressFromRuntime(rig, seat); session != "" {
		return SeatAddress{SessionName: session, Source: "the runtime's room record"}, nil
	}
	return SeatAddress{}, &clientcmd.CodedError{
		Message: fmt.Sprintf("no record of %s/%s: the room's runtime does not list that seat; no name was composed - "+
			"a seat's session name is read from the record that owns its pane, never built from a convention", rig, seat),
		Code: clientcmd.ExitUnavailable,
	}
}

// seatAddressFromRuntime is the FIELD path: ask the room's own runtime for the seat's real session name.
//
// The room is `AGENTHUB_ROOM` when this shell is a native seat - the launcher that hosts a seat states the
// room as a field, and the native shape's first positional argument names the same room. A runtime that has
// no such room answers nothing.
func seatAddressFromRuntime(rig, seat string) string {
	if seat == "" {
		return ""
	}
	// THE ROOM THE CALLER NAMED WINS over the ambient `AGENTHUB_ROOM`. A seat shell always carries the room it
	// sits in, so an env-first read answers for the WRONG room the moment a seat addresses another one -
	// `send other-room-dev@other-room` from inside this rig's own shell asked this rig's room and heard that its
	// seat was not there. The ambient value is the fallback for a caller that named no room at all.
	room := strings.TrimSpace(rig)
	if room == "" {
		room = strings.TrimSpace(os.Getenv(agentHubRoomEnv))
	}
	if room == "" {
		return ""
	}
	statuses, err := NewClient("").GetRoomStatus(room)
	if err != nil {
		return ""
	}
	for _, status := range statuses {
		if status.Seat == seat && strings.TrimSpace(status.SessionName) != "" {
			return strings.TrimSpace(status.SessionName)
		}
	}
	return ""
}
