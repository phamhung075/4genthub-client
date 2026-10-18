A handoff successor carries the closure note as its body

A `room queue close --reason handed_off_to --note <text>` created the successor row with the SOURCE
row's body: the note went to the source's transition line and nowhere else, so the receiver opened the
stale question the note had just answered. Measured 2026-10-18, 23:04-23:15: closing the 409 question
with its ruling produced a destination row whose body was the question; the reviewer claimed it in 7 s
and re-asked it, and the chain cost three rows.

The store now inserts the successor with the note as its body. An empty note is no account at all, so
the source body is carried unchanged - a handoff with nothing to say must not blank the work it hands
over. The source row, its transitions, the summary, the chain and the `handoff from <qitem>` line are
unchanged.

Changed files:
- `rust/agenthub-runtime/src/queue/mod.rs` - `close` computes `successor_body` (the note when it is
  non-empty, else the source body) and feeds it to the successor INSERT; the round-trip test asserts
  the successor body equals the note, and `a_handoff_without_a_note_keeps_the_source_body` pins the
  empty-note case.

Tests: `cargo test --offline` in `rust/agenthub-runtime` - 185 + 1 + 19 + 1 passed, 0 failed;
`go test ./internal/clientroom/... -count=1` ok; `go test ./...` rc 0. Red-first: with the successor
body forced back to the source body, `create_claim_close_round_trip` fails on the note assertion
(left the source body, right `over to seat-d`) while the empty-note case stays green.
