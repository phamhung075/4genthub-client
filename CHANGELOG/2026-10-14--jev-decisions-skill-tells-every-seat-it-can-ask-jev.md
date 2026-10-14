## A skill tells every seat that Jev runs on its machine

### Added
- `skills/share/jev-decisions/SKILL.md`: when a seat calls `4genteam decide` (before asking the lead or owner about a choice) and `4genteam complete` (instead of closing a task by hand), with the exit codes and the credentials/spend rule. It points to `room-decision-request` for the question format.

### Tested
`go vet ./...` and `go test ./...` pass. The skill is linked into `~/.claude/skills`; a seat gets it on its next start.
