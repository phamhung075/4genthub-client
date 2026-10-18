## `evidence` prints the done report at item close

Batch 7 of the supervisor-port table: `a98e78b` and `79cadc0` onto `01a2837`, rebuilt against main's
`Collect`/`postEvidence` split rather than the branch's single flow.

### Added

- A STORED submission now prints the **done report**: the board row it closes (`GET /api/v2/tasks/{id}`, read
  back through `clientsync.RequestJSON`), the size of the diff being submitted, the doc-drift line, and the
  tests the submission names. It is a report, not a gate — the stored event is still what the exit code answers
  for, and the reviewer still decides.
- The row read checks the envelope the route actually answers (`{"success": true, "task": {...}}`), the same
  way the other readers of this API do. The first version read a `data` wrapper (the MCP shape) and printed
  `row: unknown [unknown]` against the live route; a fixture that encoded `data` hid it, and the fixture now
  answers the real envelope.
- A row that cannot be read is NAMED and the report goes on, so a reader can tell "the row says nothing" from
  "the row was not read".
- The report is printed ONLY inside the 2xx branch: a refusal writes nothing to stdout and costs no second
  request.

### Tests

- `clientevidence`: a stored submission prints the report (row, diff, doc drift, tests) and reads the task route
  exactly once; a 409 prints no report and reads no row. The fake server now routes the row read apart from the
  submission, so `hits()` still means "submissions dialed".
- `clientcomplete`: its two pinned call sequences gain the evidence step's row read — four calls, with the
  comment saying which one it is.
- `gofmt -l` empty, `go vet ./...` rc 0, `go test -count=1 ./...` 34 packages ok.
- Smoke, the built binary against a throwaway local responder:

  ```
  {"event":"stored"}
  done report for task smoke-1
    row: the row from the smoke server [in_progress]
    diff da3b906..66db378: 4 file(s) changed
    doc drift: none (1 of 4 file(s) are markdown)
    tests: true exit 0
  ```
