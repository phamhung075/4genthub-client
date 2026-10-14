# The room's `logs/` is not product state

`2800dfb` is withdrawn. It made `start_room` create `seats/`, `logs/` and `run/`, and made the runner mirror
its own diagnostics into `<room>/logs/<seat>.jsonl` — product code added to satisfy an assertion, which is
what the ruling against it says: it "would restore a concept deleted the day before **and** add product code
to pass an assertion (repo rules 1 and 2)". The commit raced that ruling by under a minute.

## What was wrong with it

Three things, and the first two are the ruling's own words re-checked at source.

1. **`logs/<seat>.jsonl` was retired, not left unimplemented.** The runtime holds one definition of a seat's
   record and it is not that file: `supervisor::transcript_path` resolves the newest `*.jsonl` under
   `<room>/seats/<seat>/sessions/`, and the client reads it there (`internal/seatlog`). Writing a second file
   for the same fact would be a second convention for a seat's log — the duplication the cutover exists to
   remove.

2. **A1's `logs/` assertion was the harness's, not the product's.** The acceptance's own hygiene treats
   `logs/` as run-state (`RUN_STATE_SUBDIRS="run seats logs"`, and `rm -rf` over them between legs), so a room
   can only pass that assertion by recreating a directory the instrument itself deletes. The correction goes
   the other way: A1 asserts what the product guarantees — `spec.json`, `seats/`, `run/`.

3. **B2 was not vacuous, so nothing needed writing for it.** The cap-string leg reads
   `$(victim_transcript)`, defined as the newest session jsonl of the victim seat; only its *label* said
   `logs/<seat>.jsonl`. The fix there is labels, not a file — which is why no reader had to be re-pointed.

A `logs/` written by nothing and asserted by the harness is the failure mode the ruling names: the assertion
would pass over a fiction, and the next reader would take the directory as evidence of a writer.

## The change

The two Rust halves are reverted byte-for-byte: the room's layout is no longer created by `start_room`, and
the runner no longer opens a second log. `rust/` is identical to `2800dfb^`.

What replaces it is harness-side — A1's loop and label, `RUN_STATE_SUBDIRS`, and the three stale labels that
name `logs/<seat>.jsonl` — and one *product* half, in this same unit because it is a false claim in
user-facing text: `4genteam room stop` said the room's directory holds `spec.json, seats/ and logs/`, and
`logs/` is not there. That is the next entry.

## Evidence

- `cargo test --offline` in `rust/agenthub-runtime`: **86 passed, 0 failed** (88 before: the two tests that
  came with the withdrawn change go with it).
- `git diff 2800dfb^ -- rust/` is empty — the revert is exact, not a re-edit.
- The reverted file set is the whole of `2800dfb`: `rust/agenthub-runtime/src/{runner,supervisor}/mod.rs`
  (174 lines removed, 2 restored), and this branch carries no later change to either file.
