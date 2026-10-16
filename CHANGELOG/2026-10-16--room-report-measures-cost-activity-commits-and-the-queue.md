## `4genteam room report` measures a room's cost, activity, commits and queue over a window

### Added

- `internal/clientroom/report.go`, `internal/clientroom/report_test.go`, and one `report` case in `internal/clientroom/room.go`:
  `4genteam room report ROOM [--since 24h|90m|7d] [--json] [--state-dir DIR] [--socket PATH]`, read-only.
  Per seat and for the room it prints USD, turns, commits, docs-only commit share, queue rows, high-priority share,
  average body length (a count, never a body), guard-blocked calls, and the cost share of nine activity classes
  (each assistant turn is classified by its first tool call). It then checks five targets (docs-only < 15%, queue
  rows/day < 300, high < 20%, measuring cost < 25%, code-edit cost > 30%) as PASS/FAIL, and lists hub tasks in
  `todo` or `in_progress` with no update for over 24h.
- Sources: the seats' `sessions/*.jsonl` and `tool-calls.jsonl`; the queue through the runtime daemon (`QueueList` -
  the client links no SQLite driver and the daemon owns the store); `git log --since` of the working directory the
  sessions name, with the commit's `Seat:` trailer naming the seat ("other" when absent); the hub's
  `GET /api/v2/tasks/` through `clientsync.RequestJSON` with `AGENTHUB_URL`/`AGENTHUB_TOKEN` from the process environment.
  A source that cannot be read prints `unavailable (<reason>)` and its targets print `n/a`; it is never a zero.

### Tested

- `go vet ./... && go test ./...`. Fixture tests cover the classifiers, the git-log parser, the window, an unreadable
  queue, the stale-task filter, the whole verb through a fake daemon (no body is printed) and `--json`.
- Against the live `4genthub-min` room, 24h: 118 commits and 70 docs-only equal a manual `git log` count; 587 queue
  rows and 409 high equal a read-only SQL count of `queue.sqlite`.
