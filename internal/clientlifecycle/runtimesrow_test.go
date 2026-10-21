// The runtime rows of `doctor`: one row per runtime CLI this machine has, named `guard_<runtime>`, and its lines.
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
// entry in the owner-wide file, and the row naming the GUARD in that runtime rather than the runtime alone.
func TestOneRowPerDetectedRuntimeSaysWhereItIsAndWhatItsConfigSays(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{
		{Runtime: "claude", Bin: "/usr/bin/claude", Dir: "/home/reader/.claude",
			Config: ".claude/settings.json", Mode: "--claude-hook", EntryPresent: true},
		{Runtime: "agy", Bin: "/usr/bin/agy", Dir: "/home/reader/.gemini/config",
			Config: ".gemini/config/hooks.json", Mode: "--agy-hook",
			Note: "agy: found, and the guard's entry is not in /home/reader/.gemini/config/hooks.json"},
	})
	rows := runtimeRowsFor("")
	if len(rows) != 2 {
		t.Fatalf("two detected runtimes are two rows: %+v", rows)
	}
	if rows[0].Row.Safeguard != "guard_claude" || rows[1].Row.Safeguard != "guard_agy" {
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
	if !strings.Contains(placed, "found at /usr/bin/claude") {
		t.Errorf("the line says where the binary is: %s", placed)
	}
	if !strings.Contains(placed, "the guard's entry is in .claude/settings.json") {
		t.Errorf("the line says which file carries the entry: %s", placed)
	}

	missing := strings.Join(runtimeLines(rows[1]), "\n")
	if !strings.Contains(missing, "no seat-guard entry in .gemini/config/hooks.json") {
		t.Errorf("a missing entry names the file it should be in: %s", missing)
	}
	if !strings.Contains(missing, "guard install") {
		t.Errorf("a missing entry names the verb that writes it: %s", missing)
	}
}

// TestARuntimeInNoConfigAndOnNoPathIsNotARow: a machine without claude says nothing about claude, so a reader does
// not see a row for a runtime they never installed.
func TestARuntimeInNoConfigAndOnNoPathIsNotARow(t *testing.T) {
	rowsFromHooks(t, nil)
	if rows := runtimeRowsFor(""); len(rows) != 0 {
		t.Fatalf("an empty machine has no runtime rows: %+v", rows)
	}
}

// TestARuntimeFoundOnlyByItsConfigDirectoryStillGetsARow: the binary can leave the PATH while the config stays -
// a nvm or mise shell, a machine midway through an uninstall - and the entry is still what a seat would read.
func TestARuntimeFoundOnlyByItsConfigDirectoryStillGetsARow(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{{Runtime: "claude", Dir: "/home/reader/.claude",
		Config: ".claude/settings.json", Mode: "--claude-hook", EntryPresent: false}})
	lines := strings.Join(runtimeLines(runtimeRowsFor("")[0]), "\n")
	if !strings.Contains(lines, "by its config directory /home/reader/.claude") {
		t.Errorf("the line names the directory that found it: %s", lines)
	}
}

// TestARuntimeWithNoGuardModeSaysSoAndIsNotARefusal is gemini's row: the guard has no mode for it, nothing is
// placed, and the row says why rather than going quiet.
func TestARuntimeWithNoGuardModeSaysSoAndIsNotARefusal(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{{Runtime: "gemini", Bin: "/usr/bin/gemini", Dir: "/home/reader/.gemini"}})
	rows := runtimeRowsFor("")
	if len(rows) != 1 || rows[0].Row.State != clientservices.Running {
		t.Fatalf("gemini's row is a reading: %+v", rows)
	}
	lines := strings.Join(runtimeLines(rows[0]), "\n")
	if !strings.Contains(lines, "no seat-guard entry is placed") || !strings.Contains(lines, "no gemini mode") {
		t.Errorf("the row says there is no mode for this runtime: %s", lines)
	}
}

// TestAConfigThatCannotBeReadIsALineAndNotAFailure: the owner's file is theirs, and doctor reports the read it
// could not make rather than refusing the whole report for it.
func TestAConfigThatCannotBeReadIsALineAndNotAFailure(t *testing.T) {
	rowsFromHooks(t, []clientguard.RuntimeHook{{Runtime: "claude", Bin: "/usr/bin/claude",
		Config: ".claude/settings.json", Mode: "--claude-hook", ReadError: "settings.json is not JSON"}})
	rows := runtimeRowsFor("")
	if rows[0].Row.State != clientservices.Running {
		t.Errorf("an unreadable config is not a refusal: %+v", rows[0].Row)
	}
	lines := strings.Join(runtimeLines(rows[0]), "\n")
	if !strings.Contains(lines, "settings.json is not JSON") {
		t.Errorf("the line carries the read error: %s", lines)
	}
}

// TestAHomeThatCannotBeResolvedIsOneRowWithItsReason: the rows cannot be read at all, and the report says that
// once instead of printing nothing.
func TestAHomeThatCannotBeResolvedIsOneRowWithItsReason(t *testing.T) {
	previous := runtimeHome
	runtimeHome = func() (string, error) { return "", errors.New("no registered home") }
	t.Cleanup(func() { runtimeHome = previous })
	rows := runtimeRowsFor("")
	if len(rows) != 1 || !strings.Contains(rows[0].Hook.ReadError, "no registered home") {
		t.Fatalf("an unreadable home is one row naming the reason: %+v", rows)
	}
	if lines := runtimeLines(rows[0]); len(lines) != 0 {
		t.Errorf("a runtime that was never found prints no lines: %v", lines)
	}
}
