package clientbridge

import "testing"

// TestSeatStateMapping is the Go half of tests/test_bridge.py::test_seat_state_mapping (at 1663257):
// its ten (node, expected) rows, in the Python's order, each asserted through the exported SeatState.
//
// The row cannot be folded into the captured dump, which is why it is here rather than left to the
// parity test: the dump carries stopped 26, running 5 and unknown 5 - ZERO blocked and ZERO idle - so
// those two states are reached by no other test in this package, and the dead-agent reading and the
// two rows that must NOT be called dead are the discriminations SeatState's doc describes.
//
// The rows are the Python's, not a re-derivation. Two of them carry measurements worth keeping whole:
// the dead-agent row is the F4 reproduction of 2026-10-04 (a live tmux session, lifecycle
// attention_required, the runtime hook gone -> stopped, and the seat is respawned rather than called
// blocked or unknown), and the row beside it is the one that must not be called dead (an omp-style
// cold start reports state unknown with NO reason, so the reason is the discriminator).
func TestSeatStateMapping(t *testing.T) {
	rows := []struct {
		what string
		node map[string]any
		want string
	}{
		{
			"a stopped session reads stopped even with a running activity",
			map[string]any{"sessionStatus": "stopped", "agentActivity": map[string]any{"state": "running"}},
			"stopped",
		},
		{
			"a recoverable lifecycle reads stopped",
			map[string]any{"lifecycleState": "recoverable"},
			"stopped",
		},
		{
			"a live session whose runtime hook is gone reads stopped",
			map[string]any{
				"sessionStatus":  "running",
				"startupStatus":  "ready",
				"lifecycleState": "attention_required",
				"agentActivity":  map[string]any{"state": "unknown", "reason": "no_runtime_hook"},
			},
			"stopped",
		},
		{
			"an activity that is unknown with no reason is NOT called dead",
			map[string]any{
				"sessionStatus":  "running",
				"startupStatus":  "ready",
				"lifecycleState": "running",
				"agentActivity":  map[string]any{"state": "unknown"},
			},
			"unknown",
		},
		{
			"attention_required alone is not a blocked signal",
			map[string]any{"lifecycleState": "attention_required"},
			"unknown",
		},
		{
			"needs_input is blocked",
			map[string]any{
				"lifecycleState": "attention_required",
				"agentActivity":  map[string]any{"state": "needs_input"},
			},
			"blocked",
		},
		{
			"a failed startup is blocked",
			map[string]any{"startupStatus": "failed"},
			"blocked",
		},
		{
			"idle stays idle",
			map[string]any{"agentActivity": map[string]any{"state": "idle"}},
			"idle",
		},
		{
			"an activity word this client has not seen is unknown",
			map[string]any{"agentActivity": map[string]any{"state": "bogus"}},
			"unknown",
		},
		{
			"an empty node is unknown",
			map[string]any{},
			"unknown",
		},
	}
	for _, row := range rows {
		t.Run(row.what, func(t *testing.T) {
			if got := SeatState(row.node); got != row.want {
				t.Errorf("SeatState(%v) = %q, want %q", row.node, got, row.want)
			}
		})
	}
}

// TestRuntimeMappingKeepsEverySupportedRuntime is the Go half of
// tests/test_bridge.py::test_runtime_mapping_keeps_every_supported_runtime (at 1663257): every runtime
// a report may carry reaches the seat unchanged, and a value outside the set reads unknown. The Python
// row drove the whole bridge (build_payload) rather than the mapper, so this one goes through the
// exported door, BuildSeats, and reads the Runtime field a report carries.
//
// The Python's parametrize list is claude-code, codex, agy, terminal and an unsupported value. The
// captured dump alone would leave codex untested - it carries omp, claude-code, agy and terminal - and
// the dump is also why omp is in this list on top of the Python's: the Python's own payload emitted
// omp, and the Go runtime set at payload.go:28 supports it, so a runtime either client reports must
// not be swallowed as unknown.
func TestRuntimeMappingKeepsEverySupportedRuntime(t *testing.T) {
	known, pinsDir := map[string]string{}, t.TempDir()
	seat := func(rt string) []any {
		return []any{map[string]any{
			"roomName":      "eng",
			"logicalId":     "dev.solo",
			"runtime":       rt,
			"agentActivity": map[string]any{"state": "idle"},
		}}
	}
	for _, rt := range []string{"claude-code", "codex", "agy", "omp", "terminal"} {
		t.Run(rt, func(t *testing.T) {
			seats, invalid, duplicates, err := BuildSeats(seat(rt), known, pinsDir, false)
			if err != nil {
				t.Fatalf("BuildSeats refused: %v", err)
			}
			if len(seats) != 1 || invalid != 0 || len(duplicates) != 0 {
				t.Fatalf("BuildSeats = %d seats, %d invalid, %d duplicates, want 1, 0, 0", len(seats), invalid, len(duplicates))
			}
			if seats[0].Runtime != rt {
				t.Errorf("Runtime = %q, want %q: a supported runtime is not reported as unknown", seats[0].Runtime, rt)
			}
		})
	}
	t.Run("a runtime outside the set reads unknown", func(t *testing.T) {
		seats, _, _, err := BuildSeats(seat("weird-runtime"), known, pinsDir, false)
		if err != nil {
			t.Fatalf("BuildSeats refused: %v", err)
		}
		if len(seats) != 1 {
			t.Fatalf("BuildSeats returned %d seats, want 1", len(seats))
		}
		if seats[0].Runtime != "unknown" {
			t.Errorf("Runtime = %q, want unknown: an unseen runtime reads unknown rather than passing through", seats[0].Runtime)
		}
	})
}
