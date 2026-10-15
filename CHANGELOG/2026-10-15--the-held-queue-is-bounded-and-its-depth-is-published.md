## The held queue is bounded, and its depth is published

### What was wrong
Two required edits the lead filed against `04a9565` (`qitem-20261015051651-02eadb2bd005`, corrected at
`qitem-20261015051833-c43639917d15`), both about the hold the reviewer's gate created.

**The queue had no bound.** `hold_text` pushed onto the `VecDeque` and nothing anywhere in `runner/mod.rs`
carried a `MAX_HOLD`/`HOLD_DEPTH`/overflow of any kind. Every entry is a message the seat has not seen, and the
queue lives in the runner process's memory: a seat whose agent refuses prompt after prompt — a turn that never
ends, or an agent answering every frame with the same refusal — grew it for as long as the room lived and never
said so. That is a silent, unbounded drop path in the same mechanism whose whole purpose is that a refused text
is not silently dropped.

**The state file's sentence had no count.** `publish_hold`'s predecessor computed the depth (`waiting`) in the
same arm that wrote the classification, and then wrote `"held: the agent refused this prompt while its turn was
running"` — the same words at one held text and at eight. The classification is the surface this runner already
uses for a held text (`deliver_held`'s doc says so), and `4genteam ps` and the daemon's `status` serve it, so a
fact with no size was published where a reader looks for the size.

### Changed
- `rust/agenthub-runtime/src/runner/mod.rs`:
  - `pub const MAX_HELD: usize = 8` — the queue's bound, with the reasoning for the number (a burst inside one
    long turn, not a backlog).
  - `hold_text(...) -> Result<usize, usize>`: `Ok(depth)` after appending, `Err(depth)` when the queue is at the
    bound. The refusal EVICTS NOTHING — the texts already waiting keep waiting, in order.
  - `publish_hold(state_path, held, attempts) -> String` is the new one writer of the outcome, and it publishes
    the depth: `"held: … ({depth} of 8 held for the next turn)"` with state `working`, or, for a full queue,
    `"the held queue is full ({depth} of 8): the agent refused this prompt and the runner could NOT hold it"`
    with state `blocked` — a runner that cannot keep what it was asked to keep is not healthy.
  - The `Resume::Hold` arm matches the answer: it mirrors the `held for the next turn` line plus the
    `waits behind N` line when queued, and `refused, not held: <the published sentence>` when the queue is full.

### Tested
- `runner::tests::the_held_queue_is_bounded_and_names_the_depth_it_refused` (new): the queue takes `MAX_HELD`
  texts reporting each depth, the next offer is `Err(MAX_HELD)`, the refusal evicts nothing, and the text refused
  first is still in front.
- `runner::tests::a_refused_prompt_is_held_and_delivered_when_the_turn_ends` (extended): the published
  classification now has to carry `2 of 8` and match the state document, and a full queue has to publish
  `blocked` with `full (8 of 8)` named.
- `runner::tests::two_refusals_in_one_turn_keep_both_texts_in_order` (updated to the answered API): the ordering
  assertions are unchanged.
- `cargo test --offline` in `rust/agenthub-runtime`: **99 passed, 0 failed**.
- Not verified: the live effect. No push, deploy or restart; the running seats still run the pre-fix binary.
