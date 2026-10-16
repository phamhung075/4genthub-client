## 2026-10-16 - room report: stale tasks count from created_at

### Fixed
- `4genteam room report` printed "stale tasks: none" while 10+ go-dev tasks sat open. The hub's task list is
  minimal (no timestamps), so every task was skipped as unparseable, and the lifecycle sweeper rewrites
  `updated_at` when it escalates a task. The report now reads each open task and measures age from `created_at`;
  a task without a readable `created_at` is an error, not a skip.

### Files
- `internal/clientroom/report.go`, `internal/clientroom/report_test.go`

### Tested
- `go vet ./... && go test ./...`
