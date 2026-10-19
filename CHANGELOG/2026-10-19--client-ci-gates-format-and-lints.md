# The client CI gates formatting and lints, and the drift they expose is gone

## Changed

- `.github/workflows/ci.yml`: the `go` job runs `gofmt -l` after setup and before `go vet`; the `rust`
  job installs `rustfmt` and `clippy` with the toolchain and runs `cargo fmt --check` and
  `cargo clippy --locked --all-targets -- -D warnings` before `cargo test`. `--all-targets` so the
  tests are linted too, `-D warnings` so a new finding fails the job rather than scrolling past, and
  `--locked` for the same reason `cargo test` already takes it.
- `rust/agenthub-runtime`: `cargo fmt` over the whole crate. Fourteen files reflowed, no token changed.
- `internal/clientguard/guard_test.go`: `gofmt -w`, the one Go file that drifted.
- The sixteen clippy findings, fixed one at a time, none of them with an `allow`:
  - `main.rs`: the unused `error` import; a one-element `for` over `["RIGGED_TESTONLY_SCRUB"]` became
    the assert it was looping to make.
  - `ipc/mod.rs`: the unused `crate::pty::TerminalManager` and `std::path::Path` imports.
  - `pty/mod.rs`: the unused `std::io::Write` import.
  - `hooks/mod.rs`: `HookBus::subscribe` is reached only from the supervisor's tests, so it carries
    `#[cfg(test)]` instead of being dead weight in the binary.
  - `park.rs`: `Parked.seat` was never read; serde ignores unknown fields, so the record parses the
    same without it.
  - `types.rs`: `LinkKind` and its impl (`from_str_loose`, `as_str`) had no user anywhere in the crate.
  - `queue/mod.rs`: `map_or(true, ..)` is `is_none_or(..)`.
  - `skills/mod.rs`: `content[3..]` after `starts_with("---")` is `strip_prefix("---")`.
  - `supervisor/mod.rs`: the publication `match` that returns `false`/`true` is `!matches!(..)`; the
    newest `at` is read with `next_back()` instead of walking a whole log with `last()`.
  - `runner/claude.rs`: `Bridge::from_runner`/`from_agent` took `&self`, which `wrong_self_convention`
    reads as a `From` constructor; they are `translate_runner_frame` and `translate_agent_record`, and
    every call site (the runner's two loops and nine tests) follows the rename.
  - `runner/mod.rs`: the one `let mut attempts` whose value is only passed on.

## Tested

- `gofmt -l .` -> empty; `go vet ./...` -> clean; `go test ./...` -> ok (whole module).
- `cargo fmt --check` -> clean; `cargo clippy --locked --all-targets -- -D warnings` -> exit 0, no
  findings in the bin or the test targets; `cargo test --locked` -> 187 + 1 + 20 + 1 tests pass over
  the crate's four test binaries.
- Before the fixes, the three gates fail exactly as the board says: `gofmt -l` names
  `internal/clientguard/guard_test.go`, `cargo fmt --check` reports 340 diff hunks over 14 files, and
  clippy reports 14 errors in the bin and 15 in the test build.
