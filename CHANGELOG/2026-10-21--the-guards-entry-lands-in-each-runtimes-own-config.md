# The guard's owner-wide half: `install` places it, `doctor` reports it, `uninstall` takes it out

The seat guard had two halves and only one was installed. `<seat dir>/hooks/pre/seat-guard.ts` is written when a
seat starts and an omp seat loads it - but a claude or agy session reads its OWN owner-wide file
(`~/.claude/settings.json`, `~/.gemini/config/hooks.json`) before any seat exists, and nothing in this client
wrote, reported or removed those entries: `install` placed the git hooks only, and `doctor` had no row that
could say a runtime's config carried no guard.

- `internal/clientguard/runtimes.go` is the table: agy (`.gemini/config/hooks.json`, `--agy-hook`), claude
  (`.claude/settings.json`, `--claude-hook`), codex (no surface). A runtime is detected by its binary on the
  PATH **or** by its own config directory, and only a detected runtime is written or reported - a machine
  without agy says nothing about agy. gemini was dropped from the table: this client never launches it, so its
  config is not this client's to edit.
- **Only a runtime whose guard has a mode gets an entry.** The shipped `seat-guard.ts` dispatches
  `--agy-hook` and nothing else yet (`--redact` and `--claude-hook` are the owner's pending asset patches); it has
  no `--claude-hook`, so `GUARD_HAS_CLAUDE_HOOK` is false and the claude
  row says so instead of writing an entry that would exit 0 answering nothing while the report called it
  guarded. Flipping that one constant after the asset gains the mode is the whole change.
- The entry is **guarded**: `if [ -n "$AGENTHUB_SEAT" ]; then exec node "$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts" <mode> <phase>; <otherwise> fi`.
  Outside a seat it runs nothing, so the owner's own window keeps working; agy's `otherwise` answers what the
  runtime itself expects (`{"decision":"ask"}` / `{}`). The agy entry is byte for byte the text
  `supervisor/mod.rs`'s `agy_guard_entry` writes, so the two writers of one file cannot flip an entry back and
  forth at every seat start.
- The write reads the file's top-level members **in document order** and puts back everything it did not own:
  another tool's hooks in agy's file, the owner's `env` and their own hook definitions in claude's, the
  indentation unit and the file mode. It writes a temporary file in the same directory and renames it over the
  original. An entry already present is left exactly as it is, so a second install moves not one byte; a file
  that is not a JSON object is refused by name and not overwritten.
- `guard install` and `guard uninstall` gained `--home` (default: the registered user's home). `uninstall` now
  takes the runtime entries out as well, and it does that **before** the git half: an entry is removed by the
  home it was written in, so a checkout that has since been deleted - exactly when the owner still needs the
  entry gone - cannot refuse the cleanup. Its line says what it DID (`the guard's entry is out of X`) rather
  than only what is left, because a removed row and a row where nothing was ever placed read the same
  otherwise.
- `clientlifecycle/runtimesrow.go` adds one `doctor` row per detected runtime - `guard_<runtime>` - carrying
  no room, because the file belongs to the machine. Each row prints where the binary was found and what the
  file says, naming the config by its **absolute** path; a home that cannot be resolved is one row whose line
  is the reason, so neither the human report nor `--json` shows a bare `running`. A missing entry is a **note,
  not a failure** (placing it is this install's job, and 7.5's refusal would fire on a machine install has not
  reached yet).

Verified on this machine with the built binary and a temporary home holding another tool's `herdr` entry:
`guard install --home <tmp>` wrote the agy entry with the guarded pre/post commands and kept `herdr` first
with the owner's indentation, a second run rewrote nothing, `guard uninstall --home <tmp>` took the entry out
and left `herdr`, and `uninstall` against a missing checkout cleaned the entry and then reported the git error.
`doctor 4genthub-min` printed `guard_agy`/`guard_claude`/`guard_codex` with their lines and exited 0.
`go test ./...` in `agenthub_client` is green (35 packages); `go test -count=1 ./internal/clientguard/
./internal/clientlifecycle/` passes.

## Gate fixes (a974ec10 review: M1-M4, m1-m3)

- **M1 - an unreadable config is a failed install.** `RuntimeInstallLine` carries the read error as the
  runtime's line, and `runInstall` prints every row and then exits 3 when a DETECTED runtime was skipped for
  that reason, because its guard is not in force. A 0-byte and a BOM-prefixed file are not that case: both are
  what a runtime leaves behind, so `configBody` answers them as "no members" and the entry lands
  (`TestAnUnreadableConfigRefusesTheInstall`, `TestABlankOrBomConfigTakesTheEntry`; `guard install` on a
  directory-shaped config prints `agy: … cannot be read` and exits 3).
- **M2 - `"hooks": null` no longer panics.** A null member decodes to a nil map without an error, so the writer
  resets it to an empty object; the removal answers "nothing to remove" for it.
- **M3 - the claude write/remove half has its cases**, driven as package functions so the false flag does not
  hide them: absent, `{}`, null, `{}` with owner definitions, a whole owner file; both phases plus
  `PostToolUseFailure` get exactly one guard definition, a second pass is byte-identical, removal keeps the
  owner's definitions and drops only the guard's, a non-array phase is refused by name
  (`TestTheClaudeWriteAndRemoveHalf`). The writer now registers `PostToolUseFailure` too - the asset's failure
  event, without which a settings file would never log a failed call.
- **M4 - the uninstall ordering has a case**: `runUninstall` against a checkout that does not exist removes the
  runtime entry and exits non-zero (`TestUninstallTakesTheRuntimeEntryOutBeforeTheDeletedCheckout`).
- **m1 - the report's own wiring is tested**: `runDoctor` with a stubbed `runtimeRowFor` prints
  `guard_agy: found at …` and the absolute config path on the human surface and in `--json` reasons
  (`TestTheDoctorReportCarriesTheRuntimeRows`, replacing the case that called the helper itself).
- **m2 - `newHealthFixture` stubs `runtimeHome`/`runtimeHooks`**, so no doctor case reads the operator's home
  or PATH any more.
- **m3 - a symlinked config stays a link**: `writeDocument` follows it, so a dotfile-managed file is written
  through rather than replaced (`TestASymlinkedConfigKeepsItsLink`; measured with `guard install`, the link
  intact and the managed target holding the entry).
- **m4 - the dated entry's claim is narrowed**: the shipped asset dispatches `--agy-hook` only; `--redact` and
  `--claude-hook` are the owner's pending patches.

`go test ./...` in `agenthub_client`: green.
