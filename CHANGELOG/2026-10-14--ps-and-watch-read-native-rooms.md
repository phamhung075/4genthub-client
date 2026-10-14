## `ps`, `feed`, `grid`, `watch` and `seat status` read native rooms

### Fixed

- `4genteam ps --json` printed `[]` for a running native room: `splitSession` parsed only the retired
  `<rig>-<member>@<rig>` name, and the transcript lookup in `clientwatch.seatLog` read `<root>/<rig>-<seat>@<rig>`.
- Both now follow the native layout: session `agenthub--<room>--<seat>`, transcript directory
  `<root>/rooms/<room>/seats/<seat>/sessions` (newest `*.jsonl`; the per-session artifact subdirectories are skipped).

### Changed

- `internal/clientruntime/sendseam.go`: added `ParseSeatSessionName` (inverse of `SeatSessionName`) and `SeatSessionsDir`.
  `clientroom.SessionName` now calls `SeatSessionName` instead of spelling the format a second time.
- `internal/clientwatch/seatlog.go`: `seatLog(root, room, seat)` resolves omp transcripts only. The agy and Claude Code
  lookups (`agyLog`, `claudeLog`) are removed: both matched the retired name, and the native runtime runs omp only.
- `internal/clientwatch/snapshot.go`: `SeatSnapshot(root, room, seat)` takes the state root; an empty root yields no log.
- `internal/clientwatch/grid.go` `rigSeats` and `internal/clientps/ps.go` `collect` parse the native name.
- `internal/clientseat/sync.go` `secondsSinceLastWrite` reads the native sessions directory.

### Tested

New tests: `TestSeatLogReadsTheNativeSessionsDirectory` (log path, runtime `omp`, model, tokens, artifact dir skipped),
`TestCollectReadsANativeRoom`, `TestParseSeatSessionNameInvertsSeatSessionName`. Before the change, `seatLog` on the native
layout returned `("", "claude")` and `splitSession("agenthub--R--S")` was rejected. `go build ./... && go vet ./... &&
go test ./...` pass.
