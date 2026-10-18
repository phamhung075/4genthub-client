## `continue` no longer reads a room with its supervision down as restored

### Fixed

- `internal/clientrestore/restore.go`: after the seats are counted, the room's safeguards are read through
  the same computation `doctor`/`status` use (`clientlifecycle.SafeguardRows`, new exported wrapper over the
  one `rowsFor` seam). A rig whose seats are up while a safeguard is down is logged as
  `seats live but supervision is down (<names>) - not restored; run 4genteam up <rig>` and answers false —
  the verb exits 1 instead of printing `done`. Measured 2026-10-18 on 4genthub-min: `stop` then `continue`
  printed `already live, skipping restore`, counted the seats and ended `done` while agent, compact,
  watchdog, messages, roomguard, budget and bridge were all down; `heal` restored them.
- `internal/clientrestore/continue.go`: `ExitRestoreFailed`'s contract and the verb's help now name the
  third failure (seats up, supervision down) and say that the SEATS are what this verb restores.
- `internal/clientlifecycle/lifecycle.go`: `stop`'s help now says what it does not stop — the seat sessions
  are left standing and the runtime daemon is not a child of agent, so no verb stops it and it serves the
  rooms across a stop (`up` starts the supervision again).

### Testing

- `internal/clientrestore/continue_test.go`, new `TestALiveRigWhoseSupervisionIsDownIsNotRestored`: live
  seats, two safeguards down -> exit 1, the names and `4genteam up rig-a` in the log, no `done` line, and
  the live seat still briefed. Red without the check (`exit = 0, want 1`), green with it. The world's
  default row report is every safeguard up, so the existing live-rig and `--all` cases are unchanged.
- `go test ./... -count=1` in `agenthub_client`: every package passes. Live read on this machine through
  the new wrapper (`SafeguardRows("4genthub-min", root)`): 7 rows, 0 not running.
