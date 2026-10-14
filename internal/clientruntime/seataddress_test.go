package clientruntime

import "testing"

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
