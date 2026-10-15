# Seat guard: a bare tmux command that changes anything is blocked, because it acts on the room's own server

### Fixed
- `rust/agenthub-runtime/assets/seat-guard.ts`: new `tmux-mutate` rule. `$TMUX` is set inside a seat's pane and wins over `TMUX_TMPDIR`, so a bare `tmux kill-server`, `new-session` or `send-keys` there reaches the room's real server. fe-dev ran `tmux kill-server` at the end of a tmux repro at 18:51Z on 2026-10-15 and all five seats stopped.
- Passes: a command that names a server with `-S` or `-L` (`env -u TMUX tmux -L scratch ...`), and the read-only verbs `list-*`, `ls`, `display-message`, `capture-pane`, `show-*`, `has-session`, `info`. Text that only mentions tmux (`echo tmux is nice`, `grep -c tmux f`) is not a call.

### Testing
- The pattern was checked on 13 commands with bun (five blocked forms incl. `TMUX_TMPDIR=x tmux kill-server`, `env -i tmux ...` and `sudo tmux ...`; the rest pass); `cargo test --offline seat` covers the rule name. Live on a seat at its next start.
- Not covered: tmux called from inside a script or `python -c`; the hook reads only the command text.
