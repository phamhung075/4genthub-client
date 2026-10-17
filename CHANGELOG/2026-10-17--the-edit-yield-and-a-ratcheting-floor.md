# The report pairs the code-edit floor with an edit yield, and the floor ratchets

`code-edit cost %` was a flat number per editing seat, so a floor that a seat cleared once stayed a fixed
target and a floor set above the measured level printed a permanent FAIL. The architect ruled the pair
(`seats/architect/RULING-code-edit-share-target.md`, Addendum 3, `qitem-20261017070315`, carried into task
7527cd73): a ratio that prices landing rather than effort, beside a floor that climbs on evidence.

### Changed

- **The floor ratchets.** `> 25, cap 40` in place of a flat 40: the base is 25, and every complete UTC day on
  which **every** editing seat clears the floor then in force raises it by 5, never down, up to the ceiling 40.
  One failing seat holds it. The line prints the number it compares against, so the target cannot drift out of
  sight of the seats it gates.
- **The step is replayed from the transcripts, not stored.** The report writes nothing, so each run replays the
  last 7 complete UTC days from the seats' own session files. The reading is the **any-call** one the floor
  itself gates, not the first-call `activity` field `room metrics` persists: a ratchet moved by a different
  classifier would step for reasons the target does not measure. Rooms whose days predate a transcript step
  simply do not step - the floor stays at its base, which is the honest first reading.
- **The edit yield, one DIAGNOSTIC line per editing seat**: the commits the gate passed for that seat, over that
  seat's code-edit USD. A commit counts when its `Task:` names a task a gate row passes and its `Seat:` trailer
  names the same seat. A **pass** is a queue row tagged `gate`, in state `done`, whose `done` transition was
  made by the room's reviewer - the reviewer's close is what makes a row a pass - and whose summary or body
  names the task's 8-character id **or a commit sha**. The sha path is not decoration: gate rows here speak in
  tips (`GATE: client 9c9f4f8`, `tip 858abf2`) far more often than in task ids, and the named commit's
  `Task:` trailer names the task.
- **The seat credited is the row's source seat** - the seat that filed the gate is the seat whose work was
  gated. A task the gate passed for another seat credits nobody, and a commit that only moves a submodule pin
  never counts (`git log --raw` mode 160000, so a pin bump is not read as an edit whose author landed work).
- **Drops are printed by reason**: no `Task:` trailer, task not gate-passed, gate passed for another seat,
  pin-only. A numerator of zero can be read off the line instead of being guessed at.
- **The yield's own floor waits for a ruling.** It is diag for 5 daily windows
  (`yieldBaselineDays`), printed with its input commits and USD, never PASS/FAIL. An unread queue or git makes
  it `n/a`, never 0.
- **No wire change beyond `--json`**: new fields `code_edit_floor`, `edit_yield`, `edit_yield_drops`, and the
  per-seat `edit yield (commits/USD)` target lines.

### Fixed with it

`parseGitLog` reads `git log --raw` instead of `--name-only`, so a submodule pointer is visible as a mode
rather than as a file that happens to have no `.md` suffix.

### Measured live (room 4genthub-min, `--since 24h`, 2026-10-17 ~07:20Z, new binary)

```
FAIL code-edit cost % [go-dev]               18.7  (want > 25, cap 40)
FAIL code-edit cost % [fe-dev]               17.1  (want > 25, cap 40)
diag edit yield (commits/USD) [go-dev]        5.9  (want diagnostic for 5 daily windows)
diag edit yield (commits/USD) [fe-dev]        7.9  (want diagnostic for 5 daily windows)
diag code-edit cost % [room]                 16.1  (want diagnostic, not a target)
FAIL tasks closed after commit                3.0  (want 0)

code-edit floor: 25 (+5 per complete UTC day every editing seat passes, never down, cap 40);
                 replayed 7 days, stepped 0
edit yield (gate-passed commits per code-edit USD; diagnostic)
  go-dev       5 commits /    0.8448 USD =  5.9183 per USD
  fe-dev       2 commits /    0.2525 USD =  7.9221 per USD
  dropped: no Task: 16, task not gate-passed 22, gate passed for another seat 11, pin-only 2
```

The floor did not step: on the last seven complete days at least one editing seat sat under 25, so 25 is the
honest reading, and the two seats now fail a floor that is 30% above the 19.2% the ruling used to set it.

### Tests

- `TestGatePassedCommitsCreditOnlyTheSeatTheGatePassed`: a passed commit counts; a row closed by the lead is not
  a pass; a tip-sha row passes through the commit's `Task:`; a task passed for another seat credits nobody; a
  pin-only commit never counts; each drop bucket is counted. Red by deleting the seat join (fe-dev gains a
  commit, `OtherSeat` drops to 0) and by accepting any closer (the lead-closed task becomes passed).
- `TestTheCodeEditFloorRatchetsAndAFailingSeatHoldsIt`: 25 with no records; two passing days step to 35; the
  third day's failing seat holds 35; seven passing days cap at 40; the rendered limits are `> 25, cap 40` and
  `> 30, cap 40`, and a seat just under a stepped floor fails.
- `TestParseGitLogReadsSeatTrailerDocsOnlyAndPinOnly` pins the raw parsing, including a gitlink-only commit.
- The existing diagnostic and independence tests keep their assertions at the new floor.

### Not in this change

- The classifier, the leak checks and the five-window baseline are untouched.
- `room metrics` keeps persisting the first-call class; the floor's replay reads transcripts, and the two are
  deliberately different records.
