// A PARKED SEAT IS NOT HANDED WORK.
//
// The budget breaker stops a seat that crossed one of the room's ceilings and leaves the reason in the seat's
// own directory (roombudget.WriteParked). The stop alone would leave that park cosmetic: a seat is brought
// back by the next thing that hands it work - `4genteam send`, the restore brief, a room revive - so every
// client route that would hand a seat work asks this package first, and a park means what it says.
//
// WHY THE RECORD IS TRUSTWORTHY. It sits in the seat's own directory, protected by directory and by name in
// seat-guard.ts, so a seat cannot clear its own park and only a safeguard can declare one.
//
// WHY THE ROOT IS THE STATE ROOT AND NOT THE SEAT STORE. `parked.json` is written under
// `<state root>/rooms/<room>/seats/<seat>` - the directory the seat runs in, which is also its
// PI_CODING_AGENT_DIR - while clientenv's seat store (`~/.config/4genthub/seats`) holds audit lines and pins.
// Read the park out of the store and the path never exists, so every send is ALLOWED silently; that mistake is
// what this note exists to stop, and it is the reason the resolution is spelled out rather than inferred.
//
// WHY A FOREIGN DAEMON IS NOT GATED HERE. A park record belongs to the daemon that wrote it, and `--socket`
// aimed at another machine's daemon keeps its state under a root no socket path names, so this package would
// read this shell's root for a foreign room and find no record. On the client side the check therefore stands
// aside for a foreign socket rather than answering on the wrong file: the runtime's own Send is where that
// direction belongs, because the runtime is the one process every route to a seat goes through.
package seatpark

import (
	"fmt"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientenv"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

// StateRoot is the resolution this package reads park records under. It is a var for the same reason this
// client's other resolutions are: a test drives a root of its own rather than the machine's. The default is
// the one every verb resolves, so production has one resolution point and not two.
var StateRoot = func() (string, error) { return clientenv.ResolveStateRoot("") }

// Refuse is the refusal a parked seat gets from a route that would hand it work, and nil when the seat may
// take work. socket names the daemon the work goes to, and "" means this machine's own.
func Refuse(socket, room, seat string) error {
	if socket != "" && socket != clientruntime.DefaultSocketPath() {
		return nil
	}
	root, err := StateRoot()
	if err != nil {
		return &clientcmd.CodedError{
			Code: clientcmd.ExitUnavailable,
			Message: fmt.Sprintf("the state root holding %s's park record could not be resolved: %v; nothing "+
				"was sent", seat, err),
		}
	}
	return RefuseIn(root, room, seat)
}

// RefuseIn is Refuse for a caller that already holds the resolved state root.
func RefuseIn(root, room, seat string) error {
	seatDir := clientruntime.SeatDir(root, room, seat)
	parked, ok := roombudget.ReadParked(seatDir)
	if !ok {
		return nil
	}
	return &clientcmd.CodedError{
		Code: clientcmd.ExitUnavailable,
		Message: fmt.Sprintf("seat %s of room %s is PARKED: the budget breaker stopped it for %s "+
			"(cost_usd=%.2f, calls=%d, at %s), and a parked seat is not handed work. %s is the record; "+
			"removing it lets the seat take work again once the cause is understood. Nothing was sent",
			seat, room, parked.Reason, parked.Cost, parked.Calls, parked.At, roombudget.ParkedPath(seatDir)),
	}
}
