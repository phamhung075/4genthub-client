package clientroom

import (
	"os"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

// A parked seat's row says so, with the reason and the numbers that stopped it: a row that showed only
// `stopped` would leave the reader to guess whether the seat was idle or parked by the budget breaker.
func TestASeatRowCarriesTheParkWhenTheBreakerLeftOne(t *testing.T) {
	stateRoot := t.TempDir()
	seat := clientruntime.SeatStatus{Room: "lab", Seat: "dev", Runtime: "omp", State: "stopped", SessionName: "agenthub--lab--dev"}

	if row := seatRow(seat, stateRoot); strings.Contains(row, "parked=") {
		t.Errorf("a seat with no record was rendered parked: %q", row)
	}
	if err := os.MkdirAll(clientruntime.SeatDir(stateRoot, "lab", "dev"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := roombudget.WriteParked(clientruntime.SeatDir(stateRoot, "lab", "dev"), roombudget.Parked{
		Seat: "dev", Reason: "made 300 tool calls in the last minute, over the 10 call ceiling",
		At: "2026-10-16T17:03:20Z", Cost: 1.5, Calls: 300,
	}); err != nil {
		t.Fatalf("WriteParked: %v", err)
	}

	row := seatRow(seat, stateRoot)

	for _, want := range []string{
		"dev omp stopped agenthub--lab--dev",
		"parked=2026-10-16T17:03:20Z",
		`reason="made 300 tool calls in the last minute, over the 10 call ceiling"`,
		"cost_usd=1.50", "calls=300",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q does not carry %q", row, want)
		}
	}
}

// The park is a segment of the SAME row as the runtime's alert, so a seat that is both quiet and parked says
// both - the two facts come from different writers and neither may hide the other.
func TestTheParkAndTheAlertShareTheOneRow(t *testing.T) {
	stateRoot := t.TempDir()
	if err := os.MkdirAll(clientruntime.SeatDir(stateRoot, "lab", "dev"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := roombudget.WriteParked(clientruntime.SeatDir(stateRoot, "lab", "dev"), roombudget.Parked{
		Seat: "dev", Reason: "spent 2.00 USD in the last 24h, over the 1.00 USD ceiling",
		At: "2026-10-16T17:03:20Z", Cost: 2, Calls: 7,
	}); err != nil {
		t.Fatalf("WriteParked: %v", err)
	}
	quiet := int64(900)
	seat := clientruntime.SeatStatus{
		Room: "lab", Seat: "dev", Runtime: "omp", State: "running", SessionName: "agenthub--lab--dev",
		Alert: &clientruntime.SeatAlert{Kind: "quiet", QuietSeconds: &quiet},
	}

	row := seatRow(seat, stateRoot)

	if !strings.Contains(row, "alert=quiet quiet_seconds=900") ||
		!strings.Contains(row, `parked=2026-10-16T17:03:20Z reason="spent 2.00 USD in the last 24h, over the 1.00 USD ceiling"`) {
		t.Errorf("row = %q, want the alert and the park in one line", row)
	}
}
