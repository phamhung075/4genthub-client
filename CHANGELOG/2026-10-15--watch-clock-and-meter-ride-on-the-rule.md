# Watch: the clock and the meter ride on the rule, no blank prefix in a pane

### Changed
- `internal/clientwatch/feed.go`, `view.go`: in a one-seat pane the time and the context meter are printed at the end of the step's rule (only when they changed) and step lines start at column 0; the blank prefix that indented every think, say, in and call is gone, and the line width no longer reserves it. The merged multi-seat feed keeps its prefix because it needs the seat name.

### Testing
- `view_test.go` `TestRuleCarriesTheClockAndMeter`; `go test ./internal/clientwatch` passes.
