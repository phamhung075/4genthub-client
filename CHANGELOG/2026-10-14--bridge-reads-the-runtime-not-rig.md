## The status bridge reads the agenthub runtime, not the rig tool

### What was wrong
`4genteam bridge` built its report by exec-ing `rig ps --json --nodes -A` (seats), `rig export <rig>` (topology) and `the task queue list` (the human's rows), and walked the rigs the 4genteam daemon listed. A machine running the agenthub runtime and no 4genteam reported nothing, and the verb was refused outright when `rig` was not on PATH.

### Changed
- `rust/agenthub-runtime`: new IPC action `GetRoomLinks{room}` (the links the room's spec declares; an empty list when none); `SessionEntry` carries the seat's `detail` (why it is not running).
- `internal/clientruntime/sessions.go`: `Client.RoomLinks`; `SessionEntry.Detail`.
- `internal/clientbridge`: seats come from `ListSessions`, edges from `RoomLinks`, rooms from `ListAllRooms` and the human's rows from `QueueList` (active rows to `human@<room>`). The seams are `Bridge.Nodes`, `Links`, `Rooms` and `Queue`. `ParseRigEdges`, `ParseHumanRows`, `readRigTool` and the rig-contract reads are deleted; `BuildPayload`, `Cycle` and `Run` no longer take a rig.
- `bridgeCommand.NeedsRig()` is false. `ui` is the one verb left that needs the rig tool (it opens the previous client's own web UI).
- A machine whose runtime is not running reports no seats and notes once that the runtime is not running; it does not fail the cycle.

### Tested
`go vet ./...`, `go test ./...`, `cargo test --offline` (new: `room_links_are_read_from_the_room_spec`; Go: seats, links, queue, rooms and the absent runtime through a fake runtime socket, plus the real verb's `bridge once --print` redaction).
