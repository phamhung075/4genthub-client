# A replaced rail is the rail the daemon uses

The daemon opened each room's rail on first use and held it for the rest of its life —
`Supervisor.queue_stores`, one `QueueStore` per room, never revalidated. The rail is a *file the room owns*,
and it can be replaced while the daemon runs: a rebuilt room, a restore, or an instrument's own hygiene
(`rm -f queue.sqlite`, which the U3 acceptance does between legs) all put a new file at the same path.

## What was wrong

The held connection went on pointing at the deleted inode, so both halves of the room's rail broke and
neither self-healed — only a daemon restart cleared it:

- every **write** was refused with SQLite's `attempt to write a readonly database` — its sentence for a
  database moved out from under an open connection, which reads as a permission problem on a file that is
  writable (`test -w` true, 644 `daihu`, no immutable flag), and
- every **read** kept answering the OLD rows as fact, one line above the error, so the rail reported a
  replaced room's queue as this room's.

R6 then blocked C4: the acceptance's first room leg (A2, the queue flow) does exactly this — it clears
run-state, `up` recreates the rail, and every write after that failed 3 of 3 (`evidence-run4b-A2-*`). Run 4
never reached it because A1's `logs/` assertion starved the leg.

## The change

**Nothing is held.** `RoomSupervisor::queue_store` opens the room's rail for the request and drops it when the
request ends; the cache field, the borrow guard type (`RoomQueueStore`) and `stop_room`'s eviction are gone.
The rail is human-paced — a handful of verbs a minute — and `Connection::open` plus the idempotent schema is
the cheap half of a request, so the cache bought nothing and cost the whole class above. Two things fall out
of removing it: the next request cannot see a file that is no longer there, and a room's descriptors live as
long as its request instead of as long as the daemon has ever touched the room (measured below: 1 held fd
before, 0 after).

The room refusal is unchanged: a room that was never started still gets a refusal naming the room, not an
empty queue.

**On the ruling's second clause — a failure is never a bare SQLite string.** The store's messages already
name what was attempted (`cannot create queue row '<id>': …`), and they are unchanged here: what misled was
not the phrasing but the *attribution*, which came from the handle pointing at a file the room had replaced.
With the rail opened per request, that class cannot reach a write at all — a rail the daemon cannot use
fails in `QueueStore::open`, whose message already names the file — and a genuine permission or lock failure
now names the file it is really about, because the file is the one at the room's path.

## Evidence

**The live measurement, one client, two daemons, the same steps** (`u3bin/4genteam` driving both; isolated
state dir and socket each; the rail moved to `rooms/smoke/replaced/queue.sqlite` under the running daemon):

| step | before (`u3bin` daemon) | after (this branch) |
| --- | --- | --- |
| `room queue create` (rail in place) | 0 | 0 |
| `room queue create` (rail replaced) | **2** `cannot create queue row '…': attempt to write a readonly database` | **0**, the row lands in the new file |
| `room queue list` | the **old** row, as fact | the **new** row only |
| rail back at its path | no | yes |
| daemon fds pointing at a rail | **1** | **0** |
| rows in the moved file | `before the rail was replaced` | `before the rail was replaced` (the write did not go there) |

**The unit test, same shape at the supervisor level**: `a_replaced_rail_is_the_one_the_next_verb_uses` moves
the rail into a room-shaped directory of its own (so the moved file can be read back through the same
`QueueStore::open`), then asserts the next write is *in the new file* — the moved file keeps only the row it
already had — and that a read stops reporting the old rows as this room's. `cargo test --offline`: **87
passed, 0 failed**.

- `rustfmt --check` has no objection to the added lines (the file's 35 pre-existing hunks are elsewhere);
  `cargo build --offline` is clean apart from the crate's 24 pre-existing warnings.
- Not verified here: the acceptance's own A2 leg re-run. That needs a runtime binary built from this branch
  (this branch's `target/debug/agenthub-runtime` is what the table's "after" column used) and an idle
  harness, which is the lead's and the architect's to sequence.
