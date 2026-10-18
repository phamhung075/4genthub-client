## A stop on a tmux server holding no sessions is nothing to stop

### Fixed

- `rust/agenthub-runtime/src/pty/mod.rs` (`stop_session`): the not-found filter accepted `can't find session`
  and `no server running`, but a server that is UP with `exit-empty off` and holds NO sessions answers
  `kill-session -t <name>` with `no current target` - a third spelling of the same fact. Measured 2026-10-18
  on a scratch root: an empty server returns `no current target` and exit 1, while a server with any session
  returns `can't find session`. `stop_seat` is the first step of `restart_seat`, so this refused a reseat
  before it could start anything, on the one path - a room whose tmux server has no seats yet - where the
  stop has nothing to do.
- The filter now accepts the third spelling, with the measurement in the comment.

### Testing

- `cargo test --manifest-path rust/agenthub-runtime/Cargo.toml`: 176 + 1 + 18 + 1 passed. The new case owns a
  scratch tmux root, starts its server with no sessions, and asserts `stop_session` returns `Ok`.
