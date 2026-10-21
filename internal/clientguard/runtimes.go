// The OWNER-WIDE guard hooks: one entry per runtime CLI this machine has, in the file that runtime reads
// before a seat exists at all.
//
// WHY THIS IS NOT THE SEAT'S OWN HOOK. The runtime writes `<seat dir>/hooks/pre/seat-guard.ts` when it prepares
// a seat, and an omp seat loads it from its agent dir; an agy seat additionally gets a global `hooks.json`
// entry at start, and a claude seat a per-seat settings file. Those cover the seats the client LAUNCHES, and
// nothing else: a session the owner starts by hand in an agy or claude window runs with no guard at all, and
// the entry that would cover it is only ever written by a seat start. This file closes that gap from the one
// side a machine can be inspected from: the runtime's own config file.
//
// EVERY ENTRY IS A NO-OP OUTSIDE A SEAT. Its command tests `AGENTHUB_SEAT` and otherwise answers what the
// runtime would answer with no hook (agy: its default "ask" before a tool and `{}` after one; claude: silence,
// which its contract reads as allow). So one owner-wide entry can never begin refusing the owner's own work in
// the owner's own window, and the guard runs only where `PI_CODING_AGENT_DIR` and the seat's log exist.
//
// A RUNTIME THE GUARD HAS NO MODE FOR IS REPORTED, NEVER WRITTEN. The guard answers three protocols today:
// omp's extension hook, agy's `--agy-hook`, and claude's `--claude-hook`. Gemini CLI reads BeforeTool/AfterTool
// hooks from `~/.gemini/settings.json` and a payload the guard has no mode for; an entry there would be a hook
// that answers nothing, which reads as "guarded" in a report and is not. Its row says so instead.
package clientguard

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RuntimeHook is one runtime CLI on this machine, and what its owner-wide config says about the guard.
type RuntimeHook struct {
	// Runtime is the binary's own name: "agy", "claude" or "gemini".
	Runtime string
	// Bin is what the PATH answered for the binary, "" when it is not on PATH.
	Bin string
	// Dir is the runtime's own config directory (under the real home), "" when it does not exist.
	Dir string
	// Config is the owner-wide file the guard's entry belongs in, "" when the guard has no mode for this
	// runtime and there is nothing to place.
	Config string
	// Mode is the guard argument the entry names: `--agy-hook` or `--claude-hook`; "" for no mode.
	Mode string
	// EntryPresent is whether Config already carries the guard's entry.
	EntryPresent bool
	// Note is the one thing a reader must know that the fields above cannot say: the entry is missing, or
	// the guard has no mode for this runtime. Never a failure.
	Note string
	// ReadError is a config file this client could not read or parse: the owner's file, left alone, and the
	// reason the row cannot say whether the entry is there.
	ReadError string
}

// Found is whether this machine has the runtime at all: on the PATH, or with the config directory an earlier
// install left behind. Both halves are asked because either alone is a machine the guard should report on.
func (h RuntimeHook) Found() bool { return h.Bin != "" || h.Dir != "" }

// GuardedCommand is the command an owner-wide entry runs: the seat's own guard when the session is a seat, and
// the runtime's own default answer otherwise. `otherwise` is what the runtime needs outside a seat - agy's
// answers, or "" for a runtime whose contract reads silence as allow.
func GuardedCommand(mode, phase, otherwise string) string {
	run := fmt.Sprintf(`exec node "$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts" %s %s`, mode, phase)
	if otherwise == "" {
		return fmt.Sprintf(`if [ -n "$AGENTHUB_SEAT" ]; then %s; fi`, run)
	}
	return fmt.Sprintf(`if [ -n "$AGENTHUB_SEAT" ]; then %s; else echo '%s'; fi`, run, otherwise)
}

// agyGuardEntry is the entry the runtime itself writes at an agy seat start (supervisor/mod.rs,
// `agy_guard_entry`), reproduced byte for byte: two writers of one file must not disagree about the text, or an
// entry written here would be rewritten at the next agy start and back again.
func agyGuardEntry() map[string]any {
	definition := func(phase, otherwise string) []any {
		return []any{map[string]any{
			"matcher": "*",
			"hooks": []any{map[string]any{
				"type": "command", "timeout": 10, "command": GuardedCommand("--agy-hook", phase, otherwise),
			}},
		}}
	}
	return map[string]any{
		"PreToolUse":  definition("pre", `{"decision":"ask"}`),
		"PostToolUse": definition("post", "{}"),
	}
}

// claudeGuardDefinition is the definition an owner-wide claude settings file carries for one phase. Inside a
// seat the runtime writes the seat's own file (`claude_guard_settings`), which is why this one is guarded: the
// owner's own claude window is not a seat and must keep working as it did.
func claudeGuardDefinition(phase string) map[string]any {
	return map[string]any{
		"matcher": "*",
		"hooks": []any{map[string]any{
			"type": "command", "timeout": 10, "command": GuardedCommand("--claude-hook", phase, ""),
		}},
	}
}

// runtimeSpec is one row of the table: what to look for, where the runtime keeps its config, and where the
// guard's entry goes.
type runtimeSpec struct {
	runtime string
	binary  string
	dir     string // under the real home
	config  string // under the real home; "" when the guard has no mode for the runtime
	mode    string
}

// runtimeSpecs is the table, in the order the rows are reported. `dir` is the runtime's own config directory:
// agy's is `~/.gemini/config` and the gemini CLI's is `~/.gemini`, so a machine with only agy still names
// gemini - which is right, because the gemini CLI's own settings file is what a gemini session reads, and the
// row then says the guard has no mode for it rather than going silent.
var runtimeSpecs = []runtimeSpec{
	{runtime: "claude", binary: "claude", dir: ".claude", config: ".claude/settings.json", mode: "--claude-hook"},
	{runtime: "agy", binary: "agy", dir: ".gemini/config", config: ".gemini/config/hooks.json", mode: "--agy-hook"},
	{runtime: "gemini", binary: "gemini", dir: ".gemini", config: "", mode: ""},
}

// runtimeLookPath is the PATH read as a seam: a case drives every outcome without a PATH, exactly as the hooks
// row does with git.
var runtimeLookPath = exec.LookPath

// RuntimeHooks is the read-only report: every runtime this machine has, and whether the guard's entry is in its
// owner-wide config. It writes nothing, which is what `doctor` needs; `InstallRuntimeHooks` is the writing side
// and answers the same shape.
func RuntimeHooks(home string) []RuntimeHook {
	out := make([]RuntimeHook, 0, len(runtimeSpecs))
	for _, spec := range runtimeSpecs {
		hook := RuntimeHook{Runtime: spec.runtime, Dir: existingDir(home, spec.dir), Config: spec.config, Mode: spec.mode}
		if path, err := runtimeLookPath(spec.binary); err == nil {
			hook.Bin = path
		}
		if !hook.Found() {
			continue
		}
		switch {
		case spec.config == "":
			hook.Note = spec.runtime + ": found, and the seat guard has no " + spec.runtime +
				" mode - its hooks answer a payload the guard does not read, so no owner-wide entry is placed"
		default:
			path := filepath.Join(home, spec.config)
			present, err := guardEntryPresent(path, spec.mode)
			if err != nil {
				hook.ReadError = err.Error()
				break
			}
			hook.EntryPresent = present
			if !present {
				hook.Note = spec.runtime + ": found, and the guard's entry is not in " + path +
					" - `4genteam guard install` writes it"
			}
		}
		out = append(out, hook)
	}
	return out
}

// InstallRuntimeHooks writes the guard's entry into every detected runtime's owner-wide config, and answers the
// state after the write. It is idempotent: an entry that is already there is left exactly as it is, and no key,
// entry or byte outside the guard's own entry is removed - the agy file also holds other tools' hooks, and the
// claude file holds the owner's own settings.
//
// A runtime the guard has no mode for is answered with its note and nothing written; a runtime this machine does
// not have is not in the answer at all. The error is a FILE this client cannot write or read as a JSON object,
// which is the only thing the caller has to act on.
func InstallRuntimeHooks(home string) ([]RuntimeHook, error) {
	hooks := RuntimeHooks(home)
	for i := range hooks {
		hook := &hooks[i]
		if hook.Config == "" {
			continue
		}
		if err := writeRuntimeEntry(home, *hook); err != nil {
			return hooks, err
		}
		hook.EntryPresent = true
		hook.Note = ""
	}
	return hooks, nil
}

// RuntimeInstallLine is one runtime's line after `guard install`: the file its entry is in, or the reason
// nothing was written there. One function, so the verb and its report cannot word the same fact differently.
func RuntimeInstallLine(hook RuntimeHook) string {
	if hook.Note != "" {
		return hook.Note
	}
	if !hook.EntryPresent {
		return hook.Runtime + ": no entry was placed"
	}
	return hook.Runtime + ": the guard's entry is in " + hook.Config
}

// writeRuntimeEntry is the one write, per runtime: the whole file is read as raw JSON members, so every key this
// client does not own is carried through untouched.
func writeRuntimeEntry(home string, hook RuntimeHook) error {
	path := filepath.Join(home, hook.Config)
	members, err := readObject(path)
	if err != nil {
		return err
	}
	entry, err := json.Marshal(guardEntryFor(hook.Runtime))
	if err != nil {
		return fmt.Errorf("cannot encode the %s guard entry: %v", hook.Runtime, err)
	}
	switch hook.Runtime {
	case "agy":
		members["seat-guard"] = entry
	case "claude":
		accounted, err := withClaudeHooks(members["hooks"], entry)
		if err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		members["hooks"] = accounted
	default:
		return fmt.Errorf("no owner-wide entry is known for %s", hook.Runtime)
	}
	text, err := json.MarshalIndent(members, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %v", filepath.Dir(path), err)
	}
	// THE OWNER'S FILE KEEPS ITS OWN MODE. `~/.claude/settings.json` is 0600 on a machine that has run claude
	// with secrets in it, and a file this client creates is 0600 because it will hold a hook that names the
	// seat's guard, not because this client widens anything.
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(path, append(text, '\n'), mode); err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	return nil
}

// guardEntryFor is the JSON value one runtime's entry has: agy's own key, claude's two hook arrays.
func guardEntryFor(runtime string) any {
	switch runtime {
	case "agy":
		return agyGuardEntry()
	case "claude":
		// One DEFINITION per phase: `withClaudeHooks` appends it to that phase's array, so an array here would
		// nest one inside the other and claude would read a definition it cannot parse.
		return map[string]any{
			"PreToolUse":  claudeGuardDefinition("pre"),
			"PostToolUse": claudeGuardDefinition("post"),
		}
	}
	return nil
}

// withClaudeHooks returns the `hooks` member with the guard's definition present in both phase arrays. Every
// other member of `hooks`, and every other definition in the two arrays, is carried through as raw JSON: the
// owner's own hooks are not this client's to reformat or to drop.
func withClaudeHooks(raw json.RawMessage, entry json.RawMessage) (json.RawMessage, error) {
	var entryMembers map[string]json.RawMessage
	if err := json.Unmarshal(entry, &entryMembers); err != nil {
		return nil, fmt.Errorf("the guard's own entry is not an object: %v", err)
	}
	hooks := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, fmt.Errorf("`hooks` is not a JSON object, and the guard was not installed")
		}
	}
	for _, phase := range []string{"PreToolUse", "PostToolUse"} {
		var definitions []json.RawMessage
		if existing := hooks[phase]; len(existing) > 0 {
			if err := json.Unmarshal(existing, &definitions); err != nil {
				return nil, fmt.Errorf("`hooks.%s` is not a JSON array, and the guard was not installed", phase)
			}
		}
		present := false
		for _, definition := range definitions {
			if strings.Contains(string(definition), "seat-guard.ts") {
				present = true
				break
			}
		}
		if !present {
			definitions = append(definitions, entryMembers[phase])
		}
		encoded, err := json.Marshal(definitions)
		if err != nil {
			return nil, fmt.Errorf("cannot encode `hooks.%s`: %v", phase, err)
		}
		hooks[phase] = encoded
	}
	return json.Marshal(hooks)
}

// readObject is a config file as its top-level members. A missing file is an empty object (the runtime is
// installed and has never written one); anything that is not a JSON object is an error, because overwriting a
// file this client does not understand is how another tool's settings are lost.
func readObject(path string) (map[string]json.RawMessage, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil || members == nil {
		return nil, fmt.Errorf("%s is not a JSON object, and the seat guard's entry was not installed", path)
	}
	return members, nil
}

// guardEntryPresent reads one config file and answers whether any of its strings names the seat guard's script
// with this runtime's mode. A file that is missing holds no entry; a file that cannot be read as JSON is a note
// rather than a failure, like every other unreadable input in the report.
func guardEntryPresent(path, mode string) (bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("%s cannot be read: %v", path, err)
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return false, fmt.Errorf("%s is not JSON: %v", path, err)
	}
	return carriesGuard(value, mode), nil
}

// carriesGuard walks a decoded config for a command string that names the guard's script and this runtime's
// mode. It is a walk rather than a path per runtime because the three files nest their entries differently
// (agy under its own key, claude under `hooks.<phase>[]`), and the question is the same one in all of them.
func carriesGuard(value any, mode string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, "seat-guard.ts") && strings.Contains(typed, mode)
	case []any:
		for _, item := range typed {
			if carriesGuard(item, mode) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if carriesGuard(item, mode) {
				return true
			}
		}
	}
	return false
}

// existingDir is the runtime's config directory when this machine has one, "" otherwise.
func existingDir(home, dir string) string {
	if dir == "" {
		return ""
	}
	info, err := os.Stat(filepath.Join(home, dir))
	if err != nil || !info.IsDir() {
		return ""
	}
	return filepath.Join(home, dir)
}
