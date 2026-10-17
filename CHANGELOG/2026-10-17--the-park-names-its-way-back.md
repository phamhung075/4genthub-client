# The park names its way back, and its row asks why the seat returned

The owner ruled the exit from a park BY HAND (2026-10-16 22:31Z): remove `seats/<seat>/parked.json`, then
reseat - no unpark verb exists and none is added. That makes the record's own artifacts the only place the
exit can be READ, and neither of them named it: `roombudget.Parked` carried the numbers that stopped the seat
and nothing about undoing them, and the row the breaker filed said how to lift the park but asked for nothing
back, so the half of the acceptance that wants "why the seat returned" had no carrier - by hand, the exit
files no row of its own.

- `internal/roombudget/roombudget.go`: `Parked` gains `exit`, filled by `ParkExit(rig, seat)`. The text names
  the act (remove this file FIRST), the next one (`4genteam room reseat <room> <seat>`) and the ORDER's own
  consequence, measured rather than taken from the ruling's prose: `4genteam room reseat` stops the seat and
  then restarts it (`internal/clientroom/room.go:423/:426`), while the runtime's `restart_seat` refuses
  BEFORE its own stop whenever the record exists (`rust/agenthub-runtime/src/supervisor/mod.rs:1011`, via
  `ipc/mod.rs:143`) - so a reseat run first STOPS the seat and leaves it stopped, which is a worse outcome
  than the ruling assumed and is why the field says remove, then reseat. The room and the seat are written
  out rather than left as placeholders, because the file is what an operator copies from and the room is not
  otherwise readable out of it; a placeholder would also be JSON-escaped (`\u003croom\u003e`) by the encoder
  that makes the file.
- `internal/clientlifecycle/budgetguard.go`: the park row's reseat sentence is REPLACED, not appended to -
  it carried the same softer outcome - and the row ends by asking the reader to CLOSE THIS ROW and say why
  the seat came back: the record is the park, the row is the return.
- An older runtime reads the newer record: the runtime's `Parked` (`rust/agenthub-runtime/src/park.rs`) derives
  `Deserialize` with every field `#[serde(default)]` and no `deny_unknown_fields`, so `exit` is ignored and the
  file's existence still decides. No runtime change is needed for this field.

### Testing
- `internal/roombudget/roombudget_test.go`, `TestTheParkRecordNamesTheWayBack`: the record is written through
  `WriteParked`, read back off disk, and must carry the concrete command - so a reader holding only the file
  learns the exit, and `ReadParked` returns the field rather than dropping it.
- `internal/clientlifecycle/budgetguard_test.go`, in `TestASeatOverItsUSDBudgetIsParkedOnceAndNamedOnce`: the
  row must contain the close request, and the record's own `Exit` must equal `ParkExit(room, seat)` - the park
  is asserted through the row AND the record, which is the pair the acceptance names.
- Caps: `TestTheParkRowFitsTheRailsCapsAtItsWorstCase` (the rail refuses a body over 600 and a summary over
  120) still passes with the corrected sentence, which is 23 characters SHORTER than the one it replaces.
  Measured at this room's own names and a production-length state root (room `4genthub-min`, seat
  `architect`): the row is 91/120 summary and 561/600 body; the caps test's own fixture drives a longer seat
  dir than production and passes.
- Smoke, the real guard driven end to end with only the world-reading seams replaced (seat list, limits, usage,
  the stop, the rail): the record on disk is 347 bytes, its `exit` value is

      remove this file FIRST, then `4genteam room reseat 4genthub-min architect`; the record's absence is the
      exit, and a reseat before the removal leaves the seat stopped

  the read-back returns it, and the row the lead gets carries the same order ("Remove the record FIRST, then
  `4genteam room reseat 4genthub-min architect`: a reseat before the removal stops the seat") and the close
  request.
- `go build ./...`, `go vet ./...` and `go test -count=1 ./...` in the client: all pass.

### Not implemented here
- No unpark verb and no ceiling clamp: the owner ruled the exit by hand, and `limits.json` stays the owner's.
  A park still does not clear on the window's own boundary.
