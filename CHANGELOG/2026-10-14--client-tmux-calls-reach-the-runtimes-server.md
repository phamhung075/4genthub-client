## Every client tmux call reaches the runtime's own tmux server

### Fixed

- `ps`, `feed`, `watch`, `grid`, `seat status`, the restore watchdog, the supervisor interrupt and the install checker ran
  `tmux` against the caller's default server, so with `TMUX_TMPDIR` unset `4genteam ps --nodes --json` answered `[]` for a
  running native room. The runtime keeps its server under `<state root>/tmux`; only the acceptance harness exported that
  path, which hid the defect.

### Changed

- `internal/clientenv/tmux.go`: `TmuxRoot` (the runtime's `tmux_root_for` derivation), `TmuxArgv` and `TmuxCommand`.
  `TmuxArgv` is `env -u TMUX TMUX_TMPDIR=<root> tmux ...`: it overrides an inherited `TMUX_TMPDIR`, ignores the server of the
  pane the client runs in, and refuses (no default-server fallback) when no state root resolves.
- Call sites moved onto it: `clientps/ps.go`, `clientwatch/grid.go`, `clientrestore/{tmux,watchdog}.go`,
  `seatlog/seatlog.go`, `clientlifecycle/supervisor.go`, `clientsync/installcheckerverb.go`.

### Tested
`clientenv` (env override, refusal, derivation), `clientps` (`TestListSessionsReadsTheRuntimesServerWhateverTheEnvironmentNames`,
red on the previous code in a scratch `git archive`), `clientwatch` (`TestGridReadsSeatsFromTheRuntimesTmuxServer`).
`go build ./... && go vet ./... && go test ./...` pass. Read-only check against the live `cutover-lab` room with
`TMUX_TMPDIR` and `TMUX` unset: `ps --nodes --json` lists its seats.
