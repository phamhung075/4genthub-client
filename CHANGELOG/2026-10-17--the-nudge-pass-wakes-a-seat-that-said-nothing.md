# The nudge pass wakes a seat that has said nothing

### Changed

- `supervisor::pending_nudges` selects a seat unless its own published word says a turn is in flight
  (`Working`, `NeedsInput`), and only counts a word from a runtime that can publish one
  (`AgentRuntime::has_runner`). `Idle` is selected as before; `Unknown` now is too - no document, a document
  without the word, or an earlier runtime's leftover. `seat_is_idle`, which compared the raw `state` to
  `idle` by reading the file a second time, is deleted: the status row the pass already builds carries the
  parsed word, and one derivation is the point of `seat_facts`.
- `runner::nudge_seat` takes `has_runner` and routes the nudge the way `deliver_text` already splits: the
  runner mailbox where the runtime publishes one, the pane where it does not (`agy`, `codex`, a custom
  command). The attempt log records what each route proved - `prompt` from the runner's `relayed` answer,
  `submitted`/`unconfirmed` from the pane.
- `supervisor::PendingNudge` carries `has_runner`, decided once from the status the pass already read.

### Why

Measured 2026-10-17 06:09Z (task 022666d6): a gate row addressed to an idle agy seat at 06:08:57Z earned no
nudge attempt for six minutes - `run/reviewer.nudge.jsonl`'s last line was 03:53:33Z while the same pass
nudged lead/go-dev/fe-dev at 06:02-06:03Z - and a hand send woke the seat in seconds. The only document in
`run/` was the previous omp generation's `agent_socket_closed` (04:00:36Z, before the 04:02:58Z relaunch),
and the old gate read it as "not idle" forever. Had the pass selected the seat,
the mailbox write would have failed as well: an agy seat has no runner socket, so the delivery route had to
change with the selection rule.

### Testing

Three supervisor tests: the measured case (agy spec, live pane, the previous generation's death document -
the pass selects the seat and `nudge_pass` records attempt 1, `unconfirmed` on a pane that never reads);
a turn in flight in all four busy words, then `idle` selects; a document without the word selects. The
parked-seat test carries the new argument. `cargo test --offline` green (156 unit + 17 integration), `go test
./...` green (32 packages).

Measured before/after in a throwaway root (own state dir, socket, daemon and tmux root, one agy-runtime seat,
a row queued for it): at a40d9a0 no `nudge.jsonl` after 5 s; on this tree attempt 1 at +3.1 s. A pane that
reads its input records `submitted`; a sleeping one `unconfirmed`.
