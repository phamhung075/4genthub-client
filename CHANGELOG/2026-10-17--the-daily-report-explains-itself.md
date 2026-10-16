# The daily report explains itself

`4genteam room report ROOM --since 24h` is the surface this room reads to see what the team delivered and
cost (hub task `0d630664-b7ed-49cb-b370-14a2e144a9ce`, Outcome 3). Its `--help` explained nothing about it:
`reportUsage` was a single usage line, while the prose `room --help` prints covers status, stop, reseat,
compact and limits, and its sibling `metrics` carries a body of its own.

### Changed

- **`reportUsage` is a body.** It states what the verb is (one read-only measurement of one window, which
  writes nothing), its four sources and what each one contributes (the session transcripts' per-turn cost
  and the guard-blocked count, the queue from the daemon - counts and lengths only, never a body - and the
  `git log --since` of the repository the seats' session headers name most often, where a commit's `Seat:`
  trailer names its seat and `Task:` names a hub task), that the window is LIVE so two runs minutes apart
  answer differently and that `--json` prints the same answer as one object, the columns printed per seat
  and for the room, and the five targets with their thresholds - plus the sixth check the verb adds on its
  own (tasks closed after a commit that cites them, wanted 0). The usage line itself is unchanged, but a
  caller who mis-types a flag now reads that line followed by the whole body, on stderr, exit 3 - the same
  exit and the same first line as at the base, which printed the usage line alone.

### Testing

- `go test ./...` in the client: green. `internal/clientroom` includes
  `TestRoomReportEndToEndAgainstTheDaemon`, which runs the verb against a fixture room and asserts exit 0,
  the printed lines and that a queue body is never printed - so the help change is covered by the same
  suite that covers the verb.
- The changed surface, run against a client built from this tree: `4genteam room report --help` answers with
  22 lines (1611 B) instead of 1 (90 B), and `4genteam room report 4genthub-min --since 24h` still exits 0
  and prints the two tables, the six verdicts and the findings.

### What this does not do

- It moves no number the report prints. The five targets measure the room's window, not this verb: on the
  24h window read at 2026-10-16 22:3xZ three of the five missed - docs-only commits 57.6% (want < 15),
  high-priority rows 23.9% (want < 20), code-edit cost 14.1% (want > 30) - and two passed: queue rows/day
  247 (want < 300) and measuring cost 22.7% (want < 25). The readings and their causes are on the task.
- It schedules nothing. There is no per-day bucket, no cross-room roll-up and no scheduler; the daily read
  is one run by whoever reads it, which is how hub task `9c934d16` named the cadence gap and how the lead
  accepted it.
