# A claude seat is handed the guard's settings file

## The defect

`claude-code` seats ran with **no seat guard at all**. The runtime writes the guard to
`<seat dir>/hooks/pre/seat-guard.ts` for every seat, and omp loads it because the seat dir is its agent dir; agy
runs it through its global `hooks.json`. claude reads neither: it runs a hook only when a settings file names it,
and `runner::child_argv` passed none — so every claude seat (the reviewer, among others) took its tool calls
unchecked, `bypassPermissions` or not.

## What lands

- `<seat dir>/settings.json`, written by the supervisor in the same window as `mcp.json` and the guard itself, for
  a claude-code seat only: `PreToolUse` and `PostToolUse`, matcher `*`, command
  `node "$PI_CODING_AGENT_DIR/hooks/pre/seat-guard.ts" --claude-hook pre|post`, timeout 10 (the agy entry's
  values). The path is resolved from the agent dir the supervisor exports for every seat, as the agy entry already
  resolves it, so no absolute path and no backslash escaping is embedded in the file.
- `runner::child_argv` adds `--settings <seat dir>/settings.json` for claude-code while that file exists, and no
  flag while it does not: claude refuses to start on a `--settings` path that is not there, so a seat whose guard
  has not been installed is launched without the flag rather than with a broken one.

## The gate, and why it is deliberate

The hook mode itself lives in `assets/seat-guard.ts`, which `protectedPath` refuses to a seat (measured again
today: `blocked by seat guard (protected-paths)`). A guard without `--claude-hook` exits without reading the
payload, so a settings file written for it would plant a hook that checks nothing while looking installed.
`write_claude_guard_settings` therefore writes the file only while `GUARD_HAS_CLAUDE_HOOK` is true, and clears one
an earlier start left behind. The flag is an explicit const in `supervisor/mod.rs`, not a substring of the asset:
the asset's own header describes `--agy-hook` in prose, so a comment mentioning the claude flag would open a
substring gate while no mode exists.

The landing is **two steps**, both named in the owner's patch
(`~/.config/4genthub/rooms/4genthub-min/seats/go-dev/PATCH-2026-10-21-seat-guard-claude-hook.md`): the owner first
applies the `--claude-hook` mode to `assets/seat-guard.ts`, then this flag flips — one line, a seat may make it.
Until then a claude seat is unchanged: no settings file, no flag, no false assurance.

The file is the writer's own, and only its own: `is_claude_guard_settings` compares the bytes with what the writer
produces, so a `settings.json` someone else put in the seat dir is neither cleared when the guard lacks the mode nor
handed to claude as `--settings`. The seat dir is the runtime's to fill, not to empty — the same rule the agy
installer beside it follows when it refuses a foreign file.

## Verification

- `cargo test --offline` in `rust/agenthub-runtime`: 194 bin, 1 held_send, 27 `seat_guard`, 1 umask — all ok.
  The settings test forces the writer on (`write_claude_guard_settings_when`) and reads the file it wrote, so the
  writer's bytes are what CI checks while the gate is closed; the runner test writes that same file through the
  writer and asserts `--settings` follows it.
- `go test ./...` in `agenthub_client`: all packages ok at this tip.
