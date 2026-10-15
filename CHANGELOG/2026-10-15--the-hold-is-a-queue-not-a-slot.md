## The hold is a queue, not a slot

### What was wrong
The reviewer's required edit on `c0ffbe8` (`GATE-c0ffbe8-refused-send-2026-10-15.md`). A prompt this runner
relayed and the agent refused mid-turn is held for the next turn — but the hold was a SINGLE SLOT and the
write was unconditional (`*held_reader.lock() = Some((id, text))`), so a second refusal inside the same turn
overwrote the first text. The first text was then in no surface at all: not delivered, not held, and not named
by the `mirror` line, which reports the refusal and not what it displaced. That is the same silently-lost
message class this hold exists to remove, and R1 (no silent skip) is the standing rule.

### Changed
- `rust/agenthub-runtime/src/runner/mod.rs`:
  - `held` is `Arc<Mutex<VecDeque<(String, String)>>>` — a queue, in the order the texts were refused.
  - `hold_text(held, refused_id, text) -> usize` is the new one writer of it; the `Resume::Hold` arm appends and,
    when a text is already waiting, mirrors that this one waits behind N held text(s) — the queue is visible
    rather than implied.
  - `deliver_held` takes the FRONT of the queue, one text per call: two texts held in one turn come back in
    order, one turn apart, instead of racing the turn the first delivery opens.
  - The `idle` decision at `agent_end`/`session_settled` reads `!held.lock().is_empty()`, so a seat with a text
    still queued is published `working`.

### Tested
- `runner::tests::two_refusals_in_one_turn_keep_both_texts_in_order` (new): two refusals queue as 2 (the slot
  held 1), the first turn end delivers the first text, the second turn end delivers the second, and both leave
  the queue exactly once.
- `runner::tests::a_refused_prompt_is_held_and_delivered_when_the_turn_ends` (updated to the queue API): the
  delivery, the emptied queue, and the bound that turns a seat whose agent keeps refusing into `blocked`.
- `CARGO_NET_OFFLINE=true cargo test --offline` in `rust/agenthub-runtime`: **98 passed, 0 failed** (97 with the
  fix reverted, which is the count of the merged tree without the new test).
- Not verified: the live effect. No push, deploy or restart; the running seats still run the pre-fix binary.
