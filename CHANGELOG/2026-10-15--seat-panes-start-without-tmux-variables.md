# Seat panes start without `TMUX` and `TMUX_PANE`

### Fixed
- `rust/agenthub-runtime/src/pty/mod.rs`: the pane command `env -u <denied keys> <runner>` now always unsets `TMUX` and `TMUX_PANE` (new `pane_command`, `PANE_TMUX_KEYS`). tmux sets both in every pane, and `$TMUX` beats `TMUX_TMPDIR`, so a bare `tmux` in a seat acted on the room's own server: fe-dev's `tmux kill-server` ended all five seats on 2026-10-15. Without them a bare `tmux` in a seat reaches the user's default server, and the `tmux-mutate` guard rule stays as the second layer.
- Nothing in the client, the runtime or `scripts/git-hooks/notify-seats` reads `TMUX` or `TMUX_PANE` (the hook already runs `env -u TMUX`).

### Testing
- New `pane_command` test (unit); `cargo test --offline` passes in `rust/agenthub-runtime`. Takes effect for a seat at its next pane launch (a reseat).
