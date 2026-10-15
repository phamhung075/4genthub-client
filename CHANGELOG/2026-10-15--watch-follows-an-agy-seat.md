# Watch and ps follow a seat that switched to agy

### Fixed
- `internal/clientwatch/seatlog.go` `seatLog`: it read only omp's `sessions/*.jsonl`, so after `sync switch ... --runtime agy` the grid, feed and `ps` kept tailing the old omp log, which ends at `session_exit sighup`, and the agy seat looked silent. It now also reads the agy transcript (`~/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/transcript.jsonl`, the id from the seat directory's `agy-conversation` file) and uses the newer of the two logs, labelled `agy` or `omp`. The existing `agyToOMP` renderer was already in place and had no input.

### Testing
- `go test ./internal/clientwatch` passes, with a new test for the switch in both directions. `4genteam watch feed --room 4genthub-min --seat reviewer` shows the live agy reviewer's calls.
