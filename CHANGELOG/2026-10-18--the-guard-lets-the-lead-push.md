## The lead pushes, and no other seat: the git guard and the seat tool guard admit one seat

Owner ruling, 2026-10-18: the lead commits, pushes and rebuilds after the reviewer gate and a green test
run; no other seat pushes. In this repo the exception is the seat key `PushAuthoritySeat` (`lead`),
decided from the session a runtime sets, never from a path or a room.

### Changed

- `internal/clientguard/guard.go`: `Facts.pushes()` - a session present and its seat the lead - skips
  the pre-push refusal from the checkout and from a linked worktree, and skips the shared-checkout
  commit refusal (the checkout is where the lead merges and commits `main`). `SessionFromEnv` now reads
  both spellings: `OPENRIG_SESSION_NAME` (`<room>-<seat>@<room>`) and the canonical
  `AGENTHUB_SESSION_NAME` (`agenthub--<room>--<seat>`), each parsed with the same non-empty, no-space
  shape check. The push refusal names the new route: "only the lead pushes", "Hand the commit hash to
  the lead".
- `rust/agenthub-runtime/assets/seat-guard.ts`: the `push` rule admits the lead seat - the exported
  `AGENTHUB_SEAT` key, or either session spelling naming `lead` - and keeps the refusal, now worded
  "only the lead pushes it", for every other seat. `sessionSeat` mirrors the Go parser. The guard's
  other rules stand for the lead too: a force push is still destructive git, and the hook-bypass rule is
  unchanged.

### Tests

- `guard_test.go`: `TestTheLeadsPushIsNotRefusedInTheCheckoutOrAWorktree`,
  `TestAnotherSeatIsStillRefusedWhenTheLeadIsExempt` (`lead-dev`, `lead2`, empty),
  `TestTheLeadsCommitInTheSharedCheckoutIsNotRefused`, `TestTheCanonicalSessionNameIsRead` (both
  spellings, and five names that name no seat); the existing refusal case asserts the lead's words.
- `guard_e2e_test.go`: `TestTheLeadsPushAndCommitGoThrough` - real repositories and the installed hooks:
  the lead's push from a linked worktree and its commit plus push in the checkout go through, and the
  remote's `main` advances to the lead's commit. The e2e fixture strips both session variables.
- `rust/agenthub-runtime/tests/seat_guard.rs`: `the_lead_pushes_and_no_other_seat_does` runs the guard as
  agy does - the lead passes under both spellings and under the exported seat key; `dev`,
  `lead-dev`, `lead2` and no session are still refused by the `push` rule; and the lead's force push is
  still refused by `git-destructive`.

### Measured

`go test ./...` -> all 35 packages ok; `cargo test --offline` in `rust/agenthub-runtime` -> 184 + 1 + 19
+ 1 passed, `--test seat_guard` -> 19 passed.
