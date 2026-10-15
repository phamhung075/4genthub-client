# 4genteam retired: the C3 branch is merged and the rig CLI is off the path

### Changed
- Merged `4genthub-min/c3-printed-rig` (14 commits: send receipt claims only what the runtime proved, failed state reads are not empty answers, rail replaced per request, seat told its daemon's socket) into `feat/support-agy-watch` as `b3c9de9`. One conflict, `skills/client/spawn-team/SKILL.md`, resolved to the branch text with the watch width kept at `--width 0`.
- The `rig` CLI symlink `~/.nvm/versions/node/v24.21.0/bin/rig` is renamed to `rig.obsolete` (reversible; the npm package is untouched).
- Owner ruling 2026-10-15 ("you do it now"): the U3 acceptance gate and the snapshot-first rule were not waited for.

### Testing
- `go build`, `go vet`, `go test ./...` and `cargo test --offline` (90 passed) pass on the merge.
- With `rig` absent: `4genteam continue 4genthub-min` restored five running seats, `4genteam up 4genthub-min --team scripts/team/4genthub-min` started rigd, compact, watchdog, messages, roomguard and the bridge, and `watch` opened the grid. No `the reference client` process exists and `~/config/4genthub` is gone.
- Not done: the reviewer's gate on the branch, U3 run 5, and the push.
