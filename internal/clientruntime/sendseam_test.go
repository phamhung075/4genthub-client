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

// TestSendTextArgsNamesTheOriginAndOmitsAnUnknownSender: the request one message travels in has to carry the
// ORIGIN a seat's runner names when it can neither deliver nor keep the text - the caller's session and the
// verb that carried it - and a caller with no session of its own (a shell, a hook) sends no `from` at all,
// rather than an empty string the runner would have to read as a name (hub task c94cecfc).
func TestSendTextArgsNamesTheOriginAndOmitsAnUnknownSender(t *testing.T) {
	named := SendTextArgs("agenthub--4genthub-min--lead", "the body", "agenthub--4genthub-min--go-dev", "send")
	if named["session"] != "agenthub--4genthub-min--lead" || named["text"] != "the body" {
		t.Fatalf("the request names the target and the text: %v", named)
	}
	if named["from"] != "agenthub--4genthub-min--go-dev" || named["verb"] != "send" {
		t.Fatalf("the origin is carried: %v", named)
	}

	anonymous := SendTextArgs("agenthub--4genthub-min--lead", "the body", "  ", "send")
	if _, present := anonymous["from"]; present {
		t.Fatalf("a caller with no session sends no from, not a blank one: %v", anonymous)
	}
	if anonymous["verb"] != "send" {
		t.Fatalf("the verb is still named: %v", anonymous)
	}
}
