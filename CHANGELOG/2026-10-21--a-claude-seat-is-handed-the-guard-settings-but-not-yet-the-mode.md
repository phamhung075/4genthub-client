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
  `node "<seat dir>/hooks/pre/seat-guard.ts" --claude-hook pre|post`, timeout 10 (the agy entry's values).
- `runner::child_argv` adds `--settings <seat dir>/settings.json` for claude-code while that file exists, and no
  flag while it does not: claude refuses to start on a `--settings` path that is not there, so a seat whose guard
  has not been installed is launched without the flag rather than with a broken one.

## The gate, and why it is deliberate

The hook mode itself lives in `assets/seat-guard.ts`, which `protectedPath` refuses to a seat (measured again
today: `blocked by seat guard (protected-paths)`). A guard without `--claude-hook` exits without reading the
payload, so a settings file written for it would plant a hook that checks nothing while looking installed.
`write_claude_guard_settings` therefore writes the file only when the bundled guard carries the mode —
`SEAT_TOOL_HOOK.contains("--claude-hook")` — and clears one an earlier start left behind. The wiring goes live in
the same commit range as the owner's guard patch, with no second edit here: the patch is
`~/.config/4genthub/rooms/4genthub-min/seats/go-dev/PATCH-2026-10-21-seat-guard-claude-hook.md`.

Until then a claude seat is unchanged: no settings file, no flag, no false assurance.

## Verification

- `cargo test --offline` in `rust/agenthub-runtime`: 194 bin, 27 `seat_guard`, 1, 1 — all ok.
- `cargo build --offline`: clean.
- `agenthub_client`: `go build ./...`, `go vet ./...`, `go test -count=1 ./...` clean at this tip.
