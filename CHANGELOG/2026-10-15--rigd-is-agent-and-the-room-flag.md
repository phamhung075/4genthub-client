# `rigd` is now `agent`; `--rig` is `--room`; `sync rig` is `sync room`

### Changed
- **Breaking, no aliases.** The always-on local client is `4genteam agent` (was `4genteam rigd`). Its state file is `agent.json` (was `rigd.json`), the safeguard row is `agent`, and in code `Rigd*`/`RigDaemon*` became `Agent*`/`AgentDaemon*`; `internal/clientlifecycle/rigd.go` and `internal/clientsync/rigd.go` (and their tests) became `agent.go`.
- The flag `--rig` is `--room` on every verb that took it (`watch`, `grid`, `inputs`, `input`, `compact-run`, `idle-run`, `policy`, `sync messages` and the others), and the verb `sync rig` is `sync room`. `internal/clientpolicy` reads the `room` key; skills, README and docs were updated to match.

### Kept on purpose
- The predecessor orchestrator's own `rig` program, its retired verbs (`internal/retiredverbs`) and the refusal of the predecessor's flags in `send` (`--rig`): those name another program.
- Wire and storage names shared with the server: the `/rigspec` path, `rig_name`, the `"rig"` and `"rigs"` JSON keys, the `rig_safeguard` tables and the frontend's names. They change only together with the server, the schema and the frontend in one pushed release.
- Go identifiers and test names that contain `rig` (about 4,000 matches): they are private and carry no behavior.

### Testing
- `go build ./...`, `go vet ./...` and `go test ./...` are clean. The runtime is unchanged by this entry.
