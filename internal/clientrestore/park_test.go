package clientrestore

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
	"github.com/phamhung075/4genthub-client/internal/seatpark"
)

// parkStateRoot drives the park record at a temp tree, and writes it with the BREAKER's own writer.
func parkStateRoot(t *testing.T) {
	t.Helper()
	previous := seatpark.StateRoot
	root := t.TempDir()
	seatpark.StateRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { seatpark.StateRoot = previous })
	if err := os.MkdirAll(clientruntime.SeatDir(root, "lab", "qa"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := roombudget.WriteParked(clientruntime.SeatDir(root, "lab", "qa"), roombudget.Parked{
		Seat: "qa", Reason: roombudget.KindSpend, At: "2026-10-16T17:03:20Z", Cost: 4, Calls: 9,
	}); err != nil {
		t.Fatalf("WriteParked: %v", err)
	}
}

// The brief is how a restored seat is handed its next stretch of work, so a parked seat is not briefed - and it
// counts as NOT reached, because a brief that was asked for and withheld on purpose is not a delivery. The
// other seat of the room is briefed as usual.
func TestBriefLeavesAParkedSeatUnbriefedAndCountsItUndelivered(t *testing.T) {
	parkStateRoot(t)

	var log bytes.Buffer
	var briefed []string
	r := &restorer{
		briefs: t.TempDir(),
		log:    &teeLog{out: &log},
		send: func(_ context.Context, session, text string) error {
			briefed = append(briefed, session)
			return nil
		},
	}

	undelivered := r.brief("lab", []string{"dev", "qa"}, "your stretch")

	if len(briefed) != 1 || !strings.Contains(briefed[0], "dev") {
		t.Errorf("briefed = %v, want only dev", briefed)
	}
	if len(undelivered) != 1 || undelivered[0] != "qa" {
		t.Errorf("undelivered = %v, want qa named", undelivered)
	}
	if !strings.Contains(log.String(), "PARKED") || !strings.Contains(log.String(), "qa") {
		t.Errorf("the log does not say why qa was left out: %q", log.String())
	}
}

// A room revive starts the stopped seats, which is exactly how a parked seat would come back and cross the
// ceiling again - so it is left down, and the skip is REPORTED: a seat this verb deliberately left down is not
// a seat that came up.
func TestReviveLeavesAParkedSeatDownAndNamesIt(t *testing.T) {
	parkStateRoot(t)

	seen := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"GetRoomStatus"`) {
			return `{"ok":true,"data":[{"room":"lab","seat":"qa","session_name":"agenthub--lab--qa",` +
				`"runtime":"omp","state":"stopped"}]}` + "\n"
		}
		return `{"ok":true,"data":{"room":"lab","seat":"qa","session_name":"agenthub--lab--qa",` +
			`"runtime":"omp","state":"running"}}` + "\n"
	})

	err := reviveRoom(context.Background(), "lab")

	if err == nil || !strings.Contains(err.Error(), "PARKED") || !strings.Contains(err.Error(), "qa") {
		t.Fatalf("err = %v, want qa named as parked", err)
	}
	for _, line := range *seen {
		if strings.Contains(line, `"RestartSeat"`) {
			t.Errorf("a parked seat was restarted: %s", line)
		}
	}
}
