# Jev: the idle guard stops looping, the gate stops accepting nothing, the log keeps the options

### Fixed
- `internal/clientlifecycle/idleguard.go`: the guard's message to the lead made the lead log, which started a new quiet five minutes later, so the same question (same open rail) was asked 32 times overnight at confidence 0.4-0.64. The guard now remembers the open rail the last verdict was asked about (`railSignature`) and asks again only when it changes.
- `internal/clientcomplete/gate.go`: a task with no acceptance criteria was accepted on the deterministic checks alone (8 of 9 accepts in the log), so Jev judged nothing. It is now `uncertain`, with a reason naming what to add.

### Changed
- `internal/clientjev/jev.go`, `internal/clientdecide/decide.go`, `internal/clientlifecycle/idleguard.go`: a decision-log line now carries `questions` (the options each pick was made from).
- `skills/share/jev-decisions/SKILL.md`, `internal/clientcomplete/complete.go` help: the three behaviours above.

### Testing
- `go vet ./...` and `go test ./...` pass; new `TestTheSameOpenRailIsNotAskedAgainWhenTheLeadsOwnReplyMakesANewQuiet`; the no-criteria test in `complete_test.go` now expects `uncertain`.
