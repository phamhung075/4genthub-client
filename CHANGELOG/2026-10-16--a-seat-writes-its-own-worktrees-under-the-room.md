# A seat may write its own worktrees under the room's wt/ directory

### Fixed
- `rust/agenthub-runtime/assets/seat-guard.ts`: `worktree-scope` roots at the directory the seat starts in, and every seat starts at `.`, so a seat's worktrees under `<room>/wt/` (for example `go-dev-client`) were refused. The rule now also allows `<room>/wt/<seat>` and `<room>/wt/<seat>-<repo>`, and no other seat's worktree.

### Testing
- `rust/agenthub-runtime/tests/seat_guard.rs`: `a_seat_writes_its_own_worktrees_under_the_room_and_no_other_seats` allows `dev`, `dev-client`, `dev-root` and refuses `other` and `device`. It fails with the previous guard.
- `cargo test --offline`: 130 unit + 10 `seat_guard` + 1 other, 0 failed.
