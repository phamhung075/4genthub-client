## `continue` and `watchdog` restore through the runtime, not `rig up --existing`

### What was wrong
`continue <rig>`, `continue --all` and `watchdog --watch` restored a dead room by exec-ing `rig up <rig> --existing` and started the daemon with `rig daemon status/start`. All three verbs refused to run when `rig` was not on PATH.

### Changed
- `internal/clientrestore/restore.go`: the restorer has no `rig` handle. `ensureDaemon` calls the runtime client's `EnsureDaemon`; `revive` (`reviveRoom`) reads the room with `GetRoomStatus` and calls `RestartSeat` on each seat that is not running, so each seat resumes its own saved session. A running seat is not touched, because a restart cycles a live agent. A room the runtime does not know returns the runtime's refusal; seats that fail to start are named in the error.
- `continue.go`, `watchdog.go`: `resolveRig`, `RequireRig` and the watcher's `rig` field are removed. Neither verb refuses for a missing `rig` any more.

### Tested
- `revive_test.go` (new, against a fake runtime socket): only the stopped seat is restarted; an unknown room is the runtime's refusal and sends no start; a failed seat is named with its reason.
- `continue_test.go`, `watchdog_test.go`: the world records the restorer's runtime steps (`daemon up`, `up <rig> --existing`) in place of a stub `rig` script; the two "refuse with no rig tool" tests are removed.
- `go vet ./...` and `go test ./...` from a clean `git archive`.
