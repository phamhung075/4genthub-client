package clientwatch

import (
	"slices"
	"strings"
	"testing"
)

// The wall's geometry: the plan is pure, so the owner's shape is asserted on indices and ratios rather
// than on a running herdr.

func TestGridPlanCarvesTheOwnersWall(t *testing.T) {
	layout := gridPlan(5)
	wantSplits := []gridSplit{
		{Parent: 0, Direction: "down", Ratio: gridTopRatio}, // the full-width bottom band
		{Parent: 1, Direction: "right", Ratio: 0.5},         // its two seats, side by side
		{Parent: 0, Direction: "right", Ratio: 0.5},         // the top band's right column
		{Parent: 3, Direction: "down", Ratio: 0.5},          // the second seat stacked under it
	}
	if !slices.Equal(layout.Splits, wantSplits) {
		t.Fatalf("splits = %+v, want %+v", layout.Splits, wantSplits)
	}
	wantSlots := []int{0, 3, 4, 1, 2}
	if !slices.Equal(layout.Slots, wantSlots) {
		t.Fatalf("slots = %v, want %v", layout.Slots, wantSlots)
	}
}

func TestGridPlanLeavesNoPaneEmpty(t *testing.T) {
	for seats := 1; seats <= 12; seats++ {
		layout := gridPlan(seats)
		if got := len(layout.Splits) + 1; got != seats {
			t.Errorf("%d seats: plan creates %d panes", seats, got)
		}
		if len(layout.Slots) != seats {
			t.Errorf("%d seats: %d slots", seats, len(layout.Slots))
		}
		seen := make(map[int]bool, seats)
		for _, slot := range layout.Slots {
			if slot < 0 || slot >= seats {
				t.Fatalf("%d seats: slot %d is outside the panes the plan creates", seats, slot)
			}
			if seen[slot] {
				t.Fatalf("%d seats: pane %d gets two seats", seats, slot)
			}
			seen[slot] = true
		}
		created := 1
		for _, step := range layout.Splits {
			if step.Parent < 0 || step.Parent >= created {
				t.Fatalf("%d seats: split of pane %d before it exists", seats, step.Parent)
			}
			if step.Direction != "right" && step.Direction != "down" {
				t.Fatalf("%d seats: direction %q", seats, step.Direction)
			}
			if step.Ratio <= 0 || step.Ratio >= 1 {
				t.Fatalf("%d seats: ratio %v is not a share", seats, step.Ratio)
			}
			created++
		}
	}
}

func TestOrderSeatsReadsTheWallLeadFirst(t *testing.T) {
	got := orderSeats([]string{"reviewer", "fe-dev", "go-dev", "lead", "architect", "smol"})
	want := []string{"lead", "architect", "go-dev", "fe-dev", "reviewer", "smol"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// The wiring the plan cannot show: the default two columns place each seat in its own pane, with no
// sixth split left over, and the lead is the root pane.
func TestGridRunsTheOwnersWall(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr(nil)
	fake.splitIDs = []string{"w25:p2", "w25:p3", "w25:p4", "w25:p5"}
	fake.seatsFromTmux("r", []string{"architect", "fe-dev", "go-dev", "lead", "reviewer"})
	withSeatStore(t, "r", "architect", "fe-dev", "go-dev", "lead", "reviewer")

	code, out, errOut := runVerbT(t, "grid", "--room", "r")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "grid: 5 seats in 2 columns") {
		t.Errorf("stdout = %q", out)
	}

	splits := [][]string{}
	renamed := map[string]string{}
	for _, call := range fake.callsOf("herdr") {
		if len(call) < 3 || call[1] != "pane" {
			continue
		}
		switch call[2] {
		case "split":
			splits = append(splits, call[3:])
		case "rename":
			if len(call) >= 5 {
				renamed[call[3]] = call[4]
			}
		}
	}
	wantSplits := []string{
		"--pane w25:p1 --direction down --ratio 0.667",
		"--pane w25:p2 --direction right --ratio 0.500",
		"--pane w25:p1 --direction right --ratio 0.500",
		"--pane w25:p4 --direction down --ratio 0.500",
	}
	if len(splits) != len(wantSplits) {
		t.Fatalf("%d splits, want %d: %v", len(splits), len(wantSplits), splits)
	}
	for index, want := range wantSplits {
		got := splits[index]
		if len(got) > 6 {
			got = got[:6]
		}
		if joined := strings.Join(got, " "); joined != want {
			t.Errorf("split %d = %q, want %q", index, joined, want)
		}
	}
	wantSeats := map[string]string{
		"w25:p1": "lead",
		"w25:p4": "architect",
		"w25:p5": "go-dev",
		"w25:p2": "fe-dev",
		"w25:p3": "reviewer",
	}
	for pane, seat := range wantSeats {
		if got := renamed[pane]; got != seat {
			t.Errorf("pane %s = seat %q, want %q (renames: %v)", pane, got, seat, renamed)
		}
	}
}
