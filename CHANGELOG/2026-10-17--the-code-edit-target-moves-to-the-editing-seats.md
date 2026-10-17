# The code-edit target moves to the editing seats, and a turn is code-edit by any edit call

The report held the room to `code-edit cost % > 30` and scored each turn by its FIRST tool call. Both are
wrong for a gated room, and the architect's ruling measured why
(`seats/architect/RULING-code-edit-share-target.md`, answering the owner via `qitem-20261017065118`).

### Why the 30% room target was retired

Three of the five seats may not edit code and hold **55.1%** of the room's cost; the two editing seats hold
**44.9%** and were at **17.5%** code-edit each. So the room figure caps at 44.9% even if both developers spent
every dollar editing, and reaching a room-wide 30% would have required 67% each while also measuring, gating
and queueing. The same number applied to this room measured the role split, not the effort.

### Changed

- **The target is per EDITING SEAT.** `code-edit cost % > 40` for each seat the room spec declares
  `seat_type: developer` (today `go-dev` and `fe-dev`), each on its own line so one seat cannot hide behind the
  other. The seats are read from the room's `spec.json` — the only place that says who edits — and an
  unreadable spec is reported as a `spec` source, with no editing seats and the room diagnostic still printed.
- **The room's code-edit line is a diagnostic.** It is printed with the new `diag` verdict, which means
  "shown, never gated": `(want diagnostic, not a target)`. A room with one builder per two non-builders can
  always print a low share; that is the design.
- **A turn is code-edit when ANY of its tool calls edits code.** The old first-call rule under-counted every
  edit by the read that found the file, and left 20–33% of each seat's cost in `other`. `turnClasses` returns
  both readings; the report's activity table and the code-edit target use the any-call reading.
- **The first-call reading is kept, for comparison.** `seatStats.CodeEditFirstCallPct`
  (`code_edit_first_call_pct` in `--json`) and one line under the targets print it, and `room metrics` still
  persists the first-call class per turn, so the two readings can be compared for a window before the old
  target is fully retired. Only the code-edit class follows the any-call rule: a turn that reads and then
  writes documentation keeps its first-call class, because widening every class at once would move numbers
  nobody asked to move.
- **The two leak checks are untouched**: `tasks closed after commit` (want 0) and `queue rows/day`
  (want < 300), thresholds and verdicts as they were.

### Measured live, same rail, both binaries (room 4genthub-min, `--since 24h`, 2026-10-17 ~06:58Z)

```
before: FAIL code-edit cost %              14.3  (want > 30)
after:  FAIL code-edit cost % [go-dev]     19.2  (want > 40)
        FAIL code-edit cost % [fe-dev]     16.8  (want > 40)
        diag code-edit cost % [room]       16.2  (want diagnostic, not a target)
        code-edit by FIRST tool call ... 14.3%
```

The first-call figure reproduces the ruling's 14.27% exactly; the any-call reading lifts it to 16.2% — the
size of the under-count the ruling named.

### Not in this change

- `room metrics` event lines keep the first-call `activity` field: the persisted history stays one definition
  until the first-call reading is retired, which is what makes the comparison above possible.
- The reviewer and the lead keep their roles; no seat gained a code-edit target it cannot act on.
