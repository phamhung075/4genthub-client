# watch prints a labelled rule before every tool call

### Changed
- `watch` and `feed` print a plain rule in the kind's colour (no label, the call line names the tool) before every tool call, not only when the kind changes (`internal/clientwatch/feed.go`, `view.go`; `docs/WATCH.md`).

### Tested
- `go test ./...`; `TestAChangeOfToolKindGetsALabelledRule` covers the rule and the call detection.
