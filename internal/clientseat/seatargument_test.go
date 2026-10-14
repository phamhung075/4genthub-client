package clientseat

import (
	"strings"
	"testing"
)

// THE TWO ATTEMPTS THIS SAVES. `rig %q has no seat %q` was true both of a seat key that does not exist and of a
// canonical session name handed to the SEAT argument. The verifier who did the second spent two attempts on it,
// because the refusal never said what SEAT should be: the difference between a refusal a reader can act on and
// one they have to reverse-engineer. Each arm is one fact.
func TestTheSeatRefusalNamesTheArgumentItExpects(t *testing.T) {
	sessionName := seatArgumentHint("4genthub-min-reviewer@4genthub-min")
	for _, want := range []string{"is a session name", "seat key", "4genteam send SESSION MESSAGE"} {
		if !strings.Contains(sessionName, want) {
			t.Fatalf("a session name in the SEAT position must be told what it is and where it belongs; %q is "+
				"missing from: %s", want, sessionName)
		}
	}

	key := seatArgumentHint("reviewer")
	if strings.Contains(key, "is a session name") {
		t.Fatalf("a plain seat key must not be told it is a session name: %s", key)
	}
	if !strings.Contains(key, "seat key") {
		t.Fatalf("even a plain key must be told what SEAT is: %s", key)
	}
}
