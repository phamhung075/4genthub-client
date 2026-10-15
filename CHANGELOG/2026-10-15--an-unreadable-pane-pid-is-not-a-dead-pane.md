## An unreadable pane pid is not a dead pane

### Fixed

- `rust/agenthub-runtime/src/pty/mod.rs`: `list-panes -F #{pane_pid}` is read through a new `pane_pid`
  function that CHECKS the exit status and carries tmux's stderr. Before, the status was never read and the
  answer went through `unwrap_or(0)`, so tmux's refusal (which goes to stderr) and a pane whose pid is genuinely
  0 were the same value to every caller. The new `PaneRecord { pid: Option<u32>, unread: Option<String> }` and
  `PaneRecord::describe()` carry the two facts apart, and `launch_session` returns the record instead of a u32.
  Measured 2026-10-15 in the C4 acceptance run: `session agenthub--q1probe-ok--solo started with pane pid 0` at
  05:35:50.250518Z, then `seat q1probe-ok/solo died before it reported readiness (its pane is gone and no ready
  frame ever arrived: waited up to 60s)` at 05:35:50.252609Z - 2.09ms later, with the whole 60s budget unused.
- `rust/agenthub-runtime/src/supervisor/mod.rs`: the readiness loop no longer builds a death verdict on a read
  that failed. When a session read says the pane is gone, it asks AGAIN IMMEDIATELY - the same read, not after a
  sleep, because a delay would hide the race instead of asking a second time - and only refuses when the second
  answer agrees; a transient read is logged as one. The refusal now states the wait PERFORMED (measured, from a
  monotonic `Instant`) instead of the window it was given, and it reports the pane record: `pane pid N` when
  tmux named one, `no readable pane pid (<tmux's own words>)` when it did not. "Its pane is gone" now means two
  reads agreed the pane is gone.
- `rust/agenthub-runtime/src/hooks/mod.rs`: `SeatEvent::SessionStarted.pid` is `Option<u32>` like `SeatStatus.pid`
  already was, so an event cannot carry a 0 manufactured from a failed read as if it were a pid. A JSON consumer
  that receives `null` reads "not known".

### Testing

- `pty::pane_pid_tests` (5 cases): an unparsable answer is a FAILED READ naming what tmux said, while `0` and
  `7958` parse as pids - the boundary the defect collapsed; against real tmux, a session that is not there is
  refused in tmux's own words and never as `pane pid 0`; a live session answers a pid the record describes as
  one; `PaneRecord::from` maps a failed read to words and a successful one to a pid.
- `supervisor::tests::a_dead_non_omp_seat_is_refused_by_its_real_cause` gained two assertions: the refusal names
  TWO AGREEING READS of the session, and it does not contain `waited up to` (the lie in the measured line).
- `supervisor::tests::a_transient_session_read_does_not_kill_the_seat` (new): a `tmux` shim ahead of the real one
  on PATH lets `new-session` through and fails the FIRST `has-session` for that test's pid-named session only -
  the race, forced. The seat must come up anyway, which can only happen if the supervisor asked again; the shim's
  own refusal is what the run's log carried. FALSIFIED: with the re-read disabled (`if false && …`) this case
  FAILS on the transient read; restored, it passes.
- MEASURED: `cargo test --offline` - 105 passed, 0 failed. `cargo clippy --offline --all-targets` reports no
  warning on a line this change touched (its warnings are pre-existing, in other files).
- NOT done, deliberately: no sleep was added (the order names a delay as masking the race rather than fixing it)
  and the window's length was not changed.
