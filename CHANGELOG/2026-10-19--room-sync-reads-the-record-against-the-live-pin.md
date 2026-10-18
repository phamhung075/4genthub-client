## `room sync` reads one room's record against the live pin

### Added

- `internal/clientroom/sync.go`: the `4genteam room sync [ROOM] [--json] [--url BASE]` sub-verb, the
  client half of the hub's `GET /api/v2/rooms/{room}/sync` (route `30fa9bd9`, documented at `9c3e3013`).
  It is the group's ONE CLOUD verb: a token-scoped GET of `$AGENTHUB_URL`, with no state dir and no
  runtime socket, so its header reads `room: <room>@<url>` rather than the pair every runtime verb prints.
  ROOM defaults to `$AGENTHUB_ROOM`, the default the queue sub-verbs already take.
- One row per seat a machine reports: `SEAT SYNC EXPECTED PINNED MACHINE REPORTED`, where `SYNC` is the
  route's own `in_sync`/`drift`/`unknown`, `EXPECTED` is the cloud's resolved hash and `PINNED` the running
  pin the bridge reported. A seat no machine reports is absent, and the empty answer says so in words -
  absence is never `in_sync` - and names the roster route (`GET /api/v2/rooms/{room}/seats`) that answers
  the other question.
- `--json` prints the server's body verbatim, for a caller that wants to gate on a verdict.

### Behaviour worth naming

- **A drift is data, not a failure.** Every 2xx answer exits 0 whatever the verdicts say; the exit code
  reports whether the question was answered. A non-2xx prints the status and the server's `detail` and
  exits 1; a missing URL or token exits 2; a bad invocation (a second room, no room and no
  `$AGENTHUB_ROOM`) exits 3 with the variable named rather than guessed.
- `room.go`: the verb joins `roomVerbs`, the dispatcher and `subUsages`, and the shared usage text now
  scopes the `<room>@<state-dir> socket:` header to the verbs that ask the runtime, naming `sync` as the
  cloud exception.

Measured: `go vet ./internal/clientroom/` clean, `go test ./internal/clientroom/` passes (8 new tests);
the built binary against a stub answering the documented body prints the three rows above and exits 0,
`--json` prints the body, and against the live hub - where the route is not deployed yet - it prints
`the server answered HTTP 404` and exits 1.
