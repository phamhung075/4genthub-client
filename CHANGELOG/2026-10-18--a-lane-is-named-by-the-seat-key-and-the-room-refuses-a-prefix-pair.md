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
- `rust/agenthub-runtime/assets/seat-guard.ts`: `worktree-scope` admits a write whose first segment
  under `AGENTHUB_LANES_ROOT` is `<seat>` or starts `<seat>-`, so a lane opened mid-session is the
  seat's own; another seat's lane, a name that only starts with the key (`devv`), the room root and
  the shared checkout stay refused. With the variable unset the rule and its refusal text are
  today's, byte for byte.
- `skills/share/seat-worktree/SKILL.md`: rule 4 is the ruling's sentence - the lane is
  `<room>/wt/<seat>-<name>`, allowed by the prefix the seat was launched with; any other path is
  refused. It rides this commit because the text and the guard it describes must ship together.

### Tests

- `a_room_whose_seat_keys_share_a_lane_prefix_does_not_start` (new): `start_room` refuses `go` and
  `go-dev`, the refusal names both keys, and no room directory is created.
- `seat_keys_that_share_a_lane_prefix_are_refused_at_room_start` (new): the predicate refuses both
  orders and a duplicate key, and admits `go-dev` beside `go-fs`.
- `a_seat_starts_in_its_own_worktree_and_the_guard_is_given_the_list` now also reads
  `AGENTHUB_LANES_ROOT` out of the pane's environment and requires it to be the room's `wt/`.
- `a_lane_is_admitted_by_the_seat_key_and_only_that_prefix` (new): with `AGENTHUB_LANES_ROOT=/work/wt`
  and the seat `dev`, `/work/wt/dev-lane6/x` and `/work/wt/dev/x` are allowed while `/work/wt/devv/x`,
  `/work/wt/other-lane/x` and `/work/x` are refused as `worktree-scope`; with the variable absent the
  lane is refused and the reason string equals today's, byte for byte.

### Measured

`worktree-scope` refused 24 calls on 2026-10-18 - go-dev 17, lead 4, architect 2, fe-dev 1 - with
the skill text delivered to every seat (5/5), so a text-only fix was not the lever left.

### The guard's own half, in the owner's commit

`rust/agenthub-runtime/assets/seat-guard.ts` is protected (`basename === "seat-guard.ts"`), so the
seat that built this change could not edit it: the guard refused with `protected-paths`, and the
edit was not routed around. The guard rule, the rule-4 sentence, the `seat_guard` cases and this
entry therefore ship together in the owner's commit, which is the one commit the ruling asks for.

At this change: `cargo test --offline` in `rust/agenthub-runtime` passes, and `cargo test --offline --test seat_guard` passes with `a_lane_is_admitted_by_the_seat_key_and_only_that_prefix` among its cases.
