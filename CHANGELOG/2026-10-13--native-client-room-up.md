## `up` reads the room before it starts it, and the client gains a room-scoped verb group

### Added
- `internal/clientroom/room.go` — `4genteam room status|stop|reseat|compact`, the room-scoped frame over the
  runtime's IPC. Every sub-verb prints the pair it ran against (`room: <room>@<state-dir> socket: <socket>`)
  because neither is resolved implicitly: the state dir is the flag, else `$OPENRIG_STATE_ROOT`, else this
  machine's own; the socket is `--socket`, else `$OPENRIG_RUNTIME_SOCKET`, else this machine's own.
  `stop` is `StopRoom` and nothing else — no tmux, no rigd, no bridge, no `~/.openrig` — and the room's
  directory (spec.json, seats/, logs/) is left in place so the room can be started again. `reseat` is
  `StopSeat` then `RestartSeat`, in that order. `compact` asks the runtime once per RUNNING seat (the
  protocol's `ForceCompact` is session-scoped, so the session name is built in one named helper), refuses a
  stopped seat BY NAME instead of reporting a compact that did not happen, and repeats a runtime refusal in
  the runtime's own words; both refusals exit 2.
- The room contract's exit codes, named in `internal/clientcmd`: `ExitPartialStart` (2) for "the daemon
  started but a seat did not" and `ExitRoomRefused` (3) for a usage error or a room the daemon does not
  know. They share `ExitUsage`'s and `ExitUnavailable`'s values deliberately; they are named so that no room
  verb has to borrow a name that means something else.

### Changed
- `up` is ROOM-SCOPED: `upCommand.NeedsRig` is false, so the dispatcher hands it a nil rig. The room is the
  positional and its seats come from the LOCAL team definition, so starting them needs nothing resolved from
  OpenRig — requiring one is what made the native start path depend on the CLI exec it replaces. The two
  places that still reach for a rig take nil: the peak gate's fail-closed activity read already did, and
  `openUIImpl` now resolves the tool itself (a third that dereferenced it would panic on every `up`).
- `up` PRINTS the state dir and the socket before it touches the room, and it CHECKS BEFORE IT STARTS.
  `seatStartPlan` sends the whole definition only when the daemon does not know the room or when every seat
  it reports is stopped — nothing is running, so nothing can be cycled — and otherwise starts the seats that
  are not running one at a time with `RestartSeat`. The stake is the runtime's own semantics: `StartRoom`
  kills and re-creates a session it finds running (`pty::launch_session`), so a blind start would cycle live
  agents and move every `started_at` on a second `up`.
- `up`'s usage and unknown-room failures exit 3, not the CLI-wide `ExitUsage` (2), because 2 is what a
  PARTIAL START is for this verb (design §2.5). Every other lifecycle verb keeps 2 for its own usage errors.

### Verified
- `TestSeatStartPlan` pins the check-then-start decision (a room the daemon does not know, a reading with no
  seats, every seat stopped, every seat running, one seat down, and the runtime's own `Running` spelling).
  Three existing tests moved to the new codes: `TestEveryLifecycleVerbRequiresARig` (`up` alone is 3),
  `TestUpRefusesAFlagItDoesNotTake`, `TestUpRefusesADefinitionThatNamesAnotherRoom`.
- `gofmt` clean, `go build ./...` exit 0, `go vet ./internal/clientlifecycle/` exit 0,
  `go test ./internal/clientlifecycle/ -count=1` → `ok 0.053s`.
- Forced end-to-end through the REAL binary against a stub daemon that speaks the client's own line envelope
  (`{"action":…,"payload":…}\n` / `{"ok":true,"data":…}\n`), with `OPENRIG_RUNTIME_SOCKET` pointing at it so
  nothing live was touched:
  - all seats running → exit 0 and the wire carries **no** `StartRoom` and **no** `RestartSeat` (three
    `GetRoomStatus` only), which is the design's "a second `up` restarts nothing";
  - one seat stopped → exit 0 with exactly ONE `RestartSeat` for that seat and no request for its running
    siblings;
  - a seat that never comes back → exit 2, the seat named, and the daemon's log path;
  - a room the daemon does not know → `StartRoom` with the whole definition, exit 0 when it starts, exit 3
    when the daemon refuses the definition;
  - `up` with no `--team`, and with a flag it does not take → exit 3;
  - `room status` (pair + one line per seat), `room reseat` (`StopSeat` then `RestartSeat` on the wire),
    `room compact` (three `ForceCompact` for three running seats; with one stopped, `ForceCompact` for the
    two running seats only, the stopped one refused by name, exit 2) and `room stop` (`StopRoom` alone).
