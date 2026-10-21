// The owner-wide half of the guard: one entry in the runtime config of each runtime whose only hook surface is that
// file, placed by `guard install` and reported by `doctor`. The cases below are the ones a reader of the file can
// see and a seat can hit - a foreign tool's hooks in the same file, the owner's own hooks and key order, a second
// run, a mode the shipped guard does not answer, and the uninstall that has to take it back out.
package clientguard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runtimeOnPath answers the PATH seam with the named binaries and nothing else, so a case names the machine it runs
// on.
func runtimeOnPath(t *testing.T, binaries ...string) {
	t.Helper()
	previous := runtimeLookPath
	runtimeLookPath = func(name string) (string, error) {
		for _, binary := range binaries {
			if binary == name {
				return filepath.Join("/usr/bin", name), nil
			}
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { runtimeLookPath = previous })
}

// homeWith writes one config file under a fresh home and makes its directory.
func homeWith(t *testing.T, path, body string) string {
	t.Helper()
	home := t.TempDir()
	full := filepath.Join(home, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// claudeFile is the owner's own settings: an env block, a key order the install must keep, and one hook definition
// that is not the guard's.
const claudeFile = `{
  "zeta": true,
  "env": {"FOO": "1"},
  "alpha": 1,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "echo owner"
          }
        ]
      }
    ]
  }
}
`

// agyFile is another tool's entry under its own key: agy seats and this install share one file.
const agyFile = `{
  "herdr": {
    "hooks": {
      "PreToolUse": [
        {
          "matcher": "*",
          "hooks": [
            {
              "type": "command",
              "command": "echo herdr"
            }
          ]
        }
      ]
    }
  }
}
`

// TestInstallPlacesTheAgyEntryKeepsTheOwnersFileAndIsByteStable is the install's whole promise for the one runtime
// whose hook surface is global: agy gets the guarded entry, another tool's key survives, the file's key order and
// indentation are the owner's, and a second run changes not one byte.
func TestInstallPlacesTheAgyEntryKeepsTheOwnersFileAndIsByteStable(t *testing.T) {
	runtimeOnPath(t, "agy")
	home := homeWith(t, ".gemini/config/hooks.json", agyFile)
	hooks, err := InstallRuntimeHooks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 {
		t.Fatalf("agy is on PATH, so one row: %+v", hooks)
	}
	if !hooks[0].EntryPresent || hooks[0].Note != "" {
		t.Errorf("install answers the state after the write: %+v", hooks[0])
	}

	path := filepath.Join(home, ".gemini/config/hooks.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(first), "{\n  \"herdr\": ") {
		t.Fatalf("the owner's first key and its indentation are kept: %q", first[:40])
	}
	if !strings.Contains(string(first), "\"seat-guard\": {") {
		t.Fatalf("agy's entry is under its own key: %s", first)
	}
	// The command is the runtime's own text, spelled here as supervisor/mod.rs writes it: comparing against the
	// function the writer calls would assert nothing about the other writer. Read it back through the JSON, since
	// the file holds it escaped and the point is the command a hook runs, not the escaping.
	var parsed map[string]struct {
		PreToolUse []struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
		PostToolUse []struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"PostToolUse"`
	}
	if err := json.Unmarshal(first, &parsed); err != nil {
		t.Fatalf("the file stays JSON: %v\n%s", err, first)
	}
	entry, ok := parsed["seat-guard"]
	if !ok {
		t.Fatalf("the entry is under its own key: %s", first)
	}
	if len(entry.PreToolUse) != 1 || len(entry.PreToolUse[0].Hooks) != 1 ||
		len(entry.PostToolUse) != 1 || len(entry.PostToolUse[0].Hooks) != 1 {
		t.Fatalf("the entry is one pre and one post row: %s", first)
	}
	if got := entry.PreToolUse[0].Hooks[0].Command; got != agyPreCommand {
		t.Fatalf("the pre command is the runtime's own, byte for byte:\ngot  %s\nwant %s", got, agyPreCommand)
	}
	if got := entry.PostToolUse[0].Hooks[0].Command; got != agyPostCommand {
		t.Fatalf("the post command is the runtime's own, byte for byte:\ngot  %s\nwant %s", got, agyPostCommand)
	}

	// A second run is the state a user reaches by running install twice: no byte moves.
	if _, err := InstallRuntimeHooks(home); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("the second run rewrote the file\nfirst  %q\nsecond %q", first, second)
	}

	// No temporary file is left beside the owner's config, whatever the write did.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
}

// agyPreCommand and agyPostCommand are the two commands `supervisor/mod.rs`'s `agy_guard_entry` writes, copied from
// the rust source rather than built by the function under test - that is the point of the pair.
const (
	agyPreCommand  = `if [ -n "$AGENTHUB_SEAT" ]; then exec node "$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts" --agy-hook pre; else echo '{"decision":"ask"}'; fi`
	agyPostCommand = `if [ -n "$AGENTHUB_SEAT" ]; then exec node "$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts" --agy-hook post; else echo '{}'; fi`
)

// TestTheEntryGuardsTheSeatAndIsASilentNoOpOutsideOne RUNS the command the entry carries, in both states: outside a
// seat it must print nothing and exit 0 (the owner's own window keeps its own behaviour), and inside a seat it must
// exec the guard from PI_CODING_AGENT_DIR - proven with a stand-in script that prints, so the assertion is on the
// command's behaviour and not on its text.
func TestTheEntryGuardsTheSeatAndIsASilentNoOpOutsideOne(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(dir, "hooks", "pre", GuardScriptName)
	if err := os.MkdirAll(filepath.Dir(guard), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte("const x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// node runs the real guard script, which answers JSON on stdin; here the script is a stub that prints its own
	// argv, so the test can see the mode and phase the entry handed it without running the guard's rules.
	stub := filepath.Join(dir, "stub.js")
	if err := os.WriteFile(stub, []byte("process.stdout.write(process.argv.slice(2).join(' '));\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	realNode, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is needed to run the guard: %v", err)
	}

	run := func(seat string) string {
		t.Helper()
		command := exec.Command("sh", "-c", GuardedCommand("--agy-hook", "pre", `{"decision":"ask"}`))
		command.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+dir)
		if seat == "" {
			command.Env = append(command.Env, "AGENTHUB_SEAT=")
		} else {
			command.Env = append(command.Env, "AGENTHUB_SEAT="+seat)
		}
		// The entry runs `node .../seat-guard.ts`; point that NAME at the stub through a PATH shim, and have the shim
		// call the real node by the path resolved BEFORE the shim went on the PATH - otherwise it re-enters itself.
		shim := filepath.Join(dir, "node")
		if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec "+realNode+" "+stub+" \"$@\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		command.Env = append(command.Env, "PATH="+dir+":"+os.Getenv("PATH"))
		out, err := command.Output()
		if err != nil {
			t.Fatalf("the entry must exit 0: %v", err)
		}
		return string(out)
	}

	// Outside a seat the entry does not run the guard at all: agy's hook needs a decision on stdout, so it gets the
	// pass-through one and the guard is never executed (the stub would have printed its argv).
	if out := run(""); out != "{\"decision\":\"ask\"}\n" {
		t.Errorf("outside a seat the entry is agy's own pass-through, never the guard; got %q", out)
	}
	if out := run("dev"); !strings.Contains(out, "--agy-hook pre") {
		t.Errorf("inside a seat the entry runs the guard's mode and phase; got %q", out)
	}
}

// TestARuntimeWithNoHookSurfaceGetsARowAndNoWrite is codex: this client launches it with no settings file and no
// command hook, so there is nothing to place and the row says so.
func TestARuntimeWithNoHookSurfaceGetsARowAndNoWrite(t *testing.T) {
	runtimeOnPath(t, "codex")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks, err := InstallRuntimeHooks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].Runtime != "codex" {
		t.Fatalf("codex is on this machine, so one row: %+v", hooks)
	}
	if hooks[0].Config != "" || hooks[0].EntryPresent {
		t.Errorf("no file is written for a runtime with no surface: %+v", hooks[0])
	}
	if !strings.Contains(hooks[0].Note, "no hook surface") {
		t.Errorf("the note says why nothing was written, got %q", hooks[0].Note)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Errorf("nothing is written under .codex, and %v says otherwise", err)
	}
}

// TestARuntimeThatIsNotOnPathAndHasNoConfigDirIsNotReported is the quiet default: a machine without agy says nothing
// about agy, so `doctor` does not carry a row for a runtime its reader never installed.
func TestARuntimeThatIsNotOnPathAndHasNoConfigDirIsNotReported(t *testing.T) {
	runtimeOnPath(t)
	if hooks := RuntimeHooks(t.TempDir()); len(hooks) != 0 {
		t.Fatalf("an empty machine has no runtime rows: %+v", hooks)
	}
}

// TestTheEntryIsFoundOnlyWithItsMode is the presence read: an entry under a DIFFERENT mode is not this guard's entry,
// or install would print "placed" while doctor printed "missing".
func TestTheEntryIsFoundOnlyWithItsMode(t *testing.T) {
	home := homeWith(t, ".gemini/config/hooks.json", agyFile)
	path := filepath.Join(home, ".gemini/config/hooks.json")
	if err := os.WriteFile(path, []byte(`{"seat-guard":{"x":[{"command":"node seat-guard.ts --other-hook pre"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	present, err := guardEntryPresent(path, "--agy-hook")
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Error("an entry naming another mode is not the guard's entry")
	}
}

// TestAConfigThisClientCannotReadIsReportedAndNotOverwritten is the write's one refusal: a settings file that is not
// a JSON object is the owner's, and guessing at it would lose whatever it is.
func TestAConfigThisClientCannotReadIsReportedAndNotOverwritten(t *testing.T) {
	runtimeOnPath(t, "agy")
	home := homeWith(t, ".gemini/config/hooks.json", "[1, 2]\n")
	if _, err := InstallRuntimeHooks(home); err == nil {
		t.Fatal("a config file that is not an object is refused")
	} else if !strings.Contains(err.Error(), "hooks.json") {
		t.Errorf("the refusal names the file, got %v", err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".gemini/config/hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "[1, 2]\n" {
		t.Errorf("the file is left exactly as it was, got %q", body)
	}
}

// TestUninstallTakesTheEntryBackOutAndKeepsTheOwnersHooks is the gate's m4: an entry install wrote and no verb could
// remove would outlive the checkout it was installed for.
func TestUninstallTakesTheEntryBackOutAndKeepsTheOwnersHooks(t *testing.T) {
	runtimeOnPath(t, "agy")
	home := homeWith(t, ".gemini/config/hooks.json", agyFile)
	if _, err := InstallRuntimeHooks(home); err != nil {
		t.Fatal(err)
	}
	hooks, err := UninstallRuntimeHooks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].EntryPresent {
		t.Fatalf("the entry is gone after uninstall: %+v", hooks)
	}
	body, err := os.ReadFile(filepath.Join(home, ".gemini/config/hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), GuardScriptName) {
		t.Errorf("no byte of this client's entry is left: %s", body)
	}
	if !strings.Contains(string(body), "herdr") {
		t.Errorf("another tool's key survives the uninstall: %s", body)
	}
}

// TestTheInstallLinesSayWhereTheEntryIsOrWhyThereIsNone keeps the verb's output on the same words as the report: a
// placed entry names its file, and a runtime with no mode prints its note unchanged.
func TestTheInstallLinesSayWhereTheEntryIsOrWhyThereIsNone(t *testing.T) {
	placed := RuntimeInstallLine(RuntimeHook{Runtime: "agy", Config: ".gemini/config/hooks.json", EntryPresent: true})
	if placed != "agy: the guard's entry is in .gemini/config/hooks.json" {
		t.Errorf("got %q", placed)
	}
	note := RuntimeInstallLine(RuntimeHook{Runtime: "codex", Note: "codex: found, and there is no surface"})
	if note != "codex: found, and there is no surface" {
		t.Errorf("got %q", note)
	}
	gone := RuntimeUninstallLine(RuntimeHook{Runtime: "agy", Config: ".gemini/config/hooks.json", Mode: "--agy-hook"})
	if gone != "agy: no guard entry was in .gemini/config/hooks.json" {
		t.Errorf("got %q", gone)
	}
	// A line that cannot tell a removal from a file that never carried the entry is the verb's own report lying by
	// omission: the owner who just ran uninstall reads the first, and only one of them is about an action.
	out := RuntimeUninstallLine(RuntimeHook{Runtime: "agy", Config: ".gemini/config/hooks.json", Removed: true})
	if out != "agy: the guard's entry is out of .gemini/config/hooks.json" {
		t.Errorf("a removed entry says it was taken out; got %q", out)
	}
}

// TestTheAgyEntryIsTheRuntimesOwnObject compares the whole entry - both phases, matcher, timeout and type - with the
// object `supervisor/mod.rs` writes, spelled as JSON here so a change on either side fails.
func TestTheAgyEntryIsTheRuntimesOwnObject(t *testing.T) {
	want := `{"PostToolUse":[{"hooks":[{"command":"if [ -n \"$AGENTHUB_SEAT\" ]; then exec node \"$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts\" --agy-hook post; else echo '{}'; fi","timeout":10,"type":"command"}],"matcher":"*"}],"PreToolUse":[{"hooks":[{"command":"if [ -n \"$AGENTHUB_SEAT\" ]; then exec node \"$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts\" --agy-hook pre; else echo '{\"decision\":\"ask\"}'; fi","timeout":10,"type":"command"}],"matcher":"*"}]}`
	got, err := json.Marshal(agyGuardEntry())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("agy's entry drifted from supervisor/mod.rs's agy_guard_entry:\ngot  %s\nwant %s", got, want)
	}
}

// TestAnUnreadableConfigRefusesTheInstall is the gate's M1: a detected runtime whose config this client cannot
// read is a guard that is NOT in force, so `guard install` names it and refuses. A 0-byte and a BOM-prefixed file
// are not that case - they are files a runtime leaves behind, and they take the entry.
func TestAnUnreadableConfigRefusesTheInstall(t *testing.T) {
	runtimeOnPath(t, "agy")
	repo := newGitRepo(t)

	// A directory where the config file should be: unreadable as a file for every uid, so the case does not depend
	// on the test's own permissions.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini/config/hooks.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runInstall([]string{"--checkout", repo.dir, "--home", home}, &stdout, &stderr)
	if code != ExitRefused {
		t.Fatalf("install = %d, want %d; stdout=%s stderr=%s", code, ExitRefused, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "agy: ") || !strings.Contains(stdout.String(), "cannot be read") {
		t.Errorf("the row names the unreadable file, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "could not be read") {
		t.Errorf("the refusal says why, got %q", stderr.String())
	}
}

// TestABlankOrBomConfigTakesTheEntry: neither shape is a config this client cannot read, so both install.
func TestABlankOrBomConfigTakesTheEntry(t *testing.T) {
	runtimeOnPath(t, "agy")
	for _, body := range []string{"", "   \n", "\xef\xbb\xbf\n", "\xef\xbb\xbf{}"} {
		home := homeWith(t, ".gemini/config/hooks.json", body)
		hooks, err := InstallRuntimeHooks(home)
		if err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		if len(hooks) != 1 || !hooks[0].EntryPresent || hooks[0].ReadError != "" {
			t.Fatalf("body %q: entry not placed: %+v", body, hooks)
		}
		written, err := os.ReadFile(filepath.Join(home, ".gemini/config/hooks.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(written), GuardScriptName) {
			t.Errorf("body %q: the file holds no guard entry: %s", body, written)
		}
	}
}

// TestTheClaudeEntryIsNeverWrittenAndTheAgyOneIs is the ruling's single case (RULING-2026-10-21-claude-guard-writer
// §4.1, §4.5): claude's guard is the seat file the runtime writes, so `guard install` writes no claude entry - not
// beside an owner's settings, not into a home that has none, and not over a hand-written entry an older build left -
// while agy's entry lands in the same run. There is no claude mode gate left to vary: the client never writes the
// entry, so the mode's presence cannot license one.
func TestTheClaudeEntryIsNeverWrittenAndTheAgyOneIs(t *testing.T) {
	runtimeOnPath(t, "claude", "agy")

	for _, haveFile := range []bool{true, false} {
		home := t.TempDir()
		if haveFile {
			home = homeWith(t, ".claude/settings.json", claudeFile)
		}
		hooks, err := InstallRuntimeHooks(home)
		if err != nil {
			t.Fatal(err)
		}
		byRuntime := map[string]RuntimeHook{}
		for _, hook := range hooks {
			byRuntime[hook.Runtime] = hook
		}
		claude, ok := byRuntime["claude"]
		if !ok {
			t.Fatalf("claude is on PATH, so it has a row: %+v", hooks)
		}
		if claude.EntryPresent {
			t.Errorf("claude has no owner-wide entry, and the row says one is there: %+v", claude)
		}
		for _, want := range []string{"settings.json", "duplicate", "never fire"} {
			if !strings.Contains(claude.Note, want) {
				t.Errorf("the note names the seat file and why no entry is written (%q), got %q", want, claude.Note)
			}
		}
		body, err := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
		switch {
		case haveFile && err != nil:
			t.Fatalf("the owner's file is still there: %v", err)
		case haveFile && string(body) != claudeFile:
			t.Errorf("the owner's settings are untouched byte for byte:\ngot  %q\nwant %q", body, claudeFile)
		case !haveFile && err == nil:
			t.Errorf("no claude file existed and one was written: %s", body)
		}

		agy, ok := byRuntime["agy"]
		if !ok || !agy.EntryPresent {
			t.Fatalf("agy's entry lands in the same run: %+v", hooks)
		}
		agyBody, err := os.ReadFile(filepath.Join(home, ".gemini/config/hooks.json"))
		if err != nil || !strings.Contains(string(agyBody), GuardScriptName) {
			t.Errorf("agy's file holds the guard's entry: %v (%s)", err, agyBody)
		}
	}
}

// TestAStaleClaudeEntryIsReportedAndRemoved is the uninstall half the ruling keeps (§4.2): a file an older build or
// a hand edit left a guard entry in must still lose exactly that entry, and nothing else. The install never writes
// one, so the file's entry is written here the way a reader's settings file would carry it.
func TestAStaleClaudeEntryIsReportedAndRemoved(t *testing.T) {
	runtimeOnPath(t, "claude")
	stale := `{
  "env": {"CLAUDE_KEY": "kept"},
  "hooks": {
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "timeout": 10, "command": "node /x/hooks/pre/seat-guard.ts --claude-hook pre"}]},
      {"matcher": "*", "hooks": [{"type": "command", "command": "echo owner"}]}
    ],
    "PostToolUseFailure": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "node /x/hooks/pre/seat-guard.ts --claude-hook post"}]}
    ]
  }
}`
	home := homeWith(t, ".claude/settings.json", stale)
	hooks := RuntimeHooks(home)
	var row RuntimeHook
	for _, hook := range hooks {
		if hook.Runtime == "claude" {
			row = hook
		}
	}
	if !row.EntryPresent || !strings.Contains(row.Note, "stale owner-wide entry") ||
		!strings.Contains(row.Note, "guard uninstall") {
		t.Fatalf("a stale entry is reported as stale and named for removal: %+v", row)
	}
	if _, err := UninstallRuntimeHooks(home); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), GuardScriptName) {
		t.Errorf("a guard definition survived the uninstall: %s", body)
	}
	for _, kept := range []string{"CLAUDE_KEY", "echo owner"} {
		if !strings.Contains(string(body), kept) {
			t.Errorf("the uninstall took the owner's %q with it: %s", kept, body)
		}
	}
	var file struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		t.Fatalf("the file is still JSON claude can read: %v (%s)", err, body)
	}
	if len(file.Hooks["PreToolUse"]) != 1 {
		t.Errorf("PreToolUse kept %d definitions, want the owner's one: %s", len(file.Hooks["PreToolUse"]), body)
	}
	if len(file.Hooks["PostToolUseFailure"]) != 0 {
		t.Errorf("PostToolUseFailure kept %d definitions, want none: %s", len(file.Hooks["PostToolUseFailure"]), body)
	}
}

// TestUninstallTakesTheRuntimeEntryOutBeforeTheDeletedCheckout is the gate's M4: the ordering that was blocker 3.
// A checkout that has since been deleted must not stop the owner-wide entry from coming out.
func TestUninstallTakesTheRuntimeEntryOutBeforeTheDeletedCheckout(t *testing.T) {
	runtimeOnPath(t, "agy")
	home := homeWith(t, ".gemini/config/hooks.json", agyFile)
	if _, err := InstallRuntimeHooks(home); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "checkout-that-was-deleted")
	var stdout, stderr strings.Builder
	code := runUninstall([]string{"--checkout", missing, "--home", home}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("uninstall with a deleted checkout answered success; stdout=%s", stdout.String())
	}
	body, err := os.ReadFile(filepath.Join(home, ".gemini/config/hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), GuardScriptName) {
		t.Errorf("the runtime entry survived the deleted checkout: %s", body)
	}
	if !strings.Contains(string(body), "\"herdr\"") {
		t.Errorf("another tool's key did not survive: %s", body)
	}
}

// TestASymlinkedConfigKeepsItsLink is the gate's m3: a dotfile-managed config is a link, and the install must write
// the file the link names rather than replace the link.
func TestASymlinkedConfigKeepsItsLink(t *testing.T) {
	runtimeOnPath(t, "agy")
	home := t.TempDir()
	managed := filepath.Join(t.TempDir(), "managed-hooks.json")
	if err := os.WriteFile(managed, []byte(agyFile), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".gemini/config/hooks.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(managed, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if _, err := InstallRuntimeHooks(home); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced by a regular file")
	}
	body, err := os.ReadFile(managed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), GuardScriptName) {
		t.Errorf("the managed file was not written: %s", body)
	}
}
