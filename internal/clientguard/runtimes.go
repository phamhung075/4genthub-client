// The OWNER-WIDE guard entries: one row per runtime CLI the client launches, and an entry in the runtime's own
// config file for the runtimes whose ONLY hook surface is that file.
//
// WHAT THIS DOES AND DOES NOT COVER - the words matter, because an entry that never answers must not be reported
// as guarded. The runtime hands a seat its guard through the seat's own file (`<seat dir>/hooks/pre/seat-guard.ts`
// for omp, `<seat dir>/settings.json` for claude, a `hooks.json` entry for agy). Those cover the seats the client
// LAUNCHES. This file covers the one runtime whose hook surface is global - agy reads `~/.gemini/config/hooks.json`
// - so on a machine where agy was installed but no agy seat has started since, the entry is the difference between
// a seat that is guarded and one that is not. It is NOT a net for the owner's own window and not a second opinion
// inside a claude seat: every entry is a no-op outside a seat (see below), so what it covers is the seats, and only
// the seats, of a runtime that has no per-seat file.
//
// EVERY ENTRY IS A NO-OP OUTSIDE A SEAT. Its command tests `AGENTHUB_SEAT` and otherwise answers what the runtime
// would answer with no hook (agy: its default "ask" before a tool and `{}` after one; claude: silence, which its
// contract reads as allow). So one owner-wide entry can never begin refusing the owner's own work in the owner's
// own window, and the guard runs only where `PI_CODING_AGENT_DIR` and the seat's log exist.
//
// AN ENTRY IS WRITTEN ONLY FOR A MODE THE SHIPPED GUARD ANSWERS. `assets/seat-guard.ts` dispatches `--agy-hook`
// and, until the owner's asset patch lands, no `--claude-hook` (rust/agenthub-runtime/src/supervisor/mod.rs keeps
// the same fact in `GUARD_HAS_CLAUDE_HOOK: bool`). A claude entry written before that flag flips is a hook that
// exits 0 with no output - claude reads silence as allow - reported by `doctor` as guarded. So the claude row is
// DETECTED, REPORTED and NOT WRITTEN while the mode is absent, and the constant below is the one place the two
// languages have to agree; a test reads the rust source and fails if they drift.
//
// A RUNTIME WITH NO HOOK SURFACE IS REPORTED, NEVER WRITTEN: codex is launched by this client with no hook
// mechanism at all, so its row says so rather than inventing a file.
package clientguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RuntimeHook is one runtime CLI on this machine, and what its owner-wide config says about the guard.
type RuntimeHook struct {
	// Runtime is the binary's own name: "agy", "claude" or "codex".
	Runtime string
	// Bin is what the PATH answered for the binary, "" when it is not on PATH.
	Bin string
	// Dir is the runtime's own config directory (under the real home), "" when it does not exist.
	Dir string
	// Config is the owner-wide file the guard's entry belongs in, "" when there is no surface to place one on.
	Config string
	// Mode is the guard argument the entry names: `--agy-hook` or `--claude-hook`; "" for no mode.
	Mode string
	// EntryPresent is whether Config already carries the guard's entry.
	EntryPresent bool
	// Note is the one thing a reader must know that the fields above cannot say: the entry is missing, there is
	// no surface to place it on, or the mode the entry needs is not in the guard this client ships. Never a
	// failure.
	Note string
	// ReadError is a config file this client could not read or parse: the owner's file, left alone, and the
	// reason the row cannot say whether the entry is there.
	ReadError string
}

// Found is whether this machine has the runtime at all: on the PATH, or with the config directory an earlier
// install left behind. Both halves are asked because either alone is a machine the guard should report on.
func (h RuntimeHook) Found() bool { return h.Bin != "" || h.Dir != "" }

// Guards reports whether this row's mode is a mode the shipped guard answers, so an entry written here would
// actually run something.
func (h RuntimeHook) Guards() bool { return h.Mode != "" && guardModeAvailable(h.Mode) }

// guardCarriesClaudeHook mirrors rust/agenthub-runtime/src/supervisor/mod.rs's `GUARD_HAS_CLAUDE_HOOK`. It is
// false because the shipped `assets/seat-guard.ts` dispatches no `--claude-hook`: an entry that names a mode the
// guard does not have is a hook that exits 0 in silence, which claude reads as allow. Flipping this line without
// the asset carrying the mode is the bug this constant exists to prevent, so flipping it is one commit that also
// flips the rust constant - and `TestTheClaudeGateMatchesTheRuntimesFlag` fails if only one side moves.
const guardCarriesClaudeHook = false

// guardModeAvailable is whether the guard this client SHIPS answers `mode`. The agy mode is in the asset; the
// claude mode arrives with the owner's asset patch, and the constant above is where the two halves meet.
func guardModeAvailable(mode string) bool {
	switch mode {
	case "--agy-hook":
		return true
	case "--claude-hook":
		return guardCarriesClaudeHook
	}
	return false
}

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
// guard's entry goes. `config` is "" when the runtime has no hook surface for this client to place anything on.
type runtimeSpec struct {
	runtime string
	binary  string
	dir     string // under the real home
	config  string // under the real home; "" when there is no surface
	mode    string
}

// runtimeSpecs is the table, in the order the rows are reported, and it is the runtimes this client LAUNCHES
// (rust/agenthub-runtime/src/types.rs, `AgentRuntime`): omp needs no row (its guard is the seat's own extension
// hook), and the gemini CLI - replaced by agy and never launched by this client - had a row until 2026-10-21,
// which was wrong twice: the client never starts it, and its config dir is agy's own home.
var runtimeSpecs = []runtimeSpec{
	{runtime: "agy", binary: "agy", dir: ".gemini/config", config: ".gemini/config/hooks.json", mode: "--agy-hook"},
	{runtime: "claude", binary: "claude", dir: ".claude", config: ".claude/settings.json", mode: "--claude-hook"},
	// codex has no hook surface this client can write: it is launched with no per-seat settings file and reads no
	// command hook, so its guard would have to be its own sandbox policy - not this file's business. The row
	// exists so a reader sees the runtime was CONSIDERED and is not silently unguarded.
	{runtime: "codex", binary: "codex", dir: ".codex", config: "", mode: ""},
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
			hook.Note = spec.runtime + ": found, and there is no hook surface for it to place a guard on - " +
				"this client starts it with no settings file, so nothing is written"
		case !hook.Guards():
			hook.Note = spec.runtime + ": found, and the seat guard this client ships has no " + spec.mode +
				" mode yet - an entry would exit 0 answering nothing, so none is written (the runtime's own" +
				" GUARD_HAS_CLAUDE_HOOK is false)"
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
// A runtime with no surface, or with a mode the shipped guard does not answer, is answered with its note and
// nothing written; a runtime this machine does not have is not in the answer at all. The error is a FILE this
// client cannot write or read as a JSON object, which is the only thing the caller has to act on - and the hooks
// written before it are returned with it, so a partial install is visible rather than silent.
func InstallRuntimeHooks(home string) ([]RuntimeHook, error) {
	hooks := RuntimeHooks(home)
	for i := range hooks {
		hook := &hooks[i]
		if hook.Config == "" || !hook.Guards() || hook.ReadError != "" {
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

// UninstallRuntimeHooks takes this client's entry back out of every runtime config that carries one, and answers
// the state after the removal. Without it, an entry `guard install` wrote could not be removed by any verb, which
// is how an abandoned hook outlives the checkout it was installed for. A file whose only member was ours is left
// as `{}` rather than deleted: the runtime created the file, and another tool may have written the key order.
func UninstallRuntimeHooks(home string) ([]RuntimeHook, error) {
	hooks := RuntimeHooks(home)
	for i := range hooks {
		hook := &hooks[i]
		if hook.Config == "" || !hook.Guards() || hook.ReadError != "" || !hook.EntryPresent {
			continue
		}
		if err := removeRuntimeEntry(home, *hook); err != nil {
			return hooks, err
		}
		hook.EntryPresent = false
	}
	return hooks, nil
}

// RuntimeInstallLine is one runtime's line after `guard install`: the file its entry is in, or the reason nothing
// was written there. One function, so the verb and its report cannot word the same fact differently.
func RuntimeInstallLine(hook RuntimeHook) string {
	if hook.Note != "" {
		return hook.Note
	}
	if !hook.EntryPresent {
		return hook.Runtime + ": no entry was placed"
	}
	return hook.Runtime + ": the guard's entry is in " + hook.Config
}

// RuntimeUninstallLine is one runtime's line after `guard uninstall`.
func RuntimeUninstallLine(hook RuntimeHook) string {
	if hook.ReadError != "" {
		return hook.Runtime + ": " + hook.ReadError
	}
	if hook.Config == "" {
		return hook.Runtime + ": nothing was installed, and there is no surface for one"
	}
	if hook.EntryPresent {
		return hook.Runtime + ": the guard's entry is still in " + hook.Config
	}
	return hook.Runtime + ": no guard entry is in " + hook.Config
}

// writeRuntimeEntry is the one write, per runtime: the file is read as its top-level members IN DOCUMENT ORDER,
// so the only thing that changes is the guard's own entry - the owner's key order, their indentation unit and
// every key this client does not own are carried through. The result is written to a temporary file in the same
// directory and renamed over the original, because a crash or a full disk mid-write must not truncate the
// owner's own env or hooks.
func writeRuntimeEntry(home string, hook RuntimeHook) error {
	path := filepath.Join(home, hook.Config)
	doc, err := readDocument(path)
	if err != nil {
		return err
	}
	entry, err := json.Marshal(guardEntryFor(hook.Runtime))
	if err != nil {
		return fmt.Errorf("cannot encode the %s guard entry: %v", hook.Runtime, err)
	}
	switch hook.Runtime {
	case "agy":
		doc.set("seat-guard", entry)
	case "claude":
		accounted, err := withClaudeHooks(doc.get("hooks"), entry)
		if err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		doc.set("hooks", accounted)
	default:
		return fmt.Errorf("no owner-wide entry is known for %s", hook.Runtime)
	}
	return writeDocument(path, doc)
}

// removeRuntimeEntry is the write's inverse: agy loses the whole `seat-guard` key, and claude loses every
// definition that names the guard's script - and nothing else.
func removeRuntimeEntry(home string, hook RuntimeHook) error {
	path := filepath.Join(home, hook.Config)
	doc, err := readDocument(path)
	if err != nil {
		return err
	}
	switch hook.Runtime {
	case "agy":
		doc.drop("seat-guard")
	case "claude":
		stripped, err := withoutClaudeHooks(doc.get("hooks"))
		if err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		doc.set("hooks", stripped)
	default:
		return fmt.Errorf("no owner-wide entry is known for %s", hook.Runtime)
	}
	return writeDocument(path, doc)
}

// guardEntryFor is the JSON value one runtime's entry has: agy's own key, claude's two hook definitions.
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
	entryMembers, err := decodeObject(entry)
	if err != nil {
		return nil, fmt.Errorf("the guard's own entry is not an object: %v", err)
	}
	hooks := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, errors.New("`hooks` is not a JSON object, and the guard was not installed")
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
			if carriesGuardText(definition) {
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

// withoutClaudeHooks is `withClaudeHooks`'s inverse: both phase arrays lose the definitions that name the guard's
// script, and everything the owner wrote stays exactly as it was. An absent `hooks` member stays absent.
func withoutClaudeHooks(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	hooks := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil, errors.New("`hooks` is not a JSON object, and the guard's entry was not removed")
	}
	for phase, existing := range hooks {
		var definitions []json.RawMessage
		if err := json.Unmarshal(existing, &definitions); err != nil {
			continue
		}
		kept := make([]json.RawMessage, 0, len(definitions))
		for _, definition := range definitions {
			if carriesGuardText(definition) {
				continue
			}
			kept = append(kept, definition)
		}
		if len(kept) == len(definitions) {
			continue
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return nil, fmt.Errorf("cannot encode `hooks.%s`: %v", phase, err)
		}
		hooks[phase] = encoded
	}
	return json.Marshal(hooks)
}

// carriesGuardText is whether `raw` names the guard's script - the ONE test both the writer's presence check and
// the uninstaller's filter use, so an entry the install refuses to add twice is an entry the uninstall can remove.
func carriesGuardText(raw json.RawMessage) bool {
	return strings.Contains(string(raw), GuardScriptName)
}

// GuardScriptName is the file the guard IS, as an entry names it. A hook command that does not contain this string
// did not come from this client.
const GuardScriptName = "seat-guard.ts"

// document is a config file as its top-level members, IN DOCUMENT ORDER, plus the indentation unit it used. Re
// ordering the owner's keys, or rewriting their file with this client's indentation, is a change this install has
// no business making: the file holds another tool's hooks and the owner's own env.
type document struct {
	members []documentMember
	indent  string
}

// documentMember is one top-level member, its value kept as the bytes that were read.
type documentMember struct {
	Key string
	Raw json.RawMessage
}

// readDocument reads a config file. A missing file is an empty document with this client's indentation (the
// runtime is installed and has never written one); anything that is not a JSON object is an error, because
// overwriting a file this client does not understand is how another tool's settings are lost.
func readDocument(path string) (document, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return document{indent: "  "}, nil
		}
		return document{}, fmt.Errorf("cannot read %s: %v", path, err)
	}
	members, err := decodeMembers(body)
	if err != nil {
		return document{}, fmt.Errorf("%s is not a JSON object, and the seat guard's entry was not installed", path)
	}
	return document{members: members, indent: indentUnit(body)}, nil
}

// decodeMembers parses the top-level members of a JSON object in order. It is a token walk rather than a map so the
// file can be written back with the order it was read in.
func decodeMembers(body []byte) ([]documentMember, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("not an object")
	}
	var members []documentMember
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, errors.New("a key that is not a string")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		members = append(members, documentMember{Key: name, Raw: raw})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return members, nil
}

// indentUnit is the file's own indentation: the leading whitespace of its first indented line, and two spaces for a
// file this client creates or a file written on one line.
func indentUnit(body []byte) string {
	for _, line := range bytes.Split(body, []byte("\n"))[1:] {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == len(line) || len(trimmed) == 0 {
			continue
		}
		return string(line[:len(line)-len(trimmed)])
	}
	return "  "
}

// get is one member's value, nil when the file has no such key.
func (d document) get(key string) json.RawMessage {
	for _, member := range d.members {
		if member.Key == key {
			return member.Raw
		}
	}
	return nil
}

// set replaces a member in place, or appends it when the file has no such key - which is where a new key goes in
// every JSON file this client has seen the runtimes write.
func (d *document) set(key string, raw json.RawMessage) {
	if raw == nil {
		d.drop(key)
		return
	}
	for i := range d.members {
		if d.members[i].Key == key {
			d.members[i].Raw = raw
			return
		}
	}
	d.members = append(d.members, documentMember{Key: key, Raw: raw})
}

// drop removes a member, leaving the order of the rest alone.
func (d *document) drop(key string) {
	kept := d.members[:0]
	for _, member := range d.members {
		if member.Key == key {
			continue
		}
		kept = append(kept, member)
	}
	d.members = kept
}

// encode renders the document: the members in their order, each value indented with the file's own unit.
func (d document) encode() ([]byte, error) {
	if len(d.members) == 0 {
		return []byte("{}\n"), nil
	}
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, member := range d.members {
		key, err := json.Marshal(member.Key)
		if err != nil {
			return nil, err
		}
		out.WriteString(d.indent)
		out.Write(key)
		out.WriteString(": ")
		var value bytes.Buffer
		// The prefix is the file's unit so a nested member lands one level deeper than its key, and the first line
		// needs none because the key and its colon are already indented.
		if err := json.Indent(&value, member.Raw, d.indent, d.indent); err != nil {
			return nil, err
		}
		out.Write(value.Bytes())
		if i < len(d.members)-1 {
			out.WriteString(",")
		}
		out.WriteString("\n")
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}

// writeDocument writes the file the runtime reads. THE OWNER'S FILE KEEPS ITS OWN MODE: `~/.claude/settings.json`
// is 0600 on a machine that has run claude with secrets in it, and a file this client creates is 0600 because it
// will hold a hook naming the seat's guard, not because this client widens anything. The write goes through a
// temporary file in the same directory and a rename, so a crash mid-write cannot leave the owner with half a file.
func writeDocument(path string, doc document) error {
	body, err := doc.encode()
	if err != nil {
		return fmt.Errorf("cannot encode %s: %v", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %v", dir, err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	temp, err := os.CreateTemp(dir, ".4genteam-guard-*.tmp")
	if err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if err := os.Chmod(tempName, mode); err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	return nil
}

// decodeObject is one raw JSON object as its members.
func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	if members == nil {
		return nil, errors.New("not an object")
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
// mode. It is a walk rather than a path per runtime because the files nest their entries differently (agy under
// its own key, claude under `hooks.<phase>[]`), and the question is the same one in all of them.
func carriesGuard(value any, mode string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, GuardScriptName) && strings.Contains(typed, mode)
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
