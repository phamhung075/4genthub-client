package clientbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// pythonDuplicateLines reads the duplicate warnings the Python bridge printed on stderr, so the
// comparison is against that text rather than against an expectation of mine.
func pythonDuplicateLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "python_dump.stderr.txt"))
	if err != nil {
		t.Fatalf("read the Python stderr fixture: %v", err)
	}
	out := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "rename one") {
			continue
		}
		out = append(out, strings.TrimPrefix(line, "openrig-bridge: "))
	}
	sort.Strings(out)
	return out
}

// The fixtures are captured from the REFERENCE implementation, not written by hand:
//
//	rig_ps.json          - what `rig ps --json --nodes -A` printed when the Python bridge was run
//	python_dump.json     - what `python3 scripts/openrig_bridge.py once --print --machine-id test-machine`
//	                       printed with HOME pointed at a scratch tree carrying ONE pinned seat
//	builders_reference.json - outputs of the Python builders for inputs the dump cannot exercise
//	                       (herdr was unavailable, so no agents rode the dump)
//
// The pinned seat matters: with every hash empty, the hash field would be compared as always-empty
// and the test would be about a field that cannot fail.

type referenceDump struct {
	MachineID   string        `json:"machine_id"`
	Seats       []SeatStatus  `json:"seats"`
	Agents      []AgentStatus `json:"agents"`
	LocalRecord map[string]struct {
		Running         string `json:"running"`
		LastInSync      string `json:"last_in_sync"`
		SinceLastInSync string `json:"since_last_in_sync"`
	} `json:"local_record"`
}

type buildersReference struct {
	AgentsInput  []any `json:"agents_input"`
	AgentsExpect []struct {
		Agent  string `json:"agent"`
		Status string `json:"status"`
		PaneID string `json:"pane_id"`
	} `json:"agents_expect"`
	DuplicateCases []struct {
		Room   string   `json:"room"`
		Seat   string   `json:"seat"`
		Pods   []string `json:"pods"`
		Expect string   `json:"expect"`
	} `json:"duplicate_cases"`
	MachineIDCases []struct {
		In     string `json:"in"`
		Expect string `json:"expect"`
	} `json:"machine_id_cases"`
}

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
}

// TestPayloadParityWithThePythonBridge drives the Go builders with THE SAME INPUTS the Python bridge
// read and compares every reportable field: the seats (state, runtime, hash, detail, redacted), the
// agents, and the local record's three words.
func TestPayloadParityWithThePythonBridge(t *testing.T) {
	var dump referenceDump
	readFixture(t, "python_dump.json", &dump)

	nodesRaw, err := os.ReadFile(filepath.Join("testdata", "rig_ps.json"))
	if err != nil {
		t.Fatalf("read rig_ps.json: %v", err)
	}
	nodes, err := ParseRigNodes(string(nodesRaw))
	if err != nil {
		t.Fatalf("ParseRigNodes: %v", err)
	}

	// The one pinned seat the dump carries, placed where the builder reads pins from.
	pinsDir := t.TempDir()
	pinDir := filepath.Join(pinsDir, "4genthub-min", "go-dev2")
	if err := os.MkdirAll(pinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pin, err := os.ReadFile(filepath.Join("testdata", "pins-4genthub-min-go-dev2.json"))
	if err != nil {
		t.Fatalf("read the pin fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pinDir, "pinned.json"), pin, 0o644); err != nil {
		t.Fatal(err)
	}

	seats, invalid, duplicates := BuildSeats(nodes, nil, pinsDir, false)
	if invalid != 0 {
		t.Errorf("invalid=%d, want 0: the fixture has only well-formed names", invalid)
	}
	// The fixture's `rig ps` output DOES carry duplicated seat keys, and the Python printed one line
	// per duplicate on stderr. Comparing against that text rather than against an expectation of
	// mine is what makes this a parity assertion.
	wantDuplicates := pythonDuplicateLines(t)
	if len(duplicates) != len(wantDuplicates) {
		t.Fatalf("duplicates = %v, want %v", duplicates, wantDuplicates)
	}
	for i := range duplicates {
		if duplicates[i] != wantDuplicates[i] {
			t.Errorf("duplicate %d:\n  go     %s\n  python %s", i, duplicates[i], wantDuplicates[i])
		}
	}
	if len(seats) != len(dump.Seats) {
		t.Fatalf("seats = %d, want %d (the Python reported %d)", len(seats), len(dump.Seats), len(dump.Seats))
	}
	if !reflect.DeepEqual(seats, dump.Seats) {
		for i := range seats {
			if !reflect.DeepEqual(seats[i], dump.Seats[i]) {
				t.Errorf("seat %d:\n  go     %+v\n  python %+v", i, seats[i], dump.Seats[i])
			}
		}
	}

	// The pinned-seat requirement, asserted rather than assumed: a fixture without one compares the
	// hash field as always-empty.
	if !HasPinnedSeat(seats) {
		t.Fatal("the fixture carries no pinned seat, so the hash field is being compared as always-empty")
	}
	pinned := 0
	for _, seat := range seats {
		if seat.Hash != "" {
			pinned++
		}
	}
	if pinned != 1 {
		t.Errorf("pinned seats = %d, want exactly the one the fixture pins", pinned)
	}
}

// TestLocalRecordParityWithThePythonBridge compares the three words this machine may say, which the
// dump carries for every seat. The Go bridge starts with an EMPTY record, exactly as the Python did
// with a fresh HOME, so every seat must read unknown - and the comparison is against the dump rather
// than against the code's own idea of it.
func TestLocalRecordParityWithThePythonBridge(t *testing.T) {
	var dump referenceDump
	readFixture(t, "python_dump.json", &dump)

	bridge := NewBridge(dump.MachineID, 0, nil, nil, t.TempDir(), filepath.Join(t.TempDir(), "state.json"), nil)
	got := bridge.LocalRecord(dump.Seats)
	if len(got) != len(dump.LocalRecord) {
		t.Fatalf("local record = %d entries, want %d", len(got), len(dump.LocalRecord))
	}
	for where, want := range dump.LocalRecord {
		note, ok := got[where]
		if !ok {
			t.Errorf("%s: missing from the Go record", where)
			continue
		}
		if note.Running != want.Running || note.LastInSync != want.LastInSync || note.SinceLastInSync != want.SinceLastInSync {
			t.Errorf("%s:\n  go     %+v\n  python %+v", where, note, want)
		}
	}
}

// TestBuilderParityWithThePythonBridge covers the two builders the dump could not exercise, because
// herdr was unavailable when it was taken: the agent allow-list and the duplicate message.
func TestBuilderParityWithThePythonBridge(t *testing.T) {
	var reference buildersReference
	readFixture(t, "builders_reference.json", &reference)

	agents := BuildAgents(reference.AgentsInput)
	if len(agents) != len(reference.AgentsExpect) {
		t.Fatalf("agents = %d, want %d", len(agents), len(reference.AgentsExpect))
	}
	for i, want := range reference.AgentsExpect {
		if agents[i].Agent != want.Agent || agents[i].Status != want.Status || agents[i].PaneID != want.PaneID {
			t.Errorf("agent %d:\n  go     %+v\n  python %+v", i, agents[i], want)
		}
	}

	for _, c := range reference.DuplicateCases {
		if got := DuplicateMessage(c.Room, c.Seat, c.Pods); got != c.Expect {
			t.Errorf("duplicate message for %v:\n  go     %s\n  python %s", c.Pods, got, c.Expect)
		}
	}

	for _, c := range reference.MachineIDCases {
		if got := SanitizeMachineID(c.In); got != c.Expect {
			t.Errorf("machine id %q = %q, want %q", c.In, got, c.Expect)
		}
	}
}
