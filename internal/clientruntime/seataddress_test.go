package clientruntime

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCanonicalRigSeatSplitsTheDocumentedFormAndNothingElse pins the split F-B needs: the canonical string is
// translated, the runtime's own name is not touched, and a string that merely contains an `@` is not guessed
// at. The dashed room is the case that matters - this rig's own name carries a dash, so the room is the part
// after the LAST `@` and the seat is what remains after the room's own prefix.
func TestCanonicalRigSeatSplitsTheDocumentedFormAndNothingElse(t *testing.T) {
	cases := []struct {
		session   string
		rig, seat string
		ok        bool
	}{
		{"4genthub-min-lead@4genthub-min", "4genthub-min", "lead", true},
		{"leg-solo-solo@leg-solo", "leg-solo", "solo", true},
		{"room-a-dev-one@room-a", "room-a", "dev-one", true},
		{"room-a-f.dev@room-a", "room-a", "f.dev", true},
		// The runtime's own session name is NOT the canonical form: no `@`, so there is nothing to translate.
		{"agenthub--leg-solo--solo", "", "", false},
		// A plain name, an empty half, and a suffix that does not agree with the prefix are nobody's canonical
		// name. Left alone rather than guessed at, so the transport's refusal names the string the caller typed.
		{"lead", "", "", false},
		{"@4genthub-min", "", "", false},
		{"4genthub-min-@4genthub-min", "", "", false},
		{"other-lead@4genthub-min", "", "", false},
		{"4genthub-min-lead@", "", "", false},
	}
	for _, c := range cases {
		rig, seat, ok := CanonicalRigSeat(c.session)
		if ok != c.ok || rig != c.rig || seat != c.seat {
			t.Errorf("CanonicalRigSeat(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.session, rig, seat, ok, c.rig, c.seat, c.ok)
		}
	}
}

// TestAFailedReadIsNotTheAnswerNoSuchSeat pins the fault measured live on 2026-10-14: with the shared daemon's
// socket unlinked, `send 4genthub-min lead` answered "the room's runtime does not list that seat" while the
// truth was a connect failure. The two send an operator to different places - a room definition versus a dead
// socket - so a read that could not be made must be reported as the read it is, by its own words.
func TestAFailedReadIsNotTheAnswerNoSuchSeat(t *testing.T) {
	t.Setenv(RuntimeSocketEnv, filepath.Join(t.TempDir(), "no-such-runtime.sock"))

	_, err := ResolveSeatAddress("4genthub-min", "lead")
	if err == nil {
		t.Fatal("a seat address resolved over a socket that is not there")
	}
	if strings.Contains(err.Error(), "does not list that seat") {
		t.Errorf("a failed read was reported as a room that does not list the seat: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot connect to agenthub-runtime") {
		t.Errorf("the refusal does not name the failed read: %v", err)
	}
	if !strings.Contains(err.Error(), "4genthub-min/lead") {
		t.Errorf("the refusal does not name the seat it was asked about: %v", err)
	}
}

// TestTheRoomRecordAnswersBothWays pins that the failed-read fix did not soften the other answer: a runtime
// that answers and does not list the seat is still refused in this file's own words, and one that lists it
// still yields the session name and the record it came from.
func TestTheRoomRecordAnswersBothWays(t *testing.T) {
	original := seatStatuses
	t.Cleanup(func() { seatStatuses = original })

	seatStatuses = func(string) ([]SeatStatus, error) {
		return []SeatStatus{{Seat: "reviewer", SessionName: "agenthub--4genthub-min--reviewer"}}, nil
	}

	address, err := ResolveSeatAddress("4genthub-min", "reviewer")
	if err != nil {
		t.Fatalf("a seat the record lists did not resolve: %v", err)
	}
	if address.SessionName != "agenthub--4genthub-min--reviewer" || address.Source == "" {
		t.Errorf("address = %+v, want the record's own session name and the source it was read from", address)
	}

	_, err = ResolveSeatAddress("4genthub-min", "lead")
	if err == nil {
		t.Fatal("a seat the answering room does not list resolved")
	}
	if !strings.Contains(err.Error(), "does not list that seat") {
		t.Errorf("a room that answered without the seat = %v, want the no-record refusal", err)
	}
}
