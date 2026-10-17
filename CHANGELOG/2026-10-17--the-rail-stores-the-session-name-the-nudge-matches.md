# The rail stores the session name the nudge pass matches

A queue row addressed by a seat key woke nobody. `4genteam room queue create --to reviewer@<room>` stored
the address exactly as typed, while the runtime's nudge pass compares `destination_session` against the
session names it runs (`agenthub--<room>--<seat>`). Measured on 4genthub-min 2026-10-17: a row to
`reviewer@4genthub-min` sat ten minutes with no nudge attempt while the session-name twin was nudged in a
second; 128 of 1080 rows carried non-session destinations.

`room queue create` now resolves the address before it writes, from the runtime's own reading of the room
(`GetRoomStatus`, which lists every declared seat and its session name):

- **Three spellings name one seat** - the session name, `<seat>@<room>`, and the bare seat key - and all
  three are stored as the session name. The row, a `--destination` filter and the nudge pass therefore read
  one key; there is no second spelling to keep in step.
- **`principal@<room>` stays the owner's address** - the decision skills teach it, and the owner UI reads
  those rows - so it resolves to `agenthub--<room>--principal` without a live seat behind it.
- **A destination naming no seat is refused, exit 2**, naming the value, the room's seats with their
  sessions, and `Nothing was written`. A value carrying another room's name is refused the same way. The
  refusal fires before the create leaves the host, so no row is stored under a destination no nudge can
  match.

The actor check still comes first: a create with `$AGENTHUB_SESSION_NAME` unset asks the daemon nothing.

## Measured

Throwaway root per case (own state dir, socket, daemon and tmux root; an agy-runtime seat whose pane is up
and which publishes no word):

- before, with the client at `52b2977`: the row stored `rev@nudge-lab` (the `--destination
  agenthub--nudge-lab--rev` filter found an empty rail), no `run/rev.nudge.jsonl` after 5.1s, no alert; a
  typo'd `revv@nudge-lab` was accepted as a second row;
- after: the same create resolves to `agenthub--nudge-lab--rev`, the filter matches the row, the nudge
  attempt 1 is recorded at +3.0s with key `rev:<qitem>`, and the status carries
  `alert=seat_up_with_queued_work waiting_qitem_ids=<row>`; the typo is refused with exit 2 and no second
  row.

## Tests

`internal/clientroom` gained the one-nudge-key regression (the three spellings produce an identical
`QueueCreate` frame), the no-seat refusal, the other-room refusal and the principal address, and the
create's existing test now asserts the resolved destination. Overlay check: with the resolver call removed
from `runQueueCreate`, all five fail.
