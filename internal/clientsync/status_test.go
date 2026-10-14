package clientsync

import (
	"bytes"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// TestASeatIsBehindWhenItsPinDiffersOrIsMissing is the PARTY SPEC's own case, ported verbatim from
// agenthub_client/tests/test_seat_client.py (it lived at the retired Python seat_client test until
// 2026-10-09, when the scripts left the tree, and at agenthub_main/src/tests/scripts/ until 2026-10-08):
//
//	cloud  = lead:h2, go-dev:h1, writer:h3
//	pinned = lead:h1, go-dev:h1, writer:None   ->  [lead, writer]
//
// It is ported as an assertion on the SAME three seats and the SAME expected order, because the order
// is part of what the Python returns.
func TestASeatIsBehindWhenItsPinDiffersOrIsMissing(t *testing.T) {
	order := []string{"lead", "go-dev", "writer"}
	cloud := map[string]string{"lead": "h2", "go-dev": "h1", "writer": "h3"}
	// The Python's `writer: None` is an ABSENT key here, not an empty string: absence is what
	// pinned_hashes produces for a seat with no lock file, and an empty pin is a different thing (see
	// TestAnEmptyPinIsBehindRatherThanNotPulled).
	pinned := map[string]string{"lead": "h1", "go-dev": "h1"}

	got := SeatsBehind(order, cloud, pinned)
	if len(got) != 2 || got[0] != "lead" || got[1] != "writer" {
		t.Fatalf("SeatsBehind = %v, want [lead writer]", got)
	}

	// The three words the Python prints, one per seat.
	for seat, want := range map[string]string{"lead": "BEHIND", "go-dev": "in sync", "writer": "not pulled"} {
		if state := StatusState(seat, cloud, pinned); state != want {
			t.Errorf("StatusState(%s) = %q, want %q", seat, state, want)
		}
	}
}

// TestAnEmptyPinIsBehindRatherThanNotPulled pins the distinction the Python draws with `pinned is None`:
//
//	state = "in sync" if seat not in behind else ("not pulled" if pinned is None else "BEHIND")
//
// A lock file carrying `"hash": ""` passes read_lock - "" is a string, and read_lock validates nothing
// else - so it reaches the status line as an EMPTY PIN and the Python renders BEHIND. Only a MISSING
// entry, which is what no lock file at all produces, is "not pulled". Folding the two together reads a
// malformed-but-valid lock as a seat that was never pulled, which is the wrong story to give an operator.
func TestAnEmptyPinIsBehindRatherThanNotPulled(t *testing.T) {
	cloud := map[string]string{"seat": "h1"}
	if got := StatusState("seat", cloud, map[string]string{"seat": ""}); got != "BEHIND" {
		t.Errorf("StatusState with an empty pin = %q, want BEHIND (Python's `pinned is None` is false here)", got)
	}
	if got := StatusState("seat", cloud, map[string]string{}); got != "not pulled" {
		t.Errorf("StatusState with no entry = %q, want not pulled (that is the Python's None)", got)
	}
}

// TestStatusRendersThePythonsColumns: the parity is the LINE, not only the word beside it - the seat
// padded to 14, the pin truncated to 12 with "None" for a missing one, the cloud hash likewise.
func TestStatusRendersThePythonsColumns(t *testing.T) {
	order := []string{"lead", "writer"}
	cloud := map[string]string{"lead": "aaaaaaaaaaaa9999", "writer": "bbbbbbbbbbbb8888"}
	// writer is ABSENT, which is the Python's None and therefore the row that renders "None" and "not
	// pulled"; an empty pin is BEHIND, pinned separately.
	pinned := map[string]string{"lead": "aaaaaaaaaaaa1111"}

	var out bytes.Buffer
	if code := RunStatus(&out, order, cloud, pinned); code != clientcmd.ExitBehind {
		t.Fatalf("exit = %d, want %d (a seat is behind)", code, clientcmd.ExitBehind)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("status printed %d lines, want one per seat:\n%s", len(lines), out.String())
	}
	if want := "lead           pinned aaaaaaaaaaaa  cloud aaaaaaaaaaaa  BEHIND"; lines[0] != want {
		t.Errorf("line 1 = %q\n        want %q", lines[0], want)
	}
	if !strings.Contains(lines[1], "None") || !strings.Contains(lines[1], "not pulled") {
		t.Errorf("line 2 = %q, want the None pin and the not-pulled word", lines[1])
	}
}

// TestStatusIsOkWhenEverySeatIsInSync: the exit code is the contract a caller scripts against, so the
// happy path is pinned as well as the behind case.
func TestStatusIsOkWhenEverySeatIsInSync(t *testing.T) {
	order := []string{"lead"}
	cloud := map[string]string{"lead": "h1"}
	pinned := map[string]string{"lead": "h1"}

	var out bytes.Buffer
	if code := RunStatus(&out, order, cloud, pinned); code != clientcmd.ExitOK {
		t.Fatalf("exit = %d, want %d", code, clientcmd.ExitOK)
	}
	if !strings.Contains(out.String(), "in sync") {
		t.Errorf("output = %q, want the in-sync word", out.String())
	}
}
