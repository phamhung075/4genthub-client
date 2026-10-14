# The room's `logs/` exists, and the seat's own log is written into it

The U3 acceptance asserts the room's layout immediately after `up` — `seats/`, `logs/`, `run/` — and
`rooms/<room>/logs/` was created by **nothing**: the runtime made the room directory, `seats/<seat>` and
`run/` (`supervisor::start_room`, `start_seat`), and this client's only `logs` is the state-root one
(`clientservices/services.go`). A fresh room therefore had no `logs/`, the assertion failed, and the
harness's rule 2 then skipped nine further legs — run 4 scored 69 pass / 1 fail / 9 skip on that one
directory.

The same absence hollowed out a second leg: `B2` asserts the cap string is *absent* from
`logs/<seat>.jsonl`, and since that file never existed the assertion passed by reading nothing. The seat's
real record is its transcript, `seats/<seat>/sessions/*.jsonl` — a different file, and the one
`read_transcript` refuses by name when it is missing.

## The change

Two halves, and they are the reference's own log that the harness was written against.

1. **The room is prepared with its layout.** `start_room` creates `seats/`, `logs/` and `run/` beside the
   room directory, so the shape is part of starting a room rather than something whichever writer gets
   there first happens to need. The test pins it even for a room that refuses to start a seat, because the
   room directory and `spec.json` are already left behind in that case: the layout follows the same rule.

2. **The runner writes the seat's log** at `<state dir>/rooms/<room>/logs/<seat>.jsonl`. `mirror` — the one
   door every runner diagnostic goes through before it reaches the pane, all 21 call sites (the retry
   ladder, `gave up`, the refused pane input, mailbox errors) — now appends the same line to that file as
   one JSON line with its timestamp. One process is one seat's runner, so the path is decided once, at the
   start of `run`, before anything can be mirrored — including the refusal to launch at all. A process
   that never became a runner writes nothing.

**Why the file rather than re-pointing the readers at the session transcript.** The cap string is the
*runner's* own message: it is printed to the pane when the pane's input exceeds its cap, and it can never
appear in the session transcript. Re-pointing the readers would have made that assertion permanently
vacuous — the defect it is meant to catch, written into the instrument. Writing the log lets the leg
measure something again.

## Evidence

- `cargo build --offline` clean (no new warnings from these lines; the crate's 24 pre-existing ones are
  elsewhere).
- `cargo test --offline`: **88 passed, 0 failed**, including the two new tests —
  `the_seat_log_is_the_rooms_logs_dir_and_holds_what_the_pane_showed` (the path shape, one JSON line per
  mirrored line, each carrying its time, and **the cap line and the ladder legible as text**, because a
  reader greps for them) and `starting_a_room_creates_its_seats_logs_and_run_directories`.
- **Smoke, the built binary as a real seat's runner:** a scratch state dir whose `lab/dev` seat carries a
  policy word the runner refuses. The pane showed
  `[omp-runner] refusing to launch: permission policy "nonsense" is not one of …`, and beside it
  `rooms/lab/logs/dev.jsonl` held
  `{"text":"refusing to launch: …","ts":"2026-10-14T21:00:21.703618478+00:00"}`, with `run/dev.state.json`
  carrying the same refusal. Exit 0.
- `rustfmt` has no objection to the added lines. The crate as a whole is not rustfmt-formatted (13 files
  differ before this commit), so no blanket reformat was run.
