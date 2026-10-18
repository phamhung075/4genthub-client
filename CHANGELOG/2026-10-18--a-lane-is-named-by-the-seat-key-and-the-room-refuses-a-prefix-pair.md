## A lane is named by the seat key, and the room refuses a prefix pair

### Changed

- `rust/agenthub-runtime/src/supervisor/mod.rs`: a seat's environment now carries
  `AGENTHUB_LANES_ROOT`, the room's own `wt/`, beside `AGENTHUB_WORKTREES`. A worktree a seat
  opens mid-session at `<roomDir>/wt/<seat>-<lane>` can then be admitted by the name it was
  launched with instead of a list the guard would have to re-read.
- `rust/agenthub-runtime/src/supervisor/mod.rs`: `start_room` refuses a spec where two seat keys
  are equal or one is the other's `-`-prefix (`go` and `go-dev`), naming both, before the room's
  directory is made. Earlier in the same start, `seat_scope`'s launch list already over-matched
  `<seat>-*` for another seat; a room whose keys meet like that has no lane rule it can honour.
- `skills/share/seat-worktree/SKILL.md`: unchanged in this commit, deliberately. The ruling requires the
  lane sentence to ride the SAME commit as the guard edit, and the guard edit is the owner's (below), so
  the sentence is held back rather than shipping text ahead of the guard it describes.

### Tests

- `a_room_whose_seat_keys_share_a_lane_prefix_does_not_start` (new): `start_room` refuses `go` and
  `go-dev`, the refusal names both keys, and no room directory is created.
- `seat_keys_that_share_a_lane_prefix_are_refused_at_room_start` (new): the predicate refuses both
  orders and a duplicate key, and admits `go-dev` beside `go-fs`.
- `a_seat_starts_in_its_own_worktree_and_the_guard_is_given_the_list` now also reads
  `AGENTHUB_LANES_ROOT` out of the pane's environment and requires it to be the room's `wt/`.

### Measured

`worktree-scope` refused 24 calls on 2026-10-18 - go-dev 17, lead 4, architect 2, fe-dev 1 - with
the skill text delivered to every seat (5/5), so a text-only fix was not the lever left.

### Blocked on the owner: the guard's own half

`rust/agenthub-runtime/assets/seat-guard.ts` is protected (`basename === "seat-guard.ts"`), so a
seat may not edit it; the guard refused this seat's edit with `protected-paths`. The verified
patch is `~/.config/4genthub/rooms/4genthub-min/seats/go-dev/seat-guard-lanes.patch`: it reads the
root, admits a first segment equal to the seat key or starting `<seat>-`, and leaves the refusal
byte-identical when the variable is unset. Smoke-tested against a copy: `/work/wt/dev-lane6/x`
allowed, `/work/wt/devv/x` and `/work/wt/other-lane/x` refused, unset refuses the lane too. Until
it is applied, no lane is admitted and the guard is unchanged - so `skills/share/seat-worktree/`'s
rule 4 keeps today's text with it, and the owner's commit carries the replacement sentence.

At this change: `cargo test --offline` in `rust/agenthub-runtime` passes.
