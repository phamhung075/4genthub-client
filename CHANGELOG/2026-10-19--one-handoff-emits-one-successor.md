One handoff emits one successor, and the successor names the row it continues

Measured 2026-10-18 (review 3 line 4, `qitem-…202513-2319a0d2abfb`): one handoff left two pending rows
for the receiving seat. The second was the successor `close --reason handed_off_to` creates, and it was
duplicable because `QueueStore::close` read the row OUTSIDE its write transaction: the terminal guard
("already in terminal state") was checked against a snapshot, so two closes of one row both passed it and
both inserted a successor - two rows, same summary and body, for one handoff. The room paying for it was
this one, which is why a lane row was re-emitted after it had been processed.

`close` now opens its transaction `IMMEDIATE` before it reads the row, so the terminal check and the
successor insert are one critical section: the second close sees `handed-off` and is refused, and the
source keeps exactly one handoff transition. The chain is the emission mark - a row whose chain is this
source's chain plus the source IS the successor already emitted - and a second one is refused by name
rather than written, which also covers a retried close after an unknown outcome.

The successor now carries `reply_to` = the source qitem, so a continuation is legible as one from the row
itself and not only by walking its chain.

Proven: `cargo test --offline` in `rust/agenthub-runtime` - 185 + 1 + 19 + 1 passed, 0 failed. The new
`queue::tests::a_double_close_emits_one_successor` opens one store per thread, as the daemon opens one per
request, and counts the target's rows: red before the fix (both closes succeeded, two successors), green
after. Smoke on a scratch daemon (built binary, room `scratch`): two concurrent
`room queue close --reason handed_off_to --target seat-d` calls - one exits 0, the other is refused as
"already in terminal state 'handed-off'", `room queue list --destination seat-d` shows one row, and that
row's `reply_to` is the source qitem. `go test ./...` in the client exits 0.
