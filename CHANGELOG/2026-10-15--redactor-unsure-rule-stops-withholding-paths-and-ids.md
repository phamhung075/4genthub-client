# Redactor: the unsure rule no longer withholds paths and tool-call ids

### Fixed
- `internal/clientsync/redaction.go` `highEntropyToken`: a 32+ character run of `[A-Za-z0-9+/=_-]` with entropy of 4.0 bits or more withheld the line. File paths, queue-item ids and the random bodies of `call_00_<random>` / `toolu_<random>` tool-call ids all meet that, so 90-97% of an omp seat's transcript lines were withheld as `unsure`, and the agent stopped each session's upload ("every event has been withheld for 5m0s"). Two exclusions now apply before the test:
  - `toolCallIDPattern` removes tool-call ids (pairing keys, not credentials).
  - `isSegmentedName` skips a token made of segments of at most 16 characters with a separator (`/`, `-`, `_`) at least every 16 characters. A random secret rarely has separators that dense; known secret formats are still caught by `Redact` first.

### Testing
- `go test ./internal/clientsync` passes, including `TestEventKeepsPathsAndIdsButWithholdsSecrets`.
- A replay of the five seats' transcripts: withheld `unsure` dropped from 1827-2106 lines each to 21-115 of about 2000.
