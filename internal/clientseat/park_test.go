package clientseat

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
	"github.com/phamhung075/4genthub-client/internal/seatpark"
)

// A PARKED SEAT IS NOT HANDED WORK, and nothing needs to reach the daemon to find that out: the refusal is the
// record the breaker left in the seat's own directory, read before the policy decision and before any delivery.
// The record is written by the BREAKER's own writer, so this measures the reader against its writer.
func TestSendRefusesAParkedSeatAndDeliversNothing(t *testing.T) {
	pinGoDevShell(t)
	auditStoreUnder(t)

	stateRoot := t.TempDir()
	previous := seatpark.StateRoot
	seatpark.StateRoot = func() (string, error) { return stateRoot, nil }
	t.Cleanup(func() { seatpark.StateRoot = previous })
	if err := os.MkdirAll(clientruntime.SeatDir(stateRoot, "4genthub-min", "fe-dev"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := roombudget.WriteParked(clientruntime.SeatDir(stateRoot, "4genthub-min", "fe-dev"), roombudget.Parked{
		Seat: "fe-dev", Reason: roombudget.KindSpend, At: "2026-10-16T17:03:20Z", Cost: 3.0149, Calls: 41,
	}); err != nil {
		t.Fatalf("WriteParked: %v", err)
	}

	sock, requests := fakeRuntime(t, func(line string) string {
		if strings.Contains(line, `"Whoami"`) {
			return `{"ok":true,"data":{"identity":{"roomName":"4genthub-min","memberId":"go-dev",` +
				`"sessionName":"agenthub--4genthub-min--go-dev"}}}` + "\n"
		}
		if strings.Contains(line, `"GetRoomLinks"`) {
			return roomLinksDoc("go-dev", "fe-dev")
		}
		if strings.Contains(line, `"GetRoomStatus"`) {
			// The seat's own session name is read from the room's record before anything is decided, so the fake
			// has to answer it the way a daemon does.
			return `{"ok":true,"data":[{"room":"4genthub-min","seat":"fe-dev",` +
				`"session_name":"agenthub--4genthub-min--fe-dev","runtime":"omp","state":"Running"}]}` + "\n"
		}
		return `{"ok":true,"data":{"delivered":"4genthub-min-fe-dev@4genthub-min","outcome":"submitted"}}` + "\n"
	})
	t.Setenv(clientruntime.RuntimeSocketEnv, sock)

	var out, errOut bytes.Buffer
	code := sendCommand{}.Run(context.Background(), []string{"4genthub-min", "fe-dev", "please fix X"}, &out, &errOut)

	if code == 0 {
		t.Fatalf("a send to a parked seat exited 0; stdout %q stderr %q", out.String(), errOut.String())
	}
	for _, want := range []string{"PARKED", "cost_usd=3.01", "calls=41"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal does not carry %q: %q", want, errOut.String())
		}
	}
	// EVERY COMPLETED REQUEST IS ALREADY IN THE CHANNEL once the verb returns - the fake queues the line before
	// it answers - so no wait is needed to say that no delivery was made.
	for {
		var line string
		select {
		case line = <-requests:
		default:
			return
		}
		var frame struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("the frame the verb wrote is not JSON: %v (%q)", err, line)
		}
		if frame.Action == "SendText" {
			t.Errorf("a parked seat was handed work: %s", line)
		}
	}
}
