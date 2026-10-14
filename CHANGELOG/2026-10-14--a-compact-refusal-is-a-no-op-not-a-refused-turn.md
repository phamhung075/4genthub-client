## A compact refusal is a no-op, not a refused turn

### What was wrong
A `compact` relayed to a seat that has no turns yet is answered by omp with `Nothing to compact (session too
small)`. `classify_failure` matched none of its retryable signatures and returned `Terminal("the agent refused
the turn")`, so `decide_resume` answered `Blocked` and the runner published `run/<seat>.state.json` as
`blocked`. The nudge loop wakes only a seat whose own word is `idle`, so a seat blocked this way is never
handed a queue row. Measured on the live room `4genthub-min` (2026-10-14): go-dev, fe-dev and reviewer were
blocked at first start this way (`updated_at` 19:12:55Z, `classification` "the agent refused the turn"). The
seat was healthy; its own health report killed it.

### Changed
- `rust/agenthub-runtime/src/runner/mod.rs`:
  - `Failure::Noop(&'static str)` — a third classification: the agent declined a *request*, no turn died,
    nothing is published and nothing is re-sent.
  - `NOOP_SIGNATURES` holds both halves of the message omp actually sends (`Nothing to compact`,
    `session too small`) and is consulted **before** the retryable table, so a reply whose id this runner
    cannot correlate cannot turn a benign refusal into a blocked seat.
  - `decide_resume`: `Failure::Noop` → `Resume::Nothing`. The caller mirrors it and writes no state, so a
    fresh seat keeps the `idle` it published at handshake and stays wakeable.
  - `is_compact_reply(id)` — the id rule the previous commit inlined, now one named predicate: a reply to a
    fire-and-forget `compact` is never a turn's outcome, whatever it says. Without it a transport error on a
    compact would be refused as "a retryable failure with no turn to resume" and block the seat for the same
    non-reason. The two rules cover the two measured shapes: an id the runner recognises, and one it does not.
  - The `COMPACT_ID_PREFIX` doc comment no longer sits between `request_compact`'s own doc and its body.

### Tested
- `cargo +stable test` in `rust/agenthub-runtime`: **88 passed, 0 failed**.
- `runner::tests::a_compact_refusal_with_nothing_to_compact_is_a_no_op` (new): both message halves classify
  `Noop`, and `decide_resume(false, false, false, <refusal>, 0)` answers `Nothing` — the exact shape a
  fire-and-forget compact reply arrives in — instead of `Blocked`.
- `runner::tests::a_compact_reply_is_told_from_a_turn_by_its_id` (new; replaces the tautological
  `a_compact_reply_id_is_recognised`, which asserted that a concatenation starts with its own prefix): a real
  `forcecompact-<pid>` id is recognised; `runner-resume-1-1`, `runner-get-state` and the uncorrelated `?` are
  not.
- Falsified, not read: with the no-op table not consulted, the new test fails with
  `a compact refusal classified terminal (the agent refused the turn)` — the recorded defect's own words —
  and the file was then restored byte-identical (sha256 `7da7e2ed390fa43d35389bffc59f4e0992721748a7677d4411f56c40239b73c1`).
- Not verified: the live effect. The running seats still run the pre-fix binary (installed 21:09); nothing was
  pushed, deployed or restarted, and the live rig was not touched.
