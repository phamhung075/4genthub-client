# Seat tool hook, and one timeline to recheck what a room did

### Added
- `rust/agenthub-runtime/assets/seat-guard.ts`: the tool hook every seat starts with. The runtime writes it to
  `<seat dir>/hooks/pre/seat-guard.ts` in `start_seat`, next to `mcp.json`, and omp loads it from there (the seat dir is
  its agent dir). It refuses three calls the owner's rules forbid a seat - printing a `.env` (`env-secret`: `cat`, `head`,
  `sed`..., `grep` without `-c/-q/-l/-o`, the `read` tool), Docker (`docker`) and `git push` (`push`) - and appends one line
  per tool call to `<seat dir>/tool-calls.jsonl`: `{at, seat, tool, target, outcome, ms, rule?, error?}`.
  `outcome` is `ok`, `error` or `blocked`; an MCP call's `target` is `mcp:<tool>.<action>`; a token or key assignment in a
  target is redacted.
- `4genteam activity ROOM [--seat S] [--kind tool|send|jev] [--problems] [--since D] [--limit N] [--json]`
  (`internal/clientactivity`): one oldest-first, one-line-per-event timeline of the three records the client keeps - the
  seat tool log, the send decisions with their delivery outcome, and the Jev decisions. `--problems` keeps only blocked
  and failed calls, refused sends, escalations and rejected gates.

### Changed
- `cmd/4genteam/main.go` registers `activity`.

### Testing
- `cargo test --offline the_seat_` passes, including the new `the_seat_tool_hook_guards_and_logs`.
- `go test ./internal/clientactivity ./cmd/...` passes.
- Live, on an omp seat dir: `echo hello` ran; `cat .env`, `grep -n NAME .env`, `docker ps` and `git push --dry-run` were
  blocked with their rule named; `grep -c NAME .env` ran; an MCP `manage_project list` ran and was logged as
  `mcp:manage_project.list`.
