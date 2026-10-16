# The way back from a park is the record's absence, and the round trip is pinned

A park is the RECORD's existence, not its contents (`rust/agenthub-runtime/src/park.rs`: "THE RECORD'S
EXISTENCE IS THE PARK"), so the way back from one is that file's absence. Both directions were already
pinned - every route refuses while the record is there, and a seat that never had one takes work - but no
case ran the TRANSITION in one process, and the transition is what the exit depends on: the daemon that
refused a seat is the daemon that must hand it work again, without being replaced.

### Testing
- `rust/agenthub-runtime/src/supervisor/mod.rs`, `a_parked_seat_is_refused_by_the_runtimes_own_routes`: after
  the send and restart refusals, the record is removed and the SAME supervisor must answer for itself again.
  A park remembered past the record's removal is how a seat that was let back in stays out.
- `rust/agenthub-runtime/src/runner/mod.rs`, `a_parked_seat_is_not_nudged`: the rail nudge gets the same arm,
  and its inverse in the durable state - an unparked seat spends the ladder again, so the attempt the refusal
  withheld is recorded.
- Mutation check, the removal withheld in both arms: both tests FAIL on the new assertions, each carrying the
  runtime's own refusal in the message (`with the record gone the same nudge must answer for itself again:
  Err("seat dev of room lab is PARKED: ... the rail prompt was not delivered")`). Restored: both pass.
- `cargo test` in `rust/agenthub-runtime`: 133 + 11 + 1 passed, 0 failed.
- The CLIENT tier is deliberately untouched: `seatpark.Refuse` re-reads the record on every call, and its two
  arms already exist (`TestAParkedSeatIsRefusedWithTheNumbersThatStoppedIt`, `TestASeatWithNoRecordTakesWork`),
  so a case there would assert the same two states a third time.

### Not implemented here
- The EXIT itself: which way a seat comes back - by hand, a verb, auto-rearm at the window, or a raised
  ceiling - is the owner's call (the park is a spend safeguard, and spend is an owner category). What these
  arms pin is the invariant all four end at: the record goes and the routes work again. What the chosen exit
  still owes is its own driver and a live smoke on a real seat.
