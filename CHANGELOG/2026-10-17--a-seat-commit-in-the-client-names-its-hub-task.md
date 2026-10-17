## 2026-10-17 — a seat commit in the client names its hub task

### Added
- `scripts/git-hooks/commit-msg`: runs the hub's `scripts/git-hooks/task-trailer-guard`, so a seat commit (`AGENTHUB_SESSION_NAME` set) without a `Task: <hub task id>` trailer is refused here as in the hub. The owner host and merge commits pass. Install: `ln -sf ../../scripts/git-hooks/commit-msg .git/hooks/commit-msg`. Measured: no trailer rc=1, owner rc=0, merge rc=0, trailer rc=0.
