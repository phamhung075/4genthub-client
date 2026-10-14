# Watch bodies stay at the left edge

### Changed
- `internal/clientwatch/feed.go`: continuation lines (diff bodies, multi-line text) are no longer padded to the step glyph; the padding wasted most of the pane width. They keep the 4-column indent. This reverses that part of the style pass.

### Testing
- `go test ./internal/clientwatch` passes.
