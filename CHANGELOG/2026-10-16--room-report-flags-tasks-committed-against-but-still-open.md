# `4genteam room report` flags hub tasks committed against but still open

### Added

- `internal/clientroom/report.go`: the git log parser also reads a `Task: <uuid>` trailer (sha and title kept per
  commit). Each distinct id is looked up through the same hub transport as the stale-task check
  (`GET /api/v2/tasks/{id}`, `task.status`). Tasks whose status is not done, cancelled or archived are printed as
  `committed but still open: N` with id, status, commit count, last commit sha and title, followed by
  `commits without Task trailer: N` and the target `PASS/FAIL tasks closed after commit`. `--json` carries
  `committed_but_open` and `commits_without_task_trailer`. An unreachable hub prints `unavailable (...)` and the
  target is `n/a`.
- `internal/clientroom/report_test.go`: temp-repo fixtures for a trailer with an open task (listed), a closed task
  (not listed), commits without a trailer (counted) and an unreachable hub.

### Changed

- `buildReport` takes a task-status lookup as a third hub source; `hubTasks` and the new lookup share `hubGetter`.
