## State lives in `~/.config/4genthub`; the OpenRig fallbacks are removed

### Changed

- Default state root is `~/.config/4genthub` through one helper, `clientenv.StateSubdir`. `OPENRIG_STATE_ROOT` is gone.
- Seat store: `~/.config/4genthub/seats`. Sender identity: `AGENTHUB_SESSION_NAME`.
- Env renames: `OPENRIG_RUNTIME_SOCKET` -> `AGENTHUB_RUNTIME_SOCKET`, `OPENRIG_CLIENT_SKILLS` -> `AGENTHUB_CLIENT_SKILLS`,
  `OPENRIG_SKILLS_ROOT` -> `AGENTHUB_SKILLS_ROOT`. The shared skill `OPENRIG.md` is now `VENDORING.md`.
- `send` refuses `--verify`, `--force`, `--wait-for-idle`, `--dangerously-interact` and `--reason` (no native meaning).
- `continue --all` and the watchdog read rooms and seats through the runtime.

### Removed

- `clientruntime/openrigdaemon.go`, `RigEntries`, the `OPENRIG_URL` transport, the installcheck OpenRig port,
  `NeedsRig`/`clientcmd.Rig`, the top-level `queue` shim and the `argv[0]=="rig"` map.

### Fixed

- `RoomNodes` no longer fails on `*string` fields and reports an unknown room through `clientruntime.IsUnknownRoom`.
- `clientroom` queue actions act as the native seat session (`AGENTHUB_SESSION_NAME`).

### Tested
`go build ./... && go vet ./... && go test ./...`; U3 acceptance on `cutover-lab`, run 11: 124 pass, 0 fail.
