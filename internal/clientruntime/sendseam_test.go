package clientruntime

import "testing"

func TestParseSeatSessionNameInvertsSeatSessionName(t *testing.T) {
	room, seat, ok := ParseSeatSessionName(SeatSessionName("4genthub-min", "go-dev"))
	if !ok || room != "4genthub-min" || seat != "go-dev" {
		t.Fatalf("got %q %q %v", room, seat, ok)
	}
	for _, name := range []string{"plain-session", "r-a@r", "agenthub--r", "agenthub--r--", "agenthub----a", ""} {
		if _, _, ok := ParseSeatSessionName(name); ok {
			t.Errorf("%q is not a seat session", name)
		}
	}
}
