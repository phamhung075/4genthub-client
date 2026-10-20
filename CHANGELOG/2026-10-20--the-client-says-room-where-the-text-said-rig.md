## The client says room, where the text said rig

Owner ruling 2026-10-20: the unit is the room; **rig** is gone from this client - code, flags, env,
skills and docs - with a clean break and no alias. Queue row `qitem-20261020204734-d0ea980a72bb`.

### Changed

- **The runtime wire.** `agenthub-runtime` serves `roomName` where it served `rigName`
  (`WhoamiIdentity`, `SessionEntry`; serde rename at `types.rs:258,325`), and the Go decoders moved with
  it in the same commit (`clientruntime` `WhoamiIdentity.RoomName`, `SessionEntry.RoomName`) - the
  contract is one string and both ends carry it, so no alias and no second parser. `whoami`'s human
  output prints `Room:`.
- **Local JSON.** `agent status --json` / `agent.json` (`Room`, `Rooms`), `ps --json` (`room`,
  `roomName`), `overlap --json` and `stuck --json` (`room`) now say room. `agent.json`'s watch list is
  keyed `rooms`; an older file carrying `rigs` is not read - clean break, the agent rewrites it.
- **Flags and env.** `sync bundle --room-yaml` / `--room-root` (was `--rig-yaml` / `--rig-root`),
  `ROOM_CONTINUE_WATCH` (was `RIG_CONTINUE_WATCH`), and the bundle member is `room.yaml` in the code
  that already wrote it.
- **On-disk names.** `room-continue.log`, `room-watchdog.log`, `room-watchdog.server-id`. A machine
  keeps its old log files under the old names; nothing reads them.
- **Identifiers.** Every `rig`/`Rig`/`RIG` identifier in `internal/` and the Rust crate is now
  `room`/`Room`/`ROOM` (`ensureRoom`, `roomNodes`, `roomRoot`, `RigSeats`->`RoomSeats`,
  `CanonicalRigSeat`->`CanonicalRoomSeat`, `RigRooms`->`Rooms`, `rigRoom`->`reportedRoom`, ...).
  `clientservices.Rigd` - an unused constant - is deleted. Comments that called the daemon `rigd` call
  it `agent`.
- **Two duplicate fields collapsed.** `sync messages` held the room name twice (`rig` and `room`,
  one per shape); it is one field plus the `--room` shape flag. The worktree fixture held the room
  directory and the room name under names that both became `room`; they are `room` and `name`.
- **Skills.** `skills/client/rig-runtime-switch` -> `skills/client/room-runtime-switch` (frontmatter
  name and every cross-reference updated); the operator skills lose `<RIG>`/`--room <RIG>` and the
  `RIG compact`/`RIG watchdog` doctor labels, and `install` checks `which 4genteam`.
- **Docs.** `README.md` and `docs/WATCH.md`, `docs/AGENT-SETUP.md`: the same rename, and four stale
  instructions rewritten rather than renamed - `rig ps --nodes` -> `4genteam ps`, the predecessor's
  `rig terminal open` bullet, and the `sync` subverb list that wrongly named `rig`.
- `internal/retiredverbs`'s allowlist names the renamed skill file; the scanned sentences it pins are
  the predecessor's and are untouched.

### Unchanged, on purpose

- The predecessor program's own name stays wherever the client refuses or contrasts it: the
  `retiredverbs` table (`Program: "rig"`), `commpolicy`'s risky-command matcher, the `--rig` send
  refusal, `OPENRIG_*`/`RIGGED_*` deny prefixes, and the comments that name its commands
  (`rig ps`, `rig up`) to say what this client does not run.
- **Two hub-owned wire keys stay `rig`**: the safeguard frame's `"rig"` key
  (`clientsync/agent.go:581` -> `ws_mount.go:514`, stored and re-emitted with alerts) and the
  decisions-ingest body's `rig` (`room_decisions_mount.go:344`). Renaming either alone would break
  delivery; the client's field is `Room` and the literal is commented as the hub's key.

### Testing

- `go test ./... -count=1`: **exit 0**, 35 packages ok.
- `cargo test --offline` in `rust/agenthub-runtime`: **exit 0**, 192 + 1 + 20 + 1 passed, 0 failed.
- Smoke: the built binary's `--help`, `ps --help` and `send --help` read room throughout; `send --help`
  still lists `--rig` among the REFUSED predecessor flags.
