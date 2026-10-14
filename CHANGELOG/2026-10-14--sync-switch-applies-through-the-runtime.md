## `sync switch` applies a seat's model through the runtime, not `rig seat` (C3)

### What was wrong

`4genteam sync switch ROOM SEAT --model M [--apply set-model|restart]` still ran `rig seat set-model`,
`rig seat stop` and `rig seat launch`, so the verb stopped working the day OpenRig is retired. The runtime had
`StopSeat` and `RestartSeat` but no way to record a seat's model and no way to restart without the old agent
session.

### Changed

- `rust/agenthub-runtime`: new IPC action `SetSeatModel {room, seat, model}` writes the model into the room's
  own `spec.json` (applies at the seat's next start; an empty model or an unknown seat is refused).
  `RestartSeat` takes an optional `fresh` (default false, so the wire older callers write is unchanged) that
  removes the seat's `sessions/` before the start.
- `internal/clientruntime/ipc.go`: `Client.SetSeatModel` and `Client.RestartSeatFresh`.
- `internal/clientsync/switchverb.go`: `switchApplyToRuntime` replaces `switchApplyToRig`; the `rig` exec, the
  `rig not found` sentinel and the node-record read for the session name are deleted. The runtime keys a seat
  by room and seat key, so no session name is needed. A runtime that is not running, or a room that does not
  list the seat, is a note with `applied=none` and exit 0 (the cloud change is already recorded); any other
  refusal is exit 3 naming the step. `--reason` still goes to the cloud; the runtime has no reason field.
- `--apply set-model` now says the model applies at the seat's next start.

### Tested

- `cargo test --offline`: 67 passed (2 new: the model is persisted and the other fields untouched; the clearing
  that a fresh restart does drops one seat's saved sessions, leaves another's, and tolerates none; it is tested
  apart from the tmux stop, whose process-wide `TMUX_TMPDIR` made the first version flaky).
- `go test ./internal/clientsync ./internal/clientruntime`: the switch tests now drive a runtime stub; new
  tests pin the absent-runtime, unknown-seat and failed-call branches and the three wire frames.
