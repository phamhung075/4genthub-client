## watch puts the seat, model and token bar in the pane title

### Changed
- `internal/clientwatch/render.go`, `feed.go`: a single-seat feed running in a herdr pane (`HERDR_PANE_ID` set) renames that pane to `== <seat> | <model> ==  <bar> <pct> <used>/<limit> to compact` (plain text, at most every 5 s) instead of repeating the header line in the stream. A feed in a plain terminal keeps the in-stream header.
- `docs/WATCH.md`: describes the pane title.

### Tested
`go vet ./...` and `go test ./...`; new `TestAPaneHeaderGoesToThePaneTitleAsPlainText`.
