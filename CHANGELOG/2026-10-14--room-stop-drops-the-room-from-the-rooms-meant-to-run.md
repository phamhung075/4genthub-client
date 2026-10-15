## 2026-10-14 — `room stop` drops the room from the rooms meant to run

### Fixed
- `4genteam room stop ROOM` stopped the seats but left ROOM in `rigd.json`'s `rigs` list. rigd, the single
  restart authority, kept a compact supervisor, a watchdog and a messages child running for a room that was
  stopped on purpose. `clientservices.RemoveRig` existed for this ("what `down` does") but had no caller
  since `down` was removed with 4genteam. `room stop` now removes the room from the list before it asks the
  runtime to stop the seats, so a stale entry (a room the runtime no longer knows) can also be cleared.

### Changed
- `internal/clientroom/room.go`: `runStop` calls `RemoveRig` and reports "stopped, and no longer meant to run".
- `internal/clientroom/room_test.go`: the stop test runs against a temp rigd state; two new tests pin the removal,
  including when the runtime refuses the stop.

### Tested
- `go build ./... && go vet ./... && go test ./...`.
