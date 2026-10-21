# claude has one guard writer: the seat's own settings file

`4genteam guard install` no longer writes a claude entry into `$HOME/.claude/settings.json`
(`RULING-2026-10-21-claude-guard-writer`). The guard reaches claude through `<seat dir>/settings.json`, which the
runtime writes and passes with `--settings`; an owner-wide entry would fire beside it on every tool call (double
guard, double refusals) or never fire at all, and the mode question was moot either way.

- `internal/clientguard/runtimes.go`: `runtimeSpec` gains `seatFile`, and claude's row names `settings.json`, so
  `RuntimeHook.CoveredBySeat` marks the row the client must never write. `InstallRuntimeHooks` skips it; the row's
  note says where the guard comes from and why no owner-wide entry is written. The claude writer is gone with it -
  `claudeGuardDefinition`, `withClaudeHooks`, the claude arm of `guardEntryFor`/`writeRuntimeEntry`, and the
  `otherwise`-silence spelling in `GuardedCommand` (`GuardedCommand` and its `otherwise` stay for agy). The two-way
  mode constant `guardCarriesClaudeHook` and its rust-mirror test are deleted: the client no longer decides on the
  claude mode at all. `--settings`-vs-user-settings was left unmeasured, as the ruling orders: either answer forbids
  the entry.
- `UninstallRuntimeHooks` still takes a claude entry out of the owner's file - a hand edit or an older build - and
  the row reports one as stale; the removal keeps every other key, definition and array. `withoutClaudeHooks` is the
  only claude code left.
- `internal/clientlifecycle/runtimesrow.go`: doctor's claude row says the guard is the seat's own
  `<seat dir>/settings.json` and never offers the install verb; a stale entry gets its own line naming `guard
  uninstall`. agy's line is unchanged.
- The header of `runtimes.go` states the rule: one writer per surface, and claude's is not this file.

Tested on `4genthub-min/runtime-hooks` after merging `fbc571d2`: `go test ./...` = 35 packages ok, 0 FAIL;
`gofmt -l internal/ cmd/` clean; `go vet ./...` clean. Live smoke on throwaway homes with fake `agy`/`claude` on
PATH, from the built `4genteam guard install --home <h> --checkout <tmp git repo>`:

- a home with no `~/.claude` gets none (the directory is not created), while `~/.gemini/config/hooks.json` gets the
  agy entry and the claude line names the seat file;
- a home whose `~/.claude/settings.json` holds owner keys is byte-identical after the install (`sha256sum -c` OK);
- a home whose claude file already carries an older build's guard entry is reported as stale by the install, and
  `4genteam guard uninstall` removes exactly the guard definitions, keeping the owner's own hook and `env` key.
