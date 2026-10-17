# heal can start the safeguards: up takes a room's own record

## Fixed

- `internal/clientlifecycle/lifecycle.go`: `up RIG` no longer requires `--team`. The room's seats come from
  `--team DIR` when it is given (the local definition that CREATES the room), and otherwise from the room's
  own `spec.json` - the record the runtime wrote from that definition when it last started the room. One
  rule, not a second flag path: `--team` still creates and edits, the record starts.
- `internal/clientteam/roomspec.go`: `LoadLiveRoomSpec` reads that record into the same
  `clientruntime.RoomSpec` a definition yields, and refuses a missing, unreadable, malformed or seatless file
  with the `--team DIR` hint - a room with no record cannot be invented.
- `internal/clientheal/command.go`: heal's safeguard repair (`machineDeps.Up`) now passes
  `--state-root <heal's root>` to the `up` it execs, so the repair starts the room in the root that was
  diagnosed instead of whatever root the child would resolve on its own.

## Evidence

- Red first: `TestUpStartsARoomFromItsOwnSpecWithNoTeamDefinition` (internal/clientlifecycle) failed
  `exit 3 (stderr "4genteam up: --team DIR is required ...")` - the exact refusal heal's `up ROOM` hit.
- After the fix that test passes: the spec handed to the room start carries the live seats with their cwds
  and links.
- Live: the real `~/.config/4genthub` record for `4genthub-min` loads as 5 seats, each with its own cwd
  (lead, go-dev, fe-dev, reviewer, architect).
- The built binary: `up my-rig --state-root <empty dir> --room-only` now refuses with "room my-rig has no
  record to start from ... A room's first start needs --team DIR" (exit 3), not the old mandatory-flag
  message; `up --help` reads `usage: 4genteam up RIG [--team DIR] ...`.
- `go test ./...` in the client: pass.

Owning task: 8634eb38 (client bug 2/3).
