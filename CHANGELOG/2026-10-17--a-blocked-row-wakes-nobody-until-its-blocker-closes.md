# A blocked row wakes nobody until its blocker closes

The rail's `active_only` set is `pending`, `in-progress`, `blocked`, and the join that feeds the seat's
queued-work alert and the nudge pass read that whole set as "work waiting for this seat". A row set
`blocked` on a row that had not closed therefore woke its own destination: measured on 4genthub-min
2026-10-17, assignment row `qitem-20261017063250-0de6c9976232` was blocked at 06:37:02Z on an open gate
row, and `go-dev.nudge.jsonl` recorded attempts 2 and 3 at 06:37:06Z and 06:37:13Z for it while nothing
was callable. The same join put the room's two hand-parked owner rows into the lead's alert
(`qitem-20261017030506-68e83552c3b4`, `qitem-20261017023905-7e2b46059c7c`).

### Changed

- **`blocked` is not "work waiting for the destination".** `waiting_rows` (the one join behind both the
  status alert and `pending_nudges`) filters blocked rows out. They stay in the rail's active set - listable
  with `--state blocked`, claimable, closable - because the state still explains the row; it just is not
  work the seat can pick up.
- **A blocker's close is the wake.** `queue::unblock_dependents` runs in the closing transaction: every row
  whose `blocked_on` is exactly that qitem id returns to `pending` with `blocked_on` cleared and ONE
  transition naming the closure. It runs on both roads to terminal - `close` and `update --state done` -
  so the wake cannot be missed by choosing the other verb. The nudge pass then sees a pending row and
  nudges it once.

### Ruled here (the questions the fix had to answer, not leave implied)

- **A row blocked with no `blocked_on` (a seat parked by hand) wakes nobody and nothing wakes it.** No row
  on the rail says when it should start, so an automatic wake would be a second rule invented by the
  daemon. It stays visible in `--state blocked` until a seat moves it back with
  `room queue update <id> --state pending` (or closes it). The two owner rows above are exactly this case:
  they are the owner's to release, and the alert was never the mechanism that made them act.
- **A free-text `blocked_on` is a label, not a dependency.** `"review"`, `"ci green"` name no qitem id, so
  `unblock_dependents` cannot match them; such a row wakes only when a seat releases it. Only an exact
  qitem id is a wake edge.
- **`in-progress` is unchanged: it still counts as work for the seat.** A claimed row is in the seat's
  hands, which is what the alert is for; it is not waiting on anything else.

### Checked live (same state dir, read-only, old binary vs this build)

- Before, installed `~/.local/bin/agenthub-runtime` (built 01:01Z): the `status 4genthub-min --json` alert
  for `lead` named both blocked owner rows.
- After, this branch's binary, same command, same live rail: `lead`'s alert is gone; `go-dev` keeps its two
  `in-progress` rows and `fe-dev` its claimed one - work actually held by those seats.
- Nudge log, the measured lines: `rooms/4genthub-min/run/go-dev.nudge.jsonl`, one pile key
  `go-dev:qitem-20261017063250-0de6c9976232`, attempts 1/2/3 at 06:36:46Z, 06:37:06Z, 06:37:13Z. Attempt 1
  predates the block; 2 and 3 are the defect this change removes.
