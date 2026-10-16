# Queue rows are typed

### Changed

- `rust/agenthub-runtime/src/queue/mod.rs`: `create` refuses, in one line naming the rule and its cap: a priority
  outside `low|normal|high`; `high` without tag `prod-break`; a tag outside the allowed lowercase-kebab set (the
  kinds of row the rail carries, derived from the tags in use on `4genthub-min`); a summary over 120 chars; a body
  over 600 chars. `update --summary` obeys the same 120-char cap. Existing rows are not migrated.
- `list` carries `requester_session` (the seat's `$AGENTHUB_SESSION_NAME`). The same list by the same session
  within 5 minutes with no new row is refused as `list rate limit: no change since <qitem>`; the daemon's own
  reads send no session and are not limited. Marks live in a new `queue_list_marks` table.
- `internal/clientroom/queue.go`, `internal/clientruntime/queue.go`: usage text states the rules; `QueueTags`
  mirrors the daemon's set; `list` sends the requester session.

### Tested

- One failing-without-it Rust test per rule (priority, high+tag, summary, summary update, body, tags, list rate
  limit); Go tests for the requester frame and the usage text. `cargo test --offline`, `go vet ./...`,
  `go test ./...` green.
