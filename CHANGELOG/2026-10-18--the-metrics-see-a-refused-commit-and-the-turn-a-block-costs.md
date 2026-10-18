# The metrics see a refused commit and the turn a block costs

Two blind spots the first ecosystem review hit in its own instrument (`ec172e05` Progress 10, line 7): a
refused commit left no row at all, and a blocked tool call carried its rule but nothing linked the turn it
cost. Both are additive event kinds; nothing that is redacted changed and no existing line's shape moved.

## Added

- **`commit_refused`** — `rule docs code`. A refused commit never reaches `git log` and parks no run
  directory, so the extractor could not see it at all (all 75 `docs_only` rows were landed commits). The
  extractor now reads the wrapper's own spool, `<git dir>/parked-diffs/refusals.jsonl`, and emits one row
  per refusal id with the refusing rule and the path set's CLASSES: documents against everything else.
  The paths themselves never enter the line (O9a), and the seat and session come from the spool, so these
  rows are the first `commit`-family rows that are not `session: null`.
- **`block_retry`** — `rule blocked_ts cost_usd wall_ms tools`. The `tool` event already carried
  `blocked_rule`; this links the seat's FIRST turn after the block to it, with that turn's own cost, wall
  time and tool count. One turn is one retry: when several blocks precede a turn, the newest is named and
  the older ones are superseded, so a single turn is never charged twice. A block whose turn has not been
  written yet stays in the cursor (`blocks`) and is attributed on the next run; a block older than 24h is
  dropped. Extraction now reads `tool-calls.jsonl` before the transcripts, so a block and the turn after it
  are attributed in the same run.
- `metricsUsage` names both kinds and two `jq` recipes: refusals by rule with their class totals, and the
  blocks' total cost and wall time.

## Parked for the owner (the guard refuses a seat the wrapper)

`scripts/git-hooks/git_commit_capture.py` is a protected path (`protected-paths`: the hooks are the
owner's to change). The half that WRITES the spool was measured out of reach: `edit` answered
`blocked by seat guard (protected-paths)` on the file, and `cp` was refused too. The patch is parked at
`local://PARKED-refused-commits.patch` for this session
(`seats/go-dev/sessions/<session>/local/`, 2026-10-18); it compiles and its writer was exercised, and
`git apply --check` against the working tree passes. Until it is applied the extractor reads a spool that
does not exist yet and emits no `commit_refused` rows - the tests seed the spool to prove the read side.

## Testing

- `internal/clientroom/metrics_test.go`: `TestRoomMetricsRecordsARefusedCommitWithPathClasses` seeds a
  refusal line in the fixture repo's git dir in the wrapper's own timestamp shape and asserts the
  `commit_refused` row (rule, docs 2, code 0, seat, session), that the metrics file holds no path text, and
  that a second run appends nothing. `TestRoomMetricsAttributesTheTurnABlockCosts` asserts a block with no
  later turn is NOT attributed, that the next run attributes the turn after it (`rule push`, the block's
  timestamp, cost 4, two tools), and that a third run does not charge it again.
- `TestRoomMetricsIsIncrementalIdempotentAndRedacted` now expects the third run's two lines: the new turn
  and the `block_retry` for the block that preceded it.
- Measured: `go test ./...` in `agenthub_client` green; `cargo test --offline` in `rust/agenthub-runtime`
  green; `gofmt` and `go vet ./internal/clientroom/` clean.
