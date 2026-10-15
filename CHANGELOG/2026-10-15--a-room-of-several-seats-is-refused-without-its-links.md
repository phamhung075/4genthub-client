# A room of several seats is refused without its links

### Added
- `internal/clientteam/links_required_test.go`: the refusal, with the one-seat and the one-link controls.
- `internal/clientteam/plan.go` `loadTeam`: a definition with more than one seat and an empty `links[]` is a usage error naming the count — `a room of N seats needs its links; with none, no seat can message another and the topology has no edges`. One reader (`loadTeam`), so the dry run and the apply both stop before anything is sent: `team apply` (`team.go:150`), the room spec the loader builds for `up` (`roomspec.go:22`), and `prune`.
- `docs/AGENT-SETUP.md`: the link kinds and the rule that every room is created with its edges.
- `skills/client/spawn-team/SKILL.md`: `links[]` are required in the step that writes `team.json`, with the minimum edge set and the check afterwards (`whoami --json` `edges`, the cloud's `/rooms/<room>/seats/<seat>/links`).

### Why
- A room with `"links": []` is not finished: its seats cannot message each other, `whoami` returns no `edges`, and the topology page draws no lines. The guard makes that a refusal at the definition, not a silent half-built room.

### Testing
- `go test ./internal/clientteam` passes.
