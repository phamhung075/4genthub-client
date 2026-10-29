package clientroom

import (
	"strings"
	"testing"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/seatwait"
)

// A seat that declared a wait shows it on its status row, and a seat_up_with_queued_work alert naming only the
// wait's own rows is dropped; an alert naming another row stays.
func TestStatusRowShowsTheWaitAndDropsTheAlertItCovers(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	rec := seatwait.Record{Room: "room-a", Seat: "rust-dev", Kind: "merge", Target: "step6@f85361d",
		Started: now.Add(-12 * time.Minute), Deadline: now.Add(48 * time.Minute), Covered: []string{"qi-1:in-progress"}}
	if err := seatwait.Write(root, rec); err != nil {
		t.Fatal(err)
	}
	quiet := int64(720)
	seat := clientruntime.SeatStatus{Room: "room-a", Seat: "rust-dev", Runtime: "omp", State: "running", SessionName: "s",
		Alert: &clientruntime.SeatAlert{Kind: "seat_up_with_queued_work", QuietSeconds: &quiet, WaitingQitemIDs: []string{"qi-1"}}}
	got := seatRow(seat, root)
	if got != "rust-dev omp running s waiting=merge:step6@f85361d 12m/1h00m" {
		t.Errorf("row = %q", got)
	}
	seat.Alert = &clientruntime.SeatAlert{Kind: "seat_up_with_queued_work", QuietSeconds: &quiet, WaitingQitemIDs: []string{"qi-1", "qi-9"}}
	if got := seatRow(seat, root); !strings.Contains(got, "alert=seat_up_with_queued_work") || !strings.Contains(got, "waiting=merge:") {
		t.Errorf("an uncovered row lost its alert: %q", got)
	}
}
