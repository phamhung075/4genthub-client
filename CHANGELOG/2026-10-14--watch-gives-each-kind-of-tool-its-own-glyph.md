## watch gives each kind of tool its own glyph

### Changed
- `internal/clientwatch/render.go`: `toolColors` becomes `toolStyles`; a call's head carries a glyph and colour by kind of work (read, search, write, shell, web, delegate, ask, MCP, other) instead of one `→` for all.
- `docs/WATCH.md`: the mark table lists the glyphs.

### Tested
`go vet ./...` and `go test ./...`; new `TestEachKindOfToolHasItsOwnGlyph`, existing call tests updated to the new glyphs.
