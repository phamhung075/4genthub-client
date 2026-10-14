## decide reads its detail from a file

### Added
- `internal/clientdecide/decide.go`: `--detail-file <path>`, the same text as `--detail` read from a file; giving both is a usage error. A seat's `--detail "..."` containing a double quote ended the shell string early, so the rest of its text ran as a command and a garbled detail reached `jev_decisions.log` (the 21:37 `build ref for the U3 legs` entry).
- `skills/share/jev-decisions/SKILL.md`: tells seats to write the context with a quoted heredoc and pass `--detail-file`.

### Tested
`go vet ./...` and `go test ./...`; new `TestDetailFileCarriesTextAShellWouldBreak` logs a detail with quotes, `$` and a newline unchanged.
