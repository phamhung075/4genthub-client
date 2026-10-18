## The overlap detector and the stuck/idle alerts ride agent's loop

Plan items 4 and 5, ported onto main's rewritten supervisor (`agent.go` is what the predecessor called `rigd`).

### Changed

- `internal/clientlifecycle/overlap.go` (new): every ten minutes, and on request, compares each seat branch
  with the room's base and reports a file two seats have changed (the branch's files since the base plus the
  uncommitted ones) and a branch left with unmerged commits. One report per file-and-seat-set per hour,
  remembered under the state root; a dry run sends and remembers nothing.
- `internal/clientlifecycle/stuck.go` (new): reports a queue row in progress whose owning seat has no live
  session, a board task past its own due date that names a live seat of the room, and a seat whose session log
  has not been written for the idle window. The first alert goes to the finding's seat (when it has a live
  session, with the lead) and the lead; a finding still standing an hour later is written to the room's human.
- `internal/clientlifecycle/agent.go`: `agentManager` gains `lastOverlap`/`lastStuck` and `pass()` runs both
  scans on their own timers, for 7.1's reason - a second always-on process is a second thing to supervise.
- `internal/clientlifecycle/lifecycle.go`: both verbs are registered.
- `internal/clientteam/roomspec.go`: `RoomBaseBranch` reads `room.base_branch` through the same `loadTeam`
  the rest of the client uses, so the detector cannot disagree with `up` about the room's definition.

### Reconciled rather than cherry-picked

- **Worktrees**: the branch's `clientservices.SeatWorktree` does not exist on main; the one rule is
  `DerivedWorktrees`, so the scan reads the path and branch from that list and skips - and names - a seat the
  room derives no entry for.
- **The rail**: `stuckQueueRows` reads `clientruntime.QueueList`, not the retired `rig queue list --full`, and
  `stuckEscalateRow` writes through `clientruntime.QueueCreate`, not `rig queue create`. Two consequences are
  stated rather than papered over: the daemon's row carries **no closure deadline and no claim time**, so the
  detector's one provable queue condition is a gone owner (an age threshold was measured false before the
  branch dropped it); and a create must be the four-line body with a tag from the daemon's own vocabulary, so
  the escalation row is written in that grammar and tagged `handoff`, with "Stuck escalation in <room>:" as
  its summary.
- **Destinations**: a row's `destinationSession` is the runtime's own `agenthub--<room>--<seat>`; the scan
  reads that form as well as the canonical `<room>-<seat>@<room>` through `clientruntime.ParseSeatSessionName`,
  because a detector that knew only one would call every row ownerless.
- **Owner node**: main has no owner-node predicate, so the idle scan skips the member the ruling names
  (`owner`) rather than inventing a runtime rule; a node keeps no session log, so left in it would be skipped
  and named on every pass.
- **Sends**: `rigSay` on main takes four arguments (no outcome enum); both detectors use it unchanged.

### Smoke

Built binary against the live room with the room's team file: `overlap 4genthub-min --dry-run` names the five
seats whose worktrees this machine does not have at the derived paths (and sends nothing); `stuck 4genthub-min
--dry-run` reports 0 findings, and with `--idle-after 0s` lists the five live seats as idle while the owner
node is not among them. The escalation writer's live round trip is not smoke-tested (it would write to the
real rail); it is covered by the fixture's seam and the daemon's own create validation.

At this change: `go build ./...`, `go vet ./...`, `go test ./...` pass (36 packages).
