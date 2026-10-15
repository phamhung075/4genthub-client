# The Jev threshold comes only from the config; the seat audit keeps the two events the runtime can emit

### Changed
- `internal/clientjev/jev.go` `EvaluateCriteria`: accepts when every criterion is at or above `Config.Threshold` (`JEV_DECISION_THRESHOLD`), not at a second hard-coded 0.8. With the owner's 0.75, a 0.77 answer now accepts.
- The numbers are gone from the text that described the default: `internal/clientdecide/decide.go` help, `skills/share/room-decision-request/SKILL.md`, `README.md`. They point at the config. The only number left is the code's own fallback `defaultThreshold`.
- `rust/agenthub-runtime/src/hooks/mod.rs`: `SeatEvent` keeps `SessionStarted` and `SessionTerminated`. `PreTurn`, `PostTurn`, `ToolRequested`, `ToolCompleted` and `ContextLimitReached` are removed: nothing emitted them, and the runtime sees neither turns, tool calls nor token counts (compaction is measured by the Go supervisor).

### Added
- `SessionTerminated` is emitted by `stop_seat` and `stop_room` for each seat stopped (`exit_code` is `None`: a seat that dies on its own is only noticed by a later status read, and no code is known).

### Testing
- `go test ./internal/clientjev ./internal/clientdecide ./internal/clientcomplete`: ok; new `TestEvaluateCriteriaFollowsTheConfiguredThreshold`.
- `cargo test --offline`: 102 passed; new `stopping_a_room_announces_each_seat_it_stopped`.
