# A seat cannot raise its own budget

### Added
- `assets/seat-guard.ts`: the `budget-ceiling` rule refuses a seat's write of the room's ceilings -
  `4genteam room limits <rig> --usd|--calls|--on|--off` - and leaves the read alone, because a seat should know
  what it may spend. The verb writes `rooms/<rig>/limits.json` THROUGH the client, so it never names a file and
  every path rule missed it: measured at 6492b77, `--usd 999` was allowed while `rm`, `sed -i`, `truncate -s 0`,
  `dd` and the write tool against the same file were all refused. A ceiling a seat can raise is not a ceiling.
- `tests/seat_guard.rs`: `a_seat_cannot_raise_its_own_budget` pins six write shapes (including the env-prefixed
  and the path-prefixed call) as `budget-ceiling` and four reads as allowed.
- The guard is the owner's, and that is measured rather than assumed: while this change was being written, the
  seat's own edit tool was handed "blocked by seat guard (protected-paths): the git hooks and the seat guard are
  the owner's to change" for `assets/seat-guard.ts`. That is why this ships as a patch the owner applies.
- The limit that remains, stated: the guard judges the commands a seat RUNS, so a program that opens the path
  itself (`python3 -c "open('<record>','w')"`, `node -e fs.writeFileSync(...)`) is not judged - measured ALLOWED.
  The guard is a net for a seat's mistakes, not a sandbox, which its own source already says.

### Testing
- Before and after, one harness, the same seat dir and room: before the rule, `4genteam room limits fixture
  --usd 999` answered `{"decision":"allow"}`; after it, that command and its `--calls`, `--on`, `--off`,
  `env -u TMUX`-prefixed and `/opt/4gh/bin/`-prefixed forms answer `deny` with rule `budget-ceiling`, while
  `room limits fixture`, `room limits fixture --json`, `room status fixture` and a QUOTED mention stay allowed,
  and `rm` of the park record still answers `protected-paths` and `git push` still answers `push`.
- `cargo test --offline` is NOT run here on purpose: the test asserts the rule that lives in the one file the
  guard refuses a seat to write, so the owner applies the patch and runs the suite.
