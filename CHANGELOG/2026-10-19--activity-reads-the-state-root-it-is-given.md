# `activity` reads the state root it is given

## Changed

- `internal/clientactivity/activity.go`: `Run` takes `--state-root DIR` through
  `clientenv.StateRootFromFlags` and resolves it through `clientenv.ResolveStateRoot` - the same two
  calls `doctor`, `ps`, `room` and the rest use - instead of re-deriving `clientenv.DefaultStateRoot()`.
  The flag, else the machine's own root; an empty value means the default; the retired `--state-dir`
  spelling is refused by the shared extractor with the client's one message, before any record is read.
  Usage gains `[--state-root DIR]`. No new error path: the resolution failure is the same
  `StateRootMessage` refusal `doctor` prints, and the room is still parsed from the remaining arguments.
- The package's closing usage line now reads "Reads the state root only", because the path is no longer
  fixed at `~/.config/4genthub`.

## Tested

- `go test ./internal/clientactivity/ -run StateRootFlag -count=1` -> ok. The new test
  (`TestTheStateRootFlagIsReadInsteadOfTheMachineDefault`) builds the fixture under a directory whose own
  basename is the room name - a room the machine default cannot carry - so the assertion fails both on
  today's code (the flag is rejected as an unexpected argument) and on any code that reads the default.
- `go vet ./...` -> clean; `go test ./...` -> ok (whole module, branch
  `4genthub-min/port-03-activity-state-root`).
- Smoke: `4genteam activity demo --state-root /tmp/port03-fixture` prints the fixture's `flag-tree` row
  and exits 0; the same verb with `--state-dir` exits 2 naming `--state-root DIR`; `--help` lists
  `[--state-root DIR]`.
