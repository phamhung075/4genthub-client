# The queue transition line names WHO moved the row

## Changed

- `internal/clientroom/queue.go`: `queueTransitionLine` renders
  `<transition_id> <ts> <from> -> <state> by <actor> | <note>`. The actor was printed bare after the arrow,
  and on an unblock (`blocked -> pending <the closer>`) a reader took it for the row's new DESTINATION: the
  owner deploy row `qitem-20261017222451` looked re-routed to reviewer, fe-dev and go-dev at each blocker's
  close, while its `destination_session` stayed lead across all 22 transitions (measured 2026-10-18 from
  `queue show`).
- `rust/agenthub-runtime/src/queue/mod.rs`: `closing_a_blocker_returns_its_blocked_rows_to_pending_once`
  now pins that the wake leaves `destination_session` alone and that the transition's actor is the CLOSER.
  `unblock_dependents` never wrote the destination; the ambiguity was in the rendering, and this is the
  assertion that keeps it impossible in the store.

## Tested

- `go test ./internal/clientroom/ -count=1` -> ok.
- `cargo test closing_a_blocker_returns_its_blocked_rows_to_pending_once` -> ok.
