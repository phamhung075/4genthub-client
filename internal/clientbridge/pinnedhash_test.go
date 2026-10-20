package clientbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestPinnedHashUsedWhenValid mirrors tests/test_bridge.py::test_pinned_hash_used_when_valid (at
// 1663257), which asserts BOTH halves of one row over the same two seats the Python used:
//
//	pins eng/coder    {"hash": "ab12"}     -> "ab12" IS used as the seat's hash
//	pins eng/reviewer {"hash": "../evil"}  -> REFUSED (7.12), never travelled as a hash
//
// The invalid half is the security-adjacent one: payload.go:177 is the guard that a pin whose value
// does not match hashPattern never travels as a hash, and until this test no test in this package
// asserted it (payload.go was the only file naming hashPattern). The invariant is the PATTERN and not
// the presence of a dot-dot: hashPattern is \A[A-Za-z0-9][A-Za-z0-9._-]{0,127}\z, so the FIRST
// character must be alphanumeric while dots are legal after it - ../evil fails on its leading '.',
// where a value like aa..bb would be ACCEPTED. Reading the rule as "dots are rejected" would be wrong
// about why the code works, which is why the row's own value is used here and the pattern named.
//
// The Python answered the invalid pin with the empty string; 7.12 makes the read an error, because "" is
// also the answer for a seat no client has pinned, and the two are different facts. The parity that
// remains is the one the row was about: a malformed pin is never used as a hash.
//
// The values are the Python's own, and the code driven is the real one - the exported PinnedHash, then
// the Hash field as BuildSeats fills it - rather than a re-implementation of the pattern in the test.
func TestPinnedHashUsedWhenValid(t *testing.T) {
	pinsDir := t.TempDir()
	writePin := func(room, seat, contents string) {
		t.Helper()
		dir := filepath.Join(pinsDir, room, seat)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pinned.json"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writePin("eng", "coder", `{"hash": "ab12"}`)
	writePin("eng", "reviewer", `{"hash": "../evil"}`)

	// Half one: the reader itself. A well-formed pin IS the seat's hash; a pin whose value is not a hash
	// is REFUSED by name (7.12) rather than answered as the empty string, because "" is the report of a seat
	// no client has pinned and a malformed pin must not arrive at the server as that.
	got, err := PinnedHash(pinsDir, "eng", "coder")
	if err != nil || got != "ab12" {
		t.Errorf("a well-formed pin read as %q, %v, want ab12: a valid pinned hash IS the seat's hash", got, err)
	}
	if got, err := PinnedHash(pinsDir, "eng", "reviewer"); err == nil || got != "" {
		t.Errorf("an invalid pin read as %q, %v, want an error naming the file: ../evil must never be used as a hash", got, err)
	}
	if got, err := PinnedHash(pinsDir, "eng", "absent"); err != nil || got != "" {
		t.Errorf("a seat with no pinned.json read as %q, %v, want \"\" and no error: no client has pinned it", got, err)
	}

	// Half two, where the Python asserted it: the built payload's hash field, seat by seat. The pin dir here
	// holds the valid pin only - the malformed one refuses the whole build, which the next test drives.
	buildDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(buildDir, "eng", "coder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "eng", "coder", "pinned.json"), []byte(`{"hash": "ab12"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nodes, err := parseItems(`[
		{"roomName":"eng","logicalId":"eng.coder","runtime":"omp","sessionStatus":"running","agentActivity":{"state":"working"}},
		{"roomName":"eng","logicalId":"eng.reviewer","runtime":"omp","sessionStatus":"running","agentActivity":{"state":"working"}}
	]`, "rig ps")
	if err != nil {
		t.Fatalf("unwrapping the two-seat fixture: %v", err)
	}
	seats, invalid, _, err := BuildSeats(nodes, nil, buildDir, false)
	if err != nil {
		t.Fatalf("BuildSeats refused: %v", err)
	}
	if invalid != 0 {
		t.Errorf("invalid = %d, want 0: both names are well formed", invalid)
	}
	where, hashes := make([]string, 0, len(seats)), make([]string, 0, len(seats))
	for _, seat := range seats {
		where = append(where, seat.Room+"/"+seat.Seat)
		hashes = append(hashes, seat.Hash)
	}
	if want := []string{"eng/coder", "eng/reviewer"}; !reflect.DeepEqual(where, want) {
		t.Fatalf("seats = %v, want %v", where, want)
	}
	if want := []string{"ab12", ""}; !reflect.DeepEqual(hashes, want) {
		t.Errorf("pinned hashes = %v, want %v: the valid pin is used and an unpinned seat reads empty", hashes, want)
	}
}

// TestAMalformedPinRefusesTheSeatsReport: the file the bridge could not read is not "this seat has no pin".
// The build answers an error naming the file, the payload is not built, and a cycle records the refusal and
// sends nothing (7.12). Before this, the same file put an empty hash on the wire and the server stored it as
// the seat's pin.
func TestAMalformedPinRefusesTheSeatsReport(t *testing.T) {
	for i, contents := range []string{`{"hash": "../evil"}`, `{not json`, `{"hash": "ab12"`} {
		t.Run(fmt.Sprintf("pin-%d", i), func(t *testing.T) {
			pinsDir := t.TempDir()
			dir := filepath.Join(pinsDir, "eng", "coder")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "pinned.json"), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			nodes, err := parseItems(`[{"roomName":"eng","logicalId":"eng.coder","runtime":"omp"}]`, "rig ps")
			if err != nil {
				t.Fatalf("unwrapping the fixture: %v", err)
			}
			seats, _, _, err := BuildSeats(nodes, nil, pinsDir, false)
			if err == nil {
				t.Fatalf("BuildSeats built %d seat(s) from a pin it could not read", len(seats))
			}
			if !strings.Contains(err.Error(), "pinned.json") {
				t.Errorf("error %q does not name the file: the note must be actionable without a second lookup", err)
			}
		})
	}
}
