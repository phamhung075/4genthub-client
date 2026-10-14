# Watch shows an edit as red minus and green plus lines

### Added
- `internal/clientwatch/render.go`: an `edit` call prints its path, then the removed lines in red behind `-` and the added lines in green behind `+`, instead of the path alone.
- `docs/WATCH.md`: the `✎ edit <path>` row.
- `internal/clientwatch/render_test.go`: `TestEditShowsRemovedAndAddedLinesApart`.

### Testing
- `go test ./internal/clientwatch` passes.
