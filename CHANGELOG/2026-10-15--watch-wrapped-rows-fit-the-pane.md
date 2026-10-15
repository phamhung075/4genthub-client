# Watch: wrapped rows fit the pane and hang under the first row

### Fixed
- `internal/clientwatch/render.go` `block`: rows were wrapped at the whole line width although the head (`~ think `) and the 4-column indent also sit on the line, so every long row overflowed and the terminal wrapped it again to column 0. Rows now wrap at the width minus the head or indent, so each wrapped row stays indented under the first.

### Testing
- `go test ./internal/clientwatch` passes; a replay of the lead feed at `--width 100` has no line over 100 columns.
