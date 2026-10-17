// What these tests pin is the file's MEANING rather than its encoder: that a room with no limits file is a
// room with nothing enforced, that the two ceilings are counted over the windows the design names, and that a
// seat's spend is read from the same field `room report` reads.
package roombudget

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A room with no limits file is EVERY room's first day, so Load must answer "nothing enforced" rather than an
// error: a safeguard that refused to run because an owner had not written a file yet would be a safeguard
// that never ran.
func TestLoadWithoutAFileIsLimitsOff(t *testing.T) {
	limits, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if limits != (Limits{}) {
		t.Fatalf("limits = %+v, want the zero value: no file means nothing is enforced", limits)
	}
}

func TestSaveAndLoadRoundTripAndTheFileIs0600(t *testing.T) {
	roomDir := t.TempDir()
	want := Limits{Enabled: true, USDPerSeat24h: 25, CallsPerSeatMin: 300}
	if err := Save(roomDir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(roomDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
	info, err := os.Stat(Path(roomDir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600: the room's limits are the owner's file", info.Mode().Perm())
	}
	if raw, err := os.ReadFile(Path(roomDir)); err != nil || !strings.HasSuffix(string(raw), "\n") {
		t.Errorf("the file must end in a newline: %q (%v)", string(raw), err)
	}
}

// A reader who finds the record alone must learn the way back FROM THE FILE - not from the rail row and not
// from a refusal in hand. The exit is by hand (owner's ruling, 2026-10-16 22:31Z): the record's absence is
// what lets the seat take work again, and the ORDER is the measured half of it - a reseat run before the
// removal stops the seat and leaves it stopped, because the runtime refuses the restart before its own stop.
func TestTheParkRecordNamesTheWayBack(t *testing.T) {
	seatDir := t.TempDir()
	parked := Parked{Seat: "dev", Reason: "spent 31.40 USD in the last 24h, over the 25.00 USD ceiling",
		At: "2026-10-16T12:00:00Z", Cost: 31.4, Calls: 12, Exit: ParkExit("room-a", "dev")}
	if err := WriteParked(seatDir, parked); err != nil {
		t.Fatalf("WriteParked: %v", err)
	}
	raw, err := os.ReadFile(ParkedPath(seatDir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The concrete command and the ORDER, not a placeholder and not the softer outcome: the file is what an
	// operator copies from, and a reseat run first leaves the seat stopped.
	for _, want := range []string{`"exit"`, "remove this file FIRST", "room reseat room-a dev",
		"a reseat before the removal leaves the seat stopped"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the record must carry %q, so a reader holding only the file learns the way back:\n%s",
				want, string(raw))
		}
	}
	if got, ok := ReadParked(seatDir); !ok || got.Exit != ParkExit("room-a", "dev") {
		t.Errorf("ReadParked = %+v, %v; want the exit text back: the field is part of the record's meaning",
			got, ok)
	}
}

// Measure reads two windows from two files: the assistant turns inside 24h out of the session transcripts, and
// the guard log's lines inside the minute. Every line these tests plant also has a REASON to be skipped -
// too old, not an assistant turn, not JSON - because a reader that counted those would park a seat over
// yesterday's work.
func TestMeasureCountsTheWindowsAndSkipsEverythingElse(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	seatDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(seatDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	turn := func(offset time.Duration, cost float64, role string) string {
		return fmt.Sprintf(`{"type":"message","timestamp":"%s","message":{"role":"%s","usage":{"cost":{"total":%v}}}}`,
			stamp(offset), role, cost)
	}
	session := strings.Join([]string{
		`{"type":"session","timestamp":"` + stamp(-2*time.Hour) + `","cwd":"/work"}`,
		turn(-time.Hour, 1.5, "assistant"),
		turn(-30*time.Hour, 99, "assistant"),
		turn(-time.Hour, 7, "user"),
		`not json at all`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(seatDir, "sessions", "a.jsonl"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join([]string{
		`{"at":"` + stamp(-10*time.Second) + `","outcome":"allowed"}`,
		`{"at":"` + stamp(-2*time.Minute) + `","outcome":"blocked"}`,
		`{}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(seatDir, "tool-calls.jsonl"), []byte(calls), 0o644); err != nil {
		t.Fatal(err)
	}

	usage := Measure(seatDir, now)
	if usage.Cost != 1.5 {
		t.Errorf("cost = %v, want 1.5: only the assistant turn inside 24h counts", usage.Cost)
	}
	if usage.Calls != 1 {
		t.Errorf("calls = %d, want 1: only the call inside the minute counts", usage.Calls)
	}
}

// Breach is the decision, and it has three answers: nothing, the USD ceiling, the call ceiling - and OFF is
// not a fourth, it is the first however the numbers read.
func TestBreachNamesTheCeilingAndStaysSilentWhenOff(t *testing.T) {
	on := Limits{Enabled: true, USDPerSeat24h: 25, CallsPerSeatMin: 300}
	if kind, reason := Breach(Limits{USDPerSeat24h: 25, CallsPerSeatMin: 300}, Usage{Cost: 99, Calls: 999}); kind != "" {
		t.Errorf("kind = %q, want no breach: limits that are off enforce nothing (%s)", kind, reason)
	}
	if kind, _ := Breach(on, Usage{Cost: 24.99, Calls: 300}); kind != "" {
		t.Errorf("kind = %q, want no breach at the ceiling: the ceilings are crossed, not reached", kind)
	}
	kind, reason := Breach(on, Usage{Cost: 31.4, Calls: 10})
	if kind != KindSpend || !strings.Contains(reason, "31.40") || !strings.Contains(reason, "25.00") {
		t.Errorf("breach = (%q, %q), want the spend ceiling with both numbers", kind, reason)
	}
	kind, reason = Breach(on, Usage{Cost: 1, Calls: 412})
	if kind != KindRate || !strings.Contains(reason, "412") || !strings.Contains(reason, "300") {
		t.Errorf("breach = (%q, %q), want the call ceiling with both numbers", kind, reason)
	}
}

// The park record is what makes a stop legible after the process that stopped the seat is gone - and a record
// that cannot be read must NOT read as a parked seat.
func TestParkedRoundTripsAndACorruptRecordIsNotAParkedSeat(t *testing.T) {
	seatDir := t.TempDir()
	if _, ok := ReadParked(seatDir); ok {
		t.Fatal("a seat with no record is not parked")
	}
	want := Parked{Seat: "dev", Reason: "spent 31.40 USD in the last 24h, over the 25.00 USD ceiling",
		At: "2026-10-16T12:00:00Z", Cost: 31.4, Calls: 12}
	if err := WriteParked(seatDir, want); err != nil {
		t.Fatalf("WriteParked: %v", err)
	}
	got, ok := ReadParked(seatDir)
	if !ok || got != want {
		t.Fatalf("record = %+v (%v), want %+v", got, ok, want)
	}
	if info, err := os.Stat(ParkedPath(seatDir)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the park record is written 0600: %v (%v)", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(ParkedPath(seatDir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadParked(seatDir); ok {
		t.Error("a corrupt record must not read as a parked seat")
	}
}
