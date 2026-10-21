# The seat's directory is the room's derivation, and a cwd that names anything else is refused

`up` carried each seat's working directory forward from the room's own `spec.json` into a definition that
declared none (`carrySeatCwds`), so a room once started in hand-made directories stayed in them. Measured
2026-10-21 on this room: `spec.json` recorded `lead = /home/daihu/__projects__/4genthub` (the shared
checkout) and `go-dev = wt/go-dev-root`, while `doctor`'s F1 compares each pane's cwd against the worktree
the room DERIVES - `<room>/wt/<key>` - so F1 could never pass for either seat, and `up` kept writing the
mismatch back on every start.

The architect's ruling (`seat-worktrees.md`) is that the derived path is the seat's directory and the shared
checkout takes only the land. `seatCwdsByDerivation` implements it in one place, before the process probe,
the daemon and the first launch:

- a blank `cwd` is **filled** from the derivation, so the spec `up` starts is the spec `spec.json` records
  and the path F1 judges is the path the seat was launched in;
- a `cwd` that names anything else is **refused**, naming the seat, its declared path, the derived one and
  the two ways to settle it (drop the cwd so it is derived, or move what is there).

`carrySeatCwds` is deleted with its comment: the definition is read as written and the record no longer
overrides it.

Verified: `go test ./...` in `agenthub_client` - every package ok, `go vet ./...` clean; `cargo test
--offline` in `rust/agenthub-runtime` - 192 passed, 1 passed, and the `seat_guard` binary's 29 of 30, whose
one red is the rev-9 asset delta waiting on the lead and nothing this change touches.
`TestUpFillsABlankSeatCwdFromTheDerivation` asserts the fill equals `clientservices.DerivedWorktrees` for a
lead, a reviewer and a seat already at its path (trailing separator included);
`TestUpRefusesASeatCwdThatIsNotTheDerivedWorktree` asserts the refusal names the seat, the shared checkout
and the derived path. `TestUpKeepsEachSeatsCwdFromTheRoomsOwnSpec` pinned the carrying and is gone;
`TestUpStartsARoomFromItsOwnSpecWithNoTeamDefinition` now records no cwd and asserts the derived fill
reaches the room start.

Known gap, not fixed here and reported: `--state-root` does not reach the derivation - the room directory
comes from `clientservices.RoomDir` (the default state root), which is the same seam the worktrees step,
`doctor` and the two `worktree` verbs read. Moving it would move all four at once, so it is its own row.
