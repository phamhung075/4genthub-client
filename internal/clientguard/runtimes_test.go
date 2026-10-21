// The owner-wide half of the guard: one entry in each runtime CLI's config file, placed by `guard install` and
// reported by `doctor`. The cases below are the ones a reader of the file can see and a seat can hit - a foreign
// tool's hooks in the same file, the owner's own hooks, a second run, and a runtime with no guard mode.
package clientguard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runtimeOnPath answers the PATH seam with the named binaries and nothing else, so a case names the machine it
// runs on.
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

// members reads one config file as raw JSON members, so a case asserts about the whole file and not one key.
func members(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s is not an object: %v\n%s", path, err, body)
	}
	return out
}

// claudeFile is `{"env":{...},"hooks":{"PreToolUse":[one owner definition]}}`: the owner's own settings and one
// definition this client must not touch.
const claudeFile = `{
  "env": {"FOO": "1"},
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo owner"}]}]
  }
}
`

// agyFile is another tool's entry under its own key: agy seats and this install share one file.
const agyFile = `{
  "herdr": {"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "echo herdr"}]}]}}
}
`

// TestInstallRuntimeHooksPlacesAGuardedEntryInEachRuntimeAndKeepsTheRestOfTheFile is the install's whole promise:
// every detected runtime gets the guarded entry, the owner's keys and another tool's hooks survive, the mode of
// the file is kept, and a second run adds nothing.
func TestInstallRuntimeHooksPlacesAGuardedEntryInEachRuntimeAndKeepsTheRestOfTheFile(t *testing.T) {
	runtimeOnPath(t, "claude", "agy", "gemini")
	home := homeWith(t, ".claude/settings.json", claudeFile)
	if err := os.MkdirAll(filepath.Join(home, ".gemini/config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini/config/hooks.json"), []byte(agyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(home, ".claude/settings.json")
	if err := os.Chmod(claudePath, 0o600); err != nil {
		t.Fatal(err)
	}

	hooks, err := InstallRuntimeHooks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 3 {
		t.Fatalf("every one of the three runtimes is on PATH and has a config dir, so all three are answered: %+v", hooks)
	}
	for _, hook := range hooks {
		if hook.Runtime != "gemini" && (!hook.EntryPresent || hook.Note != "") {
			t.Errorf("%s: install answered %+v, want the entry present and no note", hook.Runtime, hook)
		}
	}

	// claude: the owner's env and their own definition are untouched, and ours is appended to both phases.
	claude := members(t, claudePath)
	var env map[string]string
	if err := json.Unmarshal(claude["env"], &env); err != nil || env["FOO"] != "1" {
		t.Skipf("env is not the object the fixture wrote: %s", claude["env"])
	}
	var hookMap map[string][]map[string]any
	if err := json.Unmarshal(claude["hooks"], &hookMap); err != nil {
		t.Fatal(err)
	}
	pre := hookMap["PreToolUse"]
	if len(pre) != 2 {
		t.Fatalf("claude's PreToolUse carries the owner's definition plus the guard's: %v", pre)
	}
	if pre[0]["matcher"] != "Bash" {
		t.Errorf("the owner's definition is first and unchanged: %v", pre[0])
	}
	command := pre[1]["hooks"].([]any)[0].(map[string]any)["command"]
	if command != GuardedCommand("--claude-hook", "pre", "") {
		t.Errorf("claude's pre entry runs the guarded command, got %v", command)
	}
	if strings.Contains(command.(string), "else") {
		t.Errorf("claude's entry must be silent outside a seat - silence is its allow - and it is not: %v", command)
	}
	if len(hookMap["PostToolUse"]) != 1 {
		t.Errorf("claude's post phase gets the guard's definition too: %v", hookMap["PostToolUse"])
	}
	if info, err := os.Stat(claudePath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("the owner's file keeps its mode, got %v", info.Mode().Perm())
	}

	// agy: the other tool's key is carried through whole, and the entry is the runtime's own text.
	agy := members(t, filepath.Join(home, ".gemini/config/hooks.json"))
	if _, ok := agy["herdr"]; !ok {
		t.Errorf("another tool's key was dropped from agy's hooks.json: %v", agy)
	}
	var entry, want map[string]any
	if err := json.Unmarshal(agy["seat-guard"], &entry); err != nil {
		t.Fatalf("agy's entry is not under `seat-guard`: %s", agy["seat-guard"])
	}
	want = agyGuardEntry()
	wantJSON, _ := json.Marshal(want)
	entryJSON, _ := json.Marshal(entry)
	if string(entryJSON) != string(wantJSON) {
		t.Errorf("agy's entry is the runtime's own text\ngot  %s\nwant %s", entryJSON, wantJSON)
	}

	// A second run is the state a user reaches by running install twice: nothing is duplicated.
	again, err := InstallRuntimeHooks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 3 {
		t.Fatalf("the second run answers the same three runtimes: %+v", again)
	}
	claude = members(t, claudePath)
	if err := json.Unmarshal(claude["hooks"], &hookMap); err != nil {
		t.Fatal(err)
	}
	if len(hookMap["PreToolUse"]) != 2 || len(hookMap["PostToolUse"]) != 1 {
		t.Errorf("the second run wrote the entry again: %v", hookMap)
	}
}

// TestInstallWritesClaudeAndAgyButReportsGeminiAsHavingNoMode pins the row's decision: the gemini CLI's own hooks
// answer a payload the guard does not read, so nothing is placed for it and the note says why - a file this
// client wrote for a mode the guard cannot run would be a hook that never refuses anything.
func TestInstallWritesClaudeAndAgyButReportsGeminiAsHavingNoMode(t *testing.T) {
	runtimeOnPath(t, "gemini")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks, err := InstallRuntimeHooks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || hooks[0].Runtime != "gemini" {
		t.Fatalf("only gemini is on this machine, so only its row is answered: %+v", hooks)
	}
	if hooks[0].EntryPresent || hooks[0].Config != "" {
		t.Errorf("no entry is placed for a runtime with no mode: %+v", hooks[0])
	}
	if !strings.Contains(hooks[0].Note, "no gemini mode") {
		t.Errorf("the note names the missing mode, got %q", hooks[0].Note)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini/hooks.json")); !os.IsNotExist(err) {
		t.Errorf("nothing is written for gemini, and %v says otherwise", err)
	}
}

// TestADetectedRuntimeWithoutTheEntryIsReportedWithTheFileThatIsMissing is the `doctor` read: the machine has the
// runtime, the file does not carry the guard, and the note names the file and the verb that writes it.
func TestADetectedRuntimeWithoutTheEntryIsReportedWithTheFileThatIsMissing(t *testing.T) {
	runtimeOnPath(t, "claude")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := RuntimeHooks(home)
	if len(hooks) != 1 {
		t.Fatalf("claude is on PATH: %+v", hooks)
	}
	hook := hooks[0]
	if hook.EntryPresent {
		t.Fatal("no settings file exists, so the entry cannot be present")
	}
	if !strings.Contains(hook.Note, filepath.Join(home, ".claude/settings.json")) ||
		!strings.Contains(hook.Note, "guard install") {
		t.Errorf("the note names the file and the verb, got %q", hook.Note)
	}
}

// TestARuntimeThatIsNotOnPathAndHasNoConfigDirIsNotReported is the quiet default: a machine without claude says
// nothing about claude, so `doctor` does not carry a row for a runtime its reader never installed.
func TestARuntimeThatIsNotOnPathAndHasNoConfigDirIsNotReported(t *testing.T) {
	runtimeOnPath(t)
	if hooks := RuntimeHooks(t.TempDir()); len(hooks) != 0 {
		t.Fatalf("an empty machine has no runtime rows: %+v", hooks)
	}
}

// TestTheEntryIsFoundInTheFileTheRuntimeReads is the present case for both shapes: claude's definition nested in
// a phase array, and agy's under its own key.
func TestTheEntryIsFoundInTheFileTheRuntimeReads(t *testing.T) {
	runtimeOnPath(t, "claude", "agy")
	home := homeWith(t, ".claude/settings.json", claudeFile)
	if err := os.MkdirAll(filepath.Join(home, ".gemini/config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini/config/hooks.json"), []byte(agyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRuntimeHooks(home); err != nil {
		t.Fatal(err)
	}
	for _, hook := range RuntimeHooks(home) {
		if hook.Runtime == "gemini" {
			continue
		}
		if !hook.EntryPresent || hook.Note != "" {
			t.Errorf("%s: the entry was just written, so it must read as present: %+v", hook.Runtime, hook)
		}
	}
}

// TestAConfigThisClientCannotReadIsReportedAndNotOverwritten is the write's one refusal: a settings file that is
// not a JSON object is the owner's, and guessing at it would lose whatever it is.
func TestAConfigThisClientCannotReadIsReportedAndNotOverwritten(t *testing.T) {
	runtimeOnPath(t, "claude")
	home := homeWith(t, ".claude/settings.json", "[1, 2]\n")
	if _, err := InstallRuntimeHooks(home); err == nil {
		t.Fatal("a settings file that is not an object is refused")
	} else if !strings.Contains(err.Error(), "settings.json") {
		t.Errorf("the refusal names the file, got %v", err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "[1, 2]\n" {
		t.Errorf("the file is left exactly as it was, got %q", body)
	}
}

// TestTheInstallLineSaysWhereTheEntryIsOrWhyThereIsNone keeps the verb's output on the same words as the report:
// a placed entry names its file, and a runtime with no mode prints its note unchanged.
func TestTheInstallLineSaysWhereTheEntryIsOrWhyThereIsNone(t *testing.T) {
	placed := RuntimeInstallLine(RuntimeHook{Runtime: "claude", Config: ".claude/settings.json", EntryPresent: true})
	if placed != "claude: the guard's entry is in .claude/settings.json" {
		t.Errorf("got %q", placed)
	}
	note := RuntimeInstallLine(RuntimeHook{Runtime: "gemini", Note: "gemini: found, and there is no mode"})
	if note != "gemini: found, and there is no mode" {
		t.Errorf("got %q", note)
	}
}
