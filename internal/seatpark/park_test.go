package seatpark

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
	"github.com/phamhung075/4genthub-client/internal/clientruntime"
	"github.com/phamhung075/4genthub-client/internal/roombudget"
)

// root drives this package's own resolution at a temp tree, so no case reads the machine's real state root.
func root(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	previous := StateRoot
	StateRoot = func() (string, error) { return dir, nil }
	t.Cleanup(func() { StateRoot = previous })
	return dir
}

// record writes the park with the BREAKER's own writer, at the path the breaker writes it, so the reader is
// measured against its writer rather than against a fixture this test invented.
func record(t *testing.T, stateRoot, room, seat string, parked roombudget.Parked) {
	t.Helper()
	seatDir := clientruntime.SeatDir(stateRoot, room, seat)
	// The seat's directory exists in production - it is the directory the seat runs in - and the writer renames
	// the record into it, so a fixture has to make it.
	if err := os.MkdirAll(seatDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", seatDir, err)
	}
	if err := roombudget.WriteParked(seatDir, parked); err != nil {
		t.Fatalf("WriteParked: %v", err)
	}
}

// A parked seat is refused, and the refusal carries the reason and the numbers that stopped it: those are what
// a sender needs to decide between raising the ceiling and leaving the seat down.
func TestAParkedSeatIsRefusedWithTheNumbersThatStoppedIt(t *testing.T) {
	dir := root(t)
	record(t, dir, "lab", "dev", roombudget.Parked{
		Seat: "dev", Reason: roombudget.KindSpend, At: "2026-10-16T17:03:20Z", Cost: 3.0149, Calls: 41,
	})

	err := Refuse("", "lab", "dev")

	if err == nil {
		t.Fatal("a route that would hand work to a parked seat was allowed")
	}
	var coded *clientcmd.CodedError
	if !errors.As(err, &coded) {
		t.Fatalf("err = %v (%T), want a CodedError so the verb reports it through one path", err, err)
	}
	if coded.Code != clientcmd.ExitUnavailable {
		t.Errorf("code = %d, want %d", coded.Code, clientcmd.ExitUnavailable)
	}
	for _, want := range []string{"PARKED", roombudget.KindSpend, "cost_usd=3.01", "calls=41", "parked.json"} {
		if !strings.Contains(coded.Message, want) {
			t.Errorf("the refusal does not carry %q: %s", want, coded.Message)
		}
	}
}

// A seat with no record takes work, which is the shipped state: the ceilings ship off and the safeguard writes
// this file only when it parks somebody.
func TestASeatWithNoRecordTakesWork(t *testing.T) {
	root(t)

	if err := Refuse("", "lab", "dev"); err != nil {
		t.Fatalf("a seat with no park record was refused: %v", err)
	}
	// AND A ROOM THAT DOES NOT EXIST ON THIS MACHINE IS THE SAME CASE: the resolution names a path, and a
	// missing directory is not a park.
	if err := Refuse("", "ghost-room", "ghost-seat"); err != nil {
		t.Fatalf("a seat of an unknown room was refused: %v", err)
	}
}

// A send aimed at ANOTHER machine's daemon is not this gate's to answer: the record belongs to that daemon and
// lives under a root no socket path names, so the gate stands aside instead of reading the wrong file and
// returning an allow that looks like a decision.
func TestAForeignDaemonsSeatIsNotGatedHere(t *testing.T) {
	dir := root(t)
	record(t, dir, "lab", "dev", roombudget.Parked{Seat: "dev", Reason: roombudget.KindRate, At: "2026-10-16T17:03:20Z", Cost: 1, Calls: 300})

	if err := Refuse("/run/other-daemon.sock", "lab", "dev"); err != nil {
		t.Fatalf("the gate answered for a seat of another daemon: %v", err)
	}
	// The same seat through THIS machine's own socket is refused, which is what makes the stand-aside above a
	// boundary rather than a hole in the gate.
	if err := Refuse(clientruntime.DefaultSocketPath(), "lab", "dev"); err == nil {
		t.Fatal("the same seat was allowed through this machine's own socket")
	}
}
