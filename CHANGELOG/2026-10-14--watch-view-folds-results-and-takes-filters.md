# watch view folds results, stamps real times and takes filters

### Added
- `--only KINDS`, `--errors`, `--changes` and `--expand` on `feed`, `grid` and `watch`
  (`internal/clientwatch/commands.go`, `watch.go`, `view.go`).
- A line diff for edits with `+N −M` counts and one line of context; a result tied to its call with
  the call's brief and duration; a turn separator before each incoming message; `↺ repeated ×N`
  for identical consecutive events; `● working` / `○ quiet Nm` in the pane title.

### Changed
- Results fold by default (reads to a line count, shell to 3 lines); `--expand` restores them.
- The line prefix carries the record's own time, printed only when the minute changes, and the
  context percentage only when it crosses a 10% step.
- `--width` defaults to 0, the width of the pane; the lead window and the skills use `--width 0`.

### Tested
- `go test ./...`; new cases in `internal/clientwatch/view_test.go`.
