# A runner-less seat is live by its pane, and its launch writes the record only the pane can back

Ruling `seats/architect/RULING-agy-nudge-and-state.md` (option A), row `qitem-20261017070855-2bebb1c1c977`.
Measured on 4genthub-min 2026-10-17: the agy reviewer held a gate row from 07:09:52Z and the daemon's own
`GetRoomStatus` answered `running` with `alert=seat_up_with_queued_work waiting_qitem_ids=[…]`, yet
`reviewer.nudge.jsonl` held no record after 03:53:33Z while other seats were nudged at 07:10:56Z and
07:11:23Z. A direct `4genteam send` reached the same seat, so the pane was live and the pass alone was not
considering it.

## What the exclusion actually was

The deployed binary (`/home/daihu/.local/bin/agenthub-runtime`, built 01:01Z from `a2c1df3`) gates the pass
on `seat_is_idle(&room, seat)` - the word in the seat's state file. The reviewer's file said
`agent_socket_closed` (written 04:00:36Z, before the 07:02:33Z pane), which is not `idle`, so the seat was
never a candidate. Main's busy-word gate (`the nudge pass wakes a seat that said nothing`) had already
replaced that predicate; the deployed daemon predates that commit, which is why the daemon's status said
running while its pass excluded the seat.

RED-checked, and not what the short reading of the ruling suggested: restoring just the `state != Running`
line does **not** exclude a runner-less seat on this base - its derivation answers `Running` for every
non-death word, and for a death word too (the death arms require `publishes_readiness`). The stored
document, not the derived state, was the exclusion.

## Changed

- **`pending_nudges` decides the gate by runtime.** The alert is the trigger for every seat. A publishing
  seat (`has_runner`) keeps the `Running` gate and the `working`/`needs_input` busy words unchanged; a
  runner-less seat (`agy`, `codex`, a custom command) is a candidate when its pane is live - its state file
  may be absent or hold a previous generation's word, and neither silences it now.
- **`TerminalManager::pane_is_live`** is the session **and** `#{pane_dead}` = 0. `pane_dead` is the only
  field that answers the second half: measured here 2026-10-17, a pane kept by `remain-on-exit` after its
  command exited still answers `has-session` 0 and still reports the dead process's pid (`118226 1`), so
  neither the session nor a nonzero pid can tell it from a live one. A probe that FAILED is not a death
  (`dce05c8`'s rule for the pid): only tmux reporting the pane dead excludes.
- **A runner-less launch writes the record it can back** (`start_seat`): `state: running`,
  `activity: unknown` (`running` is not a turn word), the classification sentence naming the missing
  readiness channel, the PANE's pid, and a fresh `updated_at`. Measured before this change: the watch and
  the room report kept showing the 04:00:36Z death word for a seat whose pane was created 3h later.
  Publishing runtimes are untouched - their runner owns the document, and a launch record for them would
  claim a handshake it has not made (the `Starting` arm exists for exactly that).
- **`runner::write_state_with_pid`**, with `write_state` delegating to it. The document's `pid` is now the
  caller's, so a claim names the process that can still update it: the runner's own for a publishing seat,
  the pane's for a launch that has none.

## Testing

- `a_runner_less_seats_liveness_is_its_pane_whatever_the_word_in_the_file`: the death words
  (`agent_socket_closed`, `agent_exited`, `stopped`), `idle`, and no file at all, over one live pane - a
  candidate in every case. Red against the deployed predicate (`activity == idle`): the test failed on the
  measured `agent_socket_closed`.
- `a_runner_less_seat_whose_pane_is_dead_is_not_a_candidate`: `remain-on-exit`, the command exits, the
  session and its pid survive; the pass skips it. Red with the pane check disabled.
- `a_runner_less_seat_with_nothing_owed_is_not_nudged`: a live pane with no row is not a candidate.
- `a_runner_less_launch_writes_a_state_document_the_pane_can_back`: a stale `agent_socket_closed` document,
  then `start_seat` writes `running`/`unknown`, the pane's pid, and a stamp within the launch. Red with the
  write disabled.
- At this change: `cargo test` in `rust/agenthub-runtime` = 164 + 15 + 1 + 1 passed, 0 failed. The built
  daemon's launch write and the pane gate are exercised through `start_seat` and `pending_nudges` on scratch
  tmux servers; the live proof of the gate on the room's own daemon needs the install below.

## What this does not do

- It does not install the daemon. The live exclusion is lifted on the deployed host by the build that
  carries the busy-word gate (and this one), so the owner's install is the live proof.
- It does not fix the metrics half named in the ruling: the reviewer is still priced at 0 turns because the
  activity and transcript readers are omp-shaped. Option C - an adapter that publishes a word for a
  runner-less runtime - remains the model fix that would make the pane exception unnecessary.
- No change to the send path, the rail, the nudge ladder, or the row caps.
