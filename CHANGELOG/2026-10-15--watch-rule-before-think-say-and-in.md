# watch prints the step rule before reasoning, replies and incoming messages too

### Changed
- The coloured rule before a tool call is also printed before `~ think`, `▸ say` and `◂ in`, so every step is separated; it replaces the dim rule that preceded `◂ in` (`internal/clientwatch/feed.go`, `view.go`, `docs/WATCH.md`).

### Tested
- `go test ./internal/clientwatch`; `TestAChangeOfToolKindGetsALabelledRule` covers reasoning detection.
