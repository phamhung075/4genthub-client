# The guard's entry lands in each runtime's own config, and `doctor` reports it

The seat guard had two halves and only one was installed. `<seat dir>/hooks/pre/seat-guard.ts` is written by
the runtime when it prepares a seat, and an omp seat loads it - but a claude or agy session reads its OWN
owner-wide file (`~/.claude/settings.json`, `~/.gemini/config/hooks.json`) before any seat exists, and nothing
in this client wrote or reported those entries: `install` placed the git hooks only, and `doctor` had no row
that could say a runtime's config carried no guard.

`guard install` now places them, and `doctor` reports them.

- `internal/clientguard/runtimes.go` is the table: claude (`.claude/settings.json`, `--claude-hook`), agy
  (`.gemini/config/hooks.json`, `--agy-hook`), gemini (no mode). A runtime is detected by its binary on the
  PATH **or** by its own config directory, and only a detected runtime is written or reported - a machine
  without claude says nothing about claude.
- The entry is **guarded**: `if [ -n "$AGENTHUB_SEAT" ]; then exec node "$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts" <mode> <phase>; <otherwise> fi`.
  Outside a seat it runs nothing, so the owner's own window keeps working; agy's `otherwise` answers what the
  runtime itself expects (`{"decision":"ask"}` / `{}`), and claude's is silence, which its contract reads as
  allow. The agy entry is byte for byte the text `supervisor/mod.rs`'s `agy_guard_entry` writes, so the two
  writers of one file cannot flip an entry back and forth at every seat start.
- The write reads the file as raw top-level members and puts back everything it did not own: another tool's
  hooks in agy's file, the owner's `env` and their own hook definitions in claude's, and the file's mode. An
  entry already present is left exactly as it is, so a second install writes nothing; a file that is not a JSON
  object is refused by name and not overwritten.
- `guard install` gained `--home` (default: the registered user's home). It is what makes the verb testable and
  is what a machine whose config sits elsewhere needs; without it the verb had no lever that is not the owner's
  real home.
- `clientlifecycle/runtimesrow.go` adds one `doctor` row per detected runtime - `guard_claude`, `guard_agy`,
  `guard_gemini` - carrying no room, because the file belongs to the machine. A missing entry is a **note, not
  a failure** (it is this install's job, and 7.5's refusal would fire on a machine install has not reached
  yet), and gemini's row says the guard has no mode for it rather than going quiet.

Verified on this machine: `go run ./cmd/4genteam guard install --checkout <tmp repo> --home <tmp home>` wrote
both entries with the guarded commands above and printed three lines (claude placed, agy placed, gemini no
mode); `go run ./cmd/4genteam doctor 4genthub-min` printed `guard_claude`, `guard_agy`, `guard_gemini` as
running, with `guard_claude` naming `~/.claude/settings.json` as missing the entry and `guard_agy` finding it
present, and the report exited 0. `go test ./...` in `agenthub_client` is green.
