// The runtime rows of `doctor`: one row per runtime this client launches on this machine, named `guard_<runtime>`,
// and the lines each row prints.
package clientlifecycle

import (
	"errors"
	"strings"
	"testing"

	"github.com/phamhung075/4genthub-client/internal/clientguard"
	"github.com/phamhung075/4genthub-client/internal/clientservices"
)

// rowsFromHooks makes runtimeRowsFor answer fixed hook states, so a case names the machine without a PATH.
func rowsFromHooks(t *testing.T, hooks []clientguard.RuntimeHook) {
	t.Helper()
	previousHome, previousHooks := runtimeHome, runtimeHooks
	runtimeHome = func() (string, error) { return "/home/reader", nil }
	runtimeHooks = func(string) []clientguard.RuntimeHook { return hooks }
	t.Cleanup(func() { runtimeHome, runtimeHooks = previousHome, previousHooks })
}

// TestOneRowPerDetectedRuntimeSaysWhereItIsAndWhatItsConfigSays is the row's whole report: the binary's path, the
// entry in the owner-wide file named ABSOLUTELY (the row writes one path and must report the same one), and the row
// naming the GUARD in that runtime rather than the runtime alone.
func TestOneRowPerDetectedRuntimeSaysWhereItIsAndWhatItsConfigSays(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{
		{Runtime: "agy", Bin: "/usr/bin/agy", Dir: "/home/reader/.gemini/config",
			Config: ".gemini/config/hooks.json", Mode: "--agy-hook", EntryPresent: true},
		{Runtime: "claude", Bin: "/usr/bin/claude", Dir: "/home/reader/.claude",
			Config: ".claude/settings.json", Mode: "--claude-hook"},
	})
	rows := runtimeRowsFor("")
	if len(rows) != 2 {
		t.Fatalf("two detected runtimes are two rows: %+v", rows)
	}
	if rows[0].Row.Safeguard != "guard_agy" || rows[1].Row.Safeguard != "guard_claude" {
		t.Fatalf("the rows are named for the guard in each runtime: %+v", rows)
	}
	if rows[0].Row.Room != "" {
		t.Errorf("an owner-wide config belongs to the machine, so the row carries no room: %+v", rows[0].Row)
	}
	for _, row := range rows {
		if row.Row.State != clientservices.Running {
			t.Errorf("%s is a reading, never a refusal: %+v", row.Row.Safeguard, row.Row)
		}
	}

	placed := strings.Join(runtimeLines(rows[0]), "\n")
	if !strings.Contains(placed, "found at /usr/bin/agy") {
		t.Errorf("the line says where the binary is: %s", placed)
	}
	if !strings.Contains(placed, "the guard's entry is in /home/reader/.gemini/config/hooks.json") {
		t.Errorf("the line names the file absolutely, the path install writes: %s", placed)
	}

	// claude has no mode in the guard this client ships: the row says so and does not claim an entry.
	gated := strings.Join(runtimeLines(rows[1]), "\n")
	if !strings.Contains(gated, "no entry is placed") || !strings.Contains(gated, "--claude-hook") {
		t.Errorf("the line names the missing mode: %s", gated)
	}
	if strings.Contains(gated, "the guard's entry is in") {
		t.Errorf("a row whose mode the guard lacks must not read as guarded: %s", gated)
	}
}

// TestARuntimeInNoConfigAndOnNoPathIsNotARow: a machine without agy says nothing about agy, so a reader does not see
// a row for a runtime they never installed.
func TestARuntimeInNoConfigAndOnNoPathIsNotARow(t *testing.T) {
	rowsFromHooks(t, nil)
	if rows := runtimeRowsFor(""); len(rows) != 0 {
		t.Fatalf("an empty machine has no runtime rows: %+v", rows)
	}
}

// TestARuntimeFoundOnlyByItsConfigDirectoryStillGetsARow: the binary can leave the PATH while the config stays -
// a nvm or mise shell, a machine midway through an uninstall - and the entry is still what a seat would read.
func TestARuntimeFoundOnlyByItsConfigDirectoryStillGetsARow(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{{Runtime: "agy", Dir: "/home/reader/.gemini/config",
		Config: ".gemini/config/hooks.json", Mode: "--agy-hook"}})
	lines := strings.Join(runtimeLines(runtimeRowsFor("")[0]), "\n")
	if !strings.Contains(lines, "by its config directory /home/reader/.gemini/config") {
		t.Errorf("the line names the directory that found it: %s", lines)
	}
}

// TestARuntimeWithNoHookSurfaceSaysSoAndIsNotARefusal is codex's row: this client launches it with no settings file
// and no command hook, so nothing is placed and the row says why rather than going quiet.
func TestARuntimeWithNoHookSurfaceSaysSoAndIsNotARefusal(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{{Runtime: "codex", Bin: "/usr/bin/codex", Dir: "/home/reader/.codex"}})
	rows := runtimeRowsFor("")
	if len(rows) != 1 || rows[0].Row.State != clientservices.Running || rows[0].Row.Safeguard != "guard_codex" {
		t.Fatalf("codex's row is a reading: %+v", rows)
	}
	lines := strings.Join(runtimeLines(rows[0]), "\n")
	if !strings.Contains(lines, "no entry is placed") || !strings.Contains(lines, "no hook surface") {
		t.Errorf("the row says there is no surface to place one on: %s", lines)
	}
}

// TestAConfigThatCannotBeReadIsALineAndNotAFailure: the owner's file is theirs, and doctor reports the read it could
// not make rather than refusing the whole report for it.
func TestAConfigThatCannotBeReadIsALineAndNotAFailure(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{{Runtime: "agy", Bin: "/usr/bin/agy",
		Config: ".gemini/config/hooks.json", Mode: "--agy-hook", ReadError: "hooks.json is not JSON"}})
	rows := runtimeRowsFor("")
	if rows[0].Row.State != clientservices.Running {
		t.Errorf("an unreadable config is not a refusal: %+v", rows[0].Row)
	}
	lines := strings.Join(runtimeLines(rows[0]), "\n")
	if !strings.Contains(lines, "hooks.json is not JSON") {
		t.Errorf("the line carries the read error: %s", lines)
	}
}

// TestTheRowForAHomeThatCannotBeResolvedPrintsItsReason is the gate's M3: the row decides nothing, and a bare
// `running` with no line under it is a report that cannot say why. The reason is in the row's line, which is also
// what `--json` carries as its reasons.
func TestTheRowForAHomeThatCannotBeResolvedPrintsItsReason(t *testing.T) {
	previous := runtimeHome
	runtimeHome = func() (string, error) { return "", errors.New("no registered home") }
	t.Cleanup(func() { runtimeHome = previous })
	rows := runtimeRowsFor("")
	if len(rows) != 1 || !strings.Contains(rows[0].Reason, "no registered home") {
		t.Fatalf("an unreadable home is one row naming the reason: %+v", rows)
	}
	lines := runtimeLines(rows[0])
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "no registered home") {
		t.Fatalf("the reason reaches the printed line, not only the struct: %v", lines)
	}
}

// TestTheDoctorReportCarriesTheRuntimeRows is the gate's m11: nothing outside this package's own case asserted that
// the report carries these rows, so deleting the doctor's wiring left the suite green. The JSON is built through the
// same functions the report uses - doctorJSONRows over rowReasons - so the assertion is on the surface a reader
// sees, not on the helper this file happens to call.
func TestTheDoctorReportCarriesTheRuntimeRows(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{{Runtime: "agy", Bin: "/usr/bin/agy",
		Config: ".gemini/config/hooks.json", Mode: "--agy-hook"}})
	runtimeRows := runtimeRowFor("")
	health := []clientservices.Health{runtimeRows[0].Row}
	reported := doctorJSONRows(health, nil, nil, nil, runtimeRows, nil, nil, nil, nil)
	if len(reported) != 1 {
		t.Fatalf("the report carries the row: %+v", reported)
	}
	reasons := strings.Join(reported[0].Reasons, "\n")
	if !strings.Contains(reasons, "guard_agy: found at /usr/bin/agy") {
		t.Fatalf("the row's reasons are the runtime lines: %v", reported[0].Reasons)
	}
}
