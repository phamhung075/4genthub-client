## `4genteam decide`: a client verb for the decision ladder

### Added

- `4genteam decide --tier seat|lead --summary ... (--questions JSON | --questions-file) [--category C] [--detail ...] [--url U]`
  posts to `POST /api/v2/decisions/evaluate` with `AGENTHUB_URL` and `AGENTHUB_TOKEN`.
- Exit codes: 0 decided, 5 escalated, 1 failed, 2 usage.
- `internal/clientdecide/{decide.go,decide_test.go}`; registered in `cmd/4genteam/main.go`.

### Tested
`go test ./internal/clientdecide/...`; the backend wiring test lives in the parent repo.
