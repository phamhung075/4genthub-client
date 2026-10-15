package clientwatch

import (
	"strings"
	"testing"
)

// The verb-level contract: argument handling and the panels a verb opens, with the exec seam faked.
// The renderer's own pure-function tests live in render_test.go.

// Mirrors test_inputs_open_for_one_seat_leaves_the_others_without_one: the FILTERED open. The
// all-seats open is one test over, in watch_test.go; --seat narrows it to exactly the named seat, and a
// seat outside the filter must get no pane at all - not a split, not a rename.
func TestInputsOpenForOneSeatLeavesTheOthersWithoutOne(t *testing.T) {
	fake := newFakeExec(t)
	fake.answerHerdr([]map[string]any{paneEntry("lead"), paneEntry("go-dev")})
	withSeatStore(t, "r", "lead", "go-dev")

	code, out, errOut := runVerbT(t, "inputs", "open", "--room", "r", "--seat", "lead")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	splits, renamed := 0, []string{}
	for _, call := range fake.callsOf("herdr") {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "pane split") {
			splits++
		}
		if strings.Contains(joined, "pane rename") {
			renamed = append(renamed, call[len(call)-1])
		}
	}
	if splits != 1 {
		t.Errorf("pane splits = %d, want only the named seat's", splits)
	}
	if len(renamed) != 1 || renamed[0] != "lead"+inputSuffix {
		t.Errorf("renamed panes = %v, want only %q", renamed, "lead"+inputSuffix)
	}
	if strings.Contains(out, "go-dev") {
		t.Errorf("a seat outside the filter was opened: %q", out)
	}
	if !strings.Contains(out, "inputs: opened 1") {
		t.Errorf("stdout = %q", out)
	}
}
