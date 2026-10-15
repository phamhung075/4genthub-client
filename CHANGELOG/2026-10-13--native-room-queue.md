## The native runtime's room queue rail, and the panic that stopped the runtime starting at all

### Added
- `rust/agenthub-runtime/src/queue/mod.rs` — the room's durable rail: `QueueStore` over
  `<state-dir>/rooms/<room>/queue.sqlite` (schema `queue_rows` + append-only `queue_transitions`).
  `create`, `list`, `show`, `update`, `claim`, `close` each RE-READ the row they answer with, so what a
  caller is told is what the next read shows. The state set `{pending, in-progress, blocked, handed-off,
  done}` and the closure-reason set `{handed_off_to, blocked_on, denied, canceled, no-follow-on,
  escalation, superseded}` are preserved from the 4genteam table, as are `chain_of_record`, `reply_to`,
  the transitions table (who/when/from/to/note) and the wake fields (`blocked_on`,
  `wake_after_seconds`). Validation names the offending value: an unknown state or closure reason lists
  the valid ones, an unknown id names itself. A note with no state is a state-preserving append. A
  caller-supplied `qitem_id` is retry identity: a create retried after an unknown outcome lands once.
  The human-notification, identity-provenance and bridge columns are deliberately not ported.
- `RoomSupervisor::queue_store(room)` — one store per room, opened on the room's first queue verb and
  held by the daemon for the room's life (`RoomQueueStore`, a `Deref` guard over the cache), dropped by
  `stop_room`. A room that was never started has no directory and no rail, so a queue verb against it is
  refused **naming the room**, not answered with an empty list — the same untrue answer as a bare `[]`.
  A *stopped* room keeps its directory and its rows and stays readable.
- The IPC surface routes the six queue verbs (`QueueCreate|List|Show|Update|Claim|Close`, tagged
  `action`/`payload`) to that store under `rust/agenthub-runtime/src/ipc/mod.rs`.

### Fixed
- `agenthub-runtime` panicked on **every** invocation: `-s` was claimed by both `--state-dir` and
  `--socket`, which clap rejects before any subcommand is parsed — so `daemon`, `whoami`, `send`,
  `status` and `compact` were all dead, and the shim's own start
  (`internal/clientruntime/ipc.go:61`: `agenthub-runtime --socket <path> daemon`) could not bring the
  runtime up. `--socket` is long-form only now; `-s` stays with `--state-dir`.

### Changed
- The runtime crate's locks are `parking_lot` (the three in `rust/agenthub-runtime/src/supervisor/mod.rs`);
  the crate already resolved `parking_lot 0.12.5` transitively, so the dependency is now direct.

### Testing
- `cargo build` and `cargo test` in `rust/agenthub-runtime`: 19 passed, 0 failed (13 of them the queue
  store's own, including `second_open_on_the_same_room_sees_the_first_stores_rows`).
- The real path was smoked over the IPC socket against a scratch state dir: `QueueList` on a fresh room
  answers `[]`; `QueueCreate` → `QueueShow` → `QueueList` return the same row with its transitions;
  `QueueClaim` moves it to `in-progress`; `QueueClose` with an unknown reason is refused and lists the
  valid reasons; a room that was never started is refused by name; a second connection sees the first
  connection's write; and after the daemon is restarted the row is still `done` with its three
  transitions, read from `rooms/roomA/queue.sqlite` (36 864 B).
- The design's own four-hop flow ran over the socket: lead creates (`pending`) → worker claims
  (`in-progress`) → worker closes `handed_off_to` the reviewer (source rests `handed-off`, and the same
  transaction creates the reviewer's `pending` successor whose `chain_of_record` is the source id and
  whose first transition reads `handoff from <source>`) → the reviewer closes **its own row**
  `no-follow-on` (`done`). Re-closing the source is refused: `queue row '<id>' is already in terminal
  state 'handed-off'`.

### Fixed (the store's first close semantics were wrong)
- `close(closure_reason='handed_off_to', closure_target=T)` set the source row to **`done`** and appended a
  synthetic second transition, so the receiver's row never existed and the row the receiver was supposed
  to close was already terminal — the exact incoherence the four-hop acceptance exists to catch. It now
  follows the daemon it ports (`queue-repository.js:1263-1300`): the source rests at `handed-off` with a
  single `current -> handed-off` transition, and the successor row is created in the same transaction
  (`chain_of_record` extended, `summary`/`body`/`priority`/`tier`/`tags` carried, its own first transition
  `handoff from <source>`). The one deliberate difference from the daemon is the successor's `summary`:
  the daemon nulls it because a *human*-routed successor must author its own, and this rail has no human
  routes, so an empty summary would only produce a row nobody can read.
- `close` had no terminal guard, so closing an already-`done` row succeeded. It now refuses a row in
  `TERMINAL_STATES` (`done`, `handed-off` — `queue-repository.js:38`) naming the state, as the daemon's
  `qitem_already_terminal` does.
