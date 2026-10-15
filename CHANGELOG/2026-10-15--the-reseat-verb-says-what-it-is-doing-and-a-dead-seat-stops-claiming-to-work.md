# The reseat verb says what it is doing, and a dead seat stops claiming to work

Two defects, one incident, measured on this box on 2026-10-15: `4genteam seat reseat` was killed at 300 s after
printing nothing at all, and the reviewer seat's own state file (`<room>/run/reviewer.state.json`) claimed
`working` for a session whose agent had been gone since 19:43:30Z - 42 minutes of a document asserting a live seat,
with the pane that outlived it keeping the status green.

## Fixed

### The wait is announced, and it can say what it cannot read (client)

- **`internal/clientseat/reseat.go`**: `reseatSeats` prints what it is waiting for BEFORE it waits - with the wait's
  budget as a duration - and prints the restart BEFORE the call that stops a session and starts another. `restarted`
  still follows the call. The give-up line names the same duration instead of `quietWaitSeconds/60`, which floored a
  budget that is not a whole number of minutes.
- **`internal/clientseat/quiet.go`**: `quietBudget()`. `QuietWaiter.quietOnce` now answers a third question - whether
  any activity could be read at all - and `Wait` reports the FIRST reading that cannot open, once per reason, the
  moment it happens. Before this, an unrecognized word was reported only at the deadline and an unreadable activity
  was reported NOWHERE, so a wait inside a 1500 s budget was silent by construction: the caller had no way to tell
  the wait from a verb that had hung.

### A claim whose publisher is gone (runtime)

- **`rust/agenthub-runtime/src/runner/mod.rs`**: `write_state` stamps the publisher's pid beside the state it writes.
- **`rust/agenthub-runtime/src/supervisor/mod.rs`**: `published_claim` reads the claimed word and the pid together;
  `process_is_alive` answers `Some(false)` only when the named pid is gone and `None` when the host cannot answer;
  `is_death_word` is now the one spelling of `DEATH_STATES`; `observed_death` returns the sentence when the pane is
  up, the runtime publishes a readiness document, that document claims life, and its publisher is gone. The status
  then (a) writes `agent_exited` into the seat's own state file, which the daemon owns, and (b) reports `Dead` with
  the observed sentence as the evidence instead of repeating the stale word.
- The paragraph in `get_room_status` already promised this ("the daemon, which owns this file's path too, replaces
  that claim with the death it can actually observe") and the code kept the promise only for a pane that was GONE. A
  pane that outlived its agent - the measured case, a SIGHUPed agent under a shell that stays - fell through to
  `Running`.

## Verified

- **`internal/clientseat`**: `go vet` rc 0, package tests pass. The three new tests FAIL on `08d8bbf` with exactly
  the measured behaviour: `the verb said 1 lines while waiting and restarting` (only `restarted`), `a wait that could
  read no activity said nothing at all`, and `the sentence arrived 1500s in, at the end of a 1500 wait`.
- **Whole module**: `go build ./...`, `go vet ./...`, `go test -count=1 ./...` rc 0.
- **`rust/agenthub-runtime`**: `cargo test` - 117 passed, 0 failed, including the four new unit tests and
  `the_status_corrects_a_claim_whose_publisher_is_gone`, which drives the real path (a real tmux pane, a room spec on
  disk, a state document whose pid cannot exist) and asserts both that the seat is `Dead` and that the document comes
  back naming `agent_exited`. The same test on `08d8bbf` reports the opposite, with the lie in its own words:
  `left: Running`, its evidence `the seat's own state file, published after its agent answered the transport
  handshake` - for a seat whose agent had been killed and whose file had said `working` for 42 minutes.

## Limits, and what is not in this change

- **PID REUSE IS THE NAMED LIMIT** of the publisher check: a recycled pid makes a stale claim look live. Its cost is
  the behaviour this change replaces - a stale `working` still shown as `Running` - and never the other direction,
  where a live seat would be called dead and its work killed.
- **A host that cannot answer keeps the old verdict**: `/proc` absent makes `process_is_alive` answer `None`, and
  `None` is not `false`, so nothing is invented from a missing directory.
- **The hub's `TEST-CHANGELOG.md` carries no line for these new tests.** It is a hub-repo file and this is a
  client-repo branch; the line belongs to the hub commit that bumps the pin, which is the lead's landing.
- **The parked T14 instrument is not here** (`ai_docs/api-integration/surface-inventory.md`, `scripts/CITATION-AUDIT.py`):
  it is parked by the owner's work-first rule.
- **Nothing pushed.**
