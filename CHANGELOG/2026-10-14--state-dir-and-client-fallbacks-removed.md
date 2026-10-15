## State lives in `~/.config/4genthub`; the 4genteam fallbacks are removed

### Changed

- Default state root is `~/.config/4genthub` through one helper, `clientenv.StateSubdir`. `AGENTHUB_STATE_ROOT` is gone.
- Seat store: `~/.config/4genthub/seats`. Sender identity: `AGENTHUB_SESSION_NAME`.
- Env renames: `CLIENT_RUNTIME_SOCKET` -> `AGENTHUB_RUNTIME_SOCKET`, `CLIENT_CLIENT_SKILLS` -> `AGENTHUB_CLIENT_SKILLS`,
  `AGENTHUB_SKILLS_ROOT` -> `AGENTHUB_SKILLS_ROOT`. The shared skill `CLIENT.md` is now `VENDORING.md`.
- `send` refuses `--verify`, `--force`, `--wait-for-idle`, `--dangerously-interact` and `--reason` (no native meaning).
- `continue --all` and the watchdog read rooms and seats through the runtime.

### Removed

- `clientruntime/referencedaemon.go`, `RigEntries`, the `CLIENT_URL` transport, the installcheck 4genteam port,
  `NeedsRig`/`clientcmd.Rig`, the top-level `queue` shim and the `argv[0]=="rig"` map.

### Fixed

- `RoomNodes` no longer fails on `*string` fields and reports an unknown room through `clientruntime.IsUnknownRoom`.
- `clientroom` queue actions act as the native seat session (`AGENTHUB_SESSION_NAME`).

### Tested
`go build ./... && go vet ./... && go test ./...`; U3 acceptance on `cutover-lab`, run 11: 124 pass, 0 fail.
