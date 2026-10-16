## 2026-10-16

### Fixed
- `internal/clientevidence/evidence.go` `failedPatterns`: a Playwright failure is now named from Playwright's own list-reporter line - `✘  2 [chromium] › e2e/x.spec.ts:12:7 › a group › the title (5ms)`, measured on this repository's own `agenthub-frontend/playwright.config.ts` - and the name is the test's location plus its title path. No pattern matched Playwright's shapes before, so a genuine failure stored an exit code with no name at all.
- `internal/clientevidence/evidence.go` pytest: the short summary must now be pytest's node id (the `::` in `FAILED tests/test_x.py::test_y`). `FAILED\s+(\S+)` stored something that was not a test name: the spec's own console line `FAILED READ {"status":503}` was recorded as failed `["READ"]` on task `c67f9ce6`'s evidence seq 26.
- `internal/clientevidence/evidence.go` `Collect`: failing names are extracted only when the command exited non-zero, so a passing run's evidence can never name a failed test, and a failing run whose output names no test carries `(no failing test name in the output)` instead of an empty list, which read as "nothing failed".

### Changed
- `4genteam evidence --help`: the `--test` paragraph says what a failing run stores when its output names no test.

### Tested
- `go test ./internal/clientevidence/` (whole suite green), and the three new cases were red first against the previous parser: `[READ]` stored for the console line, no names for the Playwright line, and an empty list for a failing run that named no test.
