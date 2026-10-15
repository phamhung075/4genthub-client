# Watch: the header of an agy seat names its model

### Fixed
- `internal/clientwatch/seatlog.go`: an agy transcript carries no `"model"` field, so the feed header and `ps` showed `reviewer` with no model after `| `. The model came from a regex over the transcript text (`Gemini N Flash`) that matched only when the seat happened to write that name. `seatModelOf` now reads the last model in the log (omp) and otherwise the seat's `model` in the room's `spec.json`, the record the runtime launches from. The regex is removed.

### Testing
- `go test ./internal/clientwatch` passes, with `TestSeatModelOfFallsBackToTheRoomSpec` added.
