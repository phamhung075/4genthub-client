# The guard refuses a seat editing the config and the log it rests on

### Added
- `rust/agenthub-runtime/assets/seat-guard.ts`: `protected-paths` now refuses a write to the two files the guard rides inside the seat's OWN directory as well - the omp config its deny patterns are rendered into (`4genteam policy apply`, header "do not edit") and the log its refusals are recorded in (`tool-calls.jsonl`). `worktree-scope` allows a seat its own directory, so both were writable: a seat could delete its own deny patterns, or rewrite its own audit trail, without touching the guard. They are named by DIRECTORY and not by file name, so a project's own `config.yml` stays the seat's to edit.
- `rust/agenthub-runtime/tests/seat_guard.rs`: `a_seat_cannot_edit_the_config_or_the_log_the_guard_rests_on` - a write to each of the two through the write tool, through a shell redirect and through a `cp` over it, plus the two negative cases (the same two file names in another directory stay writable). `verdict_in` pins the seat's own directory so a case can name the path the guard protects.

### Testing
- `cargo test --offline` in `rust/agenthub-runtime`: 125 unit + 7 `seat_guard` + 1 `umask_at_the_entry`, 0 failed.
- With the previous `seat-guard.ts` the new test fails on its first case (a write to `config.yml` is allowed) and the other six pass.
