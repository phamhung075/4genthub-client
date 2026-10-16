package clientroom

import (
	"fmt"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

// parkNote is the trailing segment of a seat's row for the budget breaker's record, or the empty string when
// the seat carries none.
//
// A PARKED SEAT IS STOPPED AND REFUSED WORK - every client route that would hand it work asks the record first
// (seatpark) - and a row that showed only `stopped` would leave the reader to guess whether the seat was idle
// or parked, so the reason and the numbers that stopped it are what the row prints rather than something the
// reader has to infer. That is the same rule the alert segment follows.
//
// THE RECORD IS READ UNDER THE STATE ROOT THIS VERB WAS GIVEN, in the seat's own directory: the SEAT STORE
// (`~/.config/4genthub/seats`) holds audit lines and pins, so a park read out of it would name a path that
// never exists and every seat would look unparked.
func parkNote(stateRoot string, seat clientruntime.SeatStatus) string {
	parked, ok := roombudget.ReadParked(clientruntime.SeatDir(stateRoot, seat.Room, seat.Seat))
	if !ok {
		return ""
	}
	return fmt.Sprintf(" parked=%s reason=%q cost_usd=%.2f calls=%d", parked.At, parked.Reason, parked.Cost, parked.Calls)
}
