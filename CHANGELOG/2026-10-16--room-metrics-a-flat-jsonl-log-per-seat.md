# `room metrics`: a flat JSON-lines metrics log per seat

### Added

- **`4genteam room metrics ROOM [--since 24h] [--seat S] [--print]`** (`internal/clientroom/metrics.go`,
  `metrics_test.go`): appends one flat JSON object per event to `rooms/<room>/metrics/<seat>.jsonl` (mode 0600) so a
  room can be analysed with `jq` instead of re-parsing transcripts. Events carry `ts`, `schema:1`, `room`, `seat`,
  `session`, `event`:
  `turn` (model, tokens_in/out, cache_read/write, cost_usd, activity, first_tool, tools, wall_ms, task),
  `tool` (tool, target_class, outcome, ms, blocked_rule, bytes_out), `commit` (sha, files, docs_only, task),
  `queue_row` (qitem, priority, tag, summary_chars, body_chars, to). Activity comes from report.go's classifier.
  Only classes, counts, ids and lengths are written: no command line, file content, queue body, env value or token.
  Incremental and idempotent through `metrics/.cursor.json` (byte offsets of whole lines, commit shas, qitem ids);
  `--print` writes the new lines to stdout and persists nothing. `metrics --help` holds the schema and six `jq`
  one-liners.
- **`seat-guard.ts`** (`rust/agenthub-runtime/assets/`): every `tool-calls.jsonl` line gains `session`
  (`AGENTHUB_SESSION_NAME`), `target_class`, `blocked_rule` and, for omp results, `bytes_out`. Additive; existing
  fields are unchanged. Test: `tests/seat_guard.rs`.

### Changed

- `internal/clientroom/report.go`: `commitRecord` also carries the commit time and file count (`%cI`), which the
  metrics `commit` event needs. `room report` itself is unchanged; it can migrate to read the metrics log later.

### Notes

- `session` is the transcript id on `turn`, the seat's session name on `tool` (what the hook can see), null on
  `commit` and `queue_row`. `task` on a turn is set only when a tool call of that turn passes a `task_id`.
- Verified on the live `4genthub-min` with `--print --since 2h`: 224 lines, all valid JSON, none with `TOKEN` or
  `KEY=`; lead's summed `cost_usd` equals a manual jq sum of its transcripts (0.0698191).
