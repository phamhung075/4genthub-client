# The rail stores the session name the nudge pass matches

A queue row addressed by a seat key woke nobody. `4genteam room queue create --to reviewer@<room>` stored
the address exactly as typed, while the runtime's alert join and nudge pass compare `destination_session`
with the names of a seat. Measured on 4genthub-min 2026-10-17: a row to `reviewer@4genthub-min` sat ten
minutes with no nudge while the session-name twin woke the seat in a second; 128 of 1080 rows carried
non-session destinations. The reviewer's own audit counted the shapes: `principal@<room>` 68,
`lead@<room>` 28, `go-dev@<room>` 10, `reviewer@<room>` 4, `architect@<room>` 2.

## Changed

- **`room create` resolves the destination at WRITE TIME, in the runtime**
  (`RoomSupervisor::resolve_queue_destination`, called from the `QueueCreate` handler). The accepted
  spellings - the session name (`agenthub--<room>--<seat>`), the rig orchestrator form
  (`<room>-<seat>@<room>`, which the session list calls canonical), `<seat>@<room>`, and the bare seat key -
  all store the session name, so the row, a `--destination` filter, the alert and the nudge pass read one
  key. It lives in the daemon rather than the CLI because the store is where every writer passes
  (`budgetguard` writes rows too) and because a refused create must exit 2 as an operation the daemon
  refused, which is the room-scoped contract's number for it.
- **A destination naming no seat is refused by name**, exit 2, with the room's seats and their sessions,
  the accepted forms, and `Nothing was written`. A value carrying another room's name is refused the same
  way. There is no exemption: `principal@<room>` names no seat of a room that declares none, so it refuses
  like any other unknown address.
- **`addressed_names` gains the fourth spelling** (`<seat>@<room>`), so the rows already on the rail in it -
  the owner asks among them - alert and nudge instead of sitting silent behind the fix.
- **The two decision skills stop teaching `principal@<room>`.** The owner step files its `owner-decision`
  row to the room's lead (`--to agenthub--<room>--lead`), the seat that carries an owner ask to the owner,
  and both skills state that a destination naming no seat is refused rather than stored. The earlier
  entry's claim that `principal@<room>` "is the principal session's address" is superseded by this one.

## Measured

Throwaway root per case (own state dir, socket, daemon and tmux root; an agy-runtime seat whose pane is up
and which publishes no word):

- before, with the client at `52b2977`: `--to rev@nudge-lab` stored the literal (a `--destination
  agenthub--nudge-lab--rev` filter found an empty rail), no `run/rev.nudge.jsonl` after 5.1s, no alert; a
  typo'd `revv@nudge-lab` was accepted as a second row;
- after: the same create stores `agenthub--nudge-lab--rev`, the filter matches, nudge attempt 1 is recorded
  at +3.0s with key `rev:<qitem>`, and the status carries
  `alert=seat_up_with_queued_work waiting_qitem_ids=<row>`; `revv@nudge-lab` and `principal@nudge-lab` both
  exit 2, name the value and the seats, and write nothing.

## Tests

The runtime gained `a_queue_destination_resolves_to_the_session_name_or_refuses_by_name` (four spellings to
one session name; the unknown seat, the principal and the other room each refused by name) and
`waiting_rows_match_every_spelling_the_rail_has_used` now carries the `seat@room` row. The client's create
test pins the frame: the CLI sends the address as typed and the daemon resolves it, so an older client
against this daemon gets the same resolution.
