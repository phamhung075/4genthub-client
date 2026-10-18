## The build respawns the team on the binary it just installed

`scripts/build.sh` installed `4genteam` and `agenthub-runtime` and printed "restart the daemon to run it":
the runtime daemon is nobody's child (an agent stop leaves it running, which is what makes `up` idempotent),
it never re-reads its own binary, and no verb stops it. So a seat that rebuilt the client - the
`permanent-change` skill tells it to, and a seat must not relaunch the stack - left the machine serving the
previous runtime until the owner did the restart by hand, in the right order.

### Changed

- `scripts/build.sh` takes `--respawn RIG`, opt-in and parsed before anything is built (a missing rig is one
  line and exit 2, not a full build then a refusal). After the install and the `.env` step it runs
  `4genteam up RIG --respawn` with the installed binary, so the same run that replaces the daemon starts the
  room and opens the watch view and the UI. The peak gate is not bypassed: an `--allow-peak` is the
  caller's.
- `internal/clientlifecycle`: `up --respawn` stops agent first (the restart authority; a watchdog left
  running would restore the seats of the room being cycled), then stops the runtime daemon **serving this
  state root** and waits until it is gone; a daemon that does not accept the signal, or does not exit within
  `respawnWindow` (10 s), is reported by process id and the run refuses rather than starting a second
  daemon beside it. The room is then read and started as any other `up`: its record (`spec.json`), its
  seats' directories and their worktrees are never written by this step.
- `internal/clientlifecycle/respawn.go`: the daemon is matched by the argv `runtimeDaemonCommand` builds -
  `^[^ ]*agenthub-runtime --state-dir <root>( |$)` - through the same `pgrep -f` / `pkill -f` pair the
  safeguards use, with the root quoted so a daemon of another state tree is not this run's to stop.

### Measured

- `go test ./...` in `agenthub_client` -> rc 0; `go vet ./...` -> rc 0.
- `bash -n scripts/build.sh`; `--respawn` without a rig and an unknown flag both exit 2 before `go vet`
  runs.
- The pattern and the kill were exercised against a real process table: a stand-in spawned with the daemon's
  argv is stopped, and a stand-in for another state root survives
  (`TestRespawnDaemonReplacesARealProcessForTheRoot`, skipped where `pgrep`/`pkill` are absent).
