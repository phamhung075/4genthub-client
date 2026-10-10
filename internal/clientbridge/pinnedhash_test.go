package clientbridge

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestPinnedHashUsedWhenValid mirrors tests/test_bridge.py::test_pinned_hash_used_when_valid (at
// 1663257), which asserts BOTH halves of one row over the same two seats the Python used:
//
//	pins eng/coder    {"hash": "ab12"}     -> "ab12" IS used as the seat's hash
//	pins eng/reviewer {"hash": "../evil"}  -> "" - not used as a hash at all
//
// The invalid half is the security-adjacent one: payload.go:177 is the guard that a pin whose value
// does not match hashPattern never travels as a hash, and until this test no test in this package
// asserted it (payload.go was the only file naming hashPattern). The invariant is the PATTERN and not
// the presence of a dot-dot: hashPattern is \A[A-Za-z0-9][A-Za-z0-9._-]{0,127}\z, so the FIRST
// character must be alphanumeric while dots are legal after it - ../evil fails on its leading '.',
// where a value like aa..bb would be ACCEPTED. Reading the rule as "dots are rejected" would be wrong
// about why the code works, which is why the row's own value is used here and the pattern named.
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

	// Half one: the reader itself.
	if got := PinnedHash(pinsDir, "eng", "coder"); got != "ab12" {
		t.Errorf("a well-formed pin read as %q, want ab12: a valid pinned hash IS the seat's hash", got)
	}
	if got := PinnedHash(pinsDir, "eng", "reviewer"); got != "" {
		t.Errorf("an invalid pin read as %q, want the empty string: ../evil must never be used as a hash", got)
	}

	// Half two, where the Python asserted it: the built payload's hash field, seat by seat.
	nodes, err := ParseRigNodes(`[
		{"rigName":"eng","logicalId":"eng.coder","runtime":"omp","sessionStatus":"running","agentActivity":{"state":"working"}},
		{"rigName":"eng","logicalId":"eng.reviewer","runtime":"omp","sessionStatus":"running","agentActivity":{"state":"working"}}
	]`)
	if err != nil {
		t.Fatalf("ParseRigNodes: %v", err)
	}
	seats, invalid, _ := BuildSeats(nodes, nil, pinsDir, false)
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
		t.Errorf("pinned hashes = %v, want %v: the valid pin is used and the traversal shape is not", hashes, want)
	}
}
