## The guard's worktree-scope roots at the seat's own worktrees, and no longer fails open

### Fixed
- `rust/agenthub-runtime/assets/seat-guard.ts`: `worktree-scope` reads the supervisor's `AGENTHUB_WORKTREES` and refuses a write outside it; the early return that skipped the rule when the variable was unset is gone, so a missing variable can no longer turn the rule off. Applied from go-dev's parked patch (sha256 `ad9cd78f…`) on top of the runtime half `f1d5396`.
- `rust/agenthub-runtime/tests/seat_guard.rs`: the cases for the new scope.

### Tested
- `cargo test --offline` in `rust/agenthub-runtime`: 133 unit + 11 `seat_guard` + 1, 0 failed.
