# The seat log resolver reads an agy seat's transcript, so the stall clock stops counting a dead file

## Fixed

- `internal/seatlog/seatlog.go`: `SeatLog` now returns the newer of the seat's omp session and the agy
  transcript, the Claude Code log being the fallback it already was, and the new `AgyTranscript` reads
  the conversation agy names in `<seat dir>/agy-conversation`. A seat that switched runtime keeps the
  old runtime's log, so existence alone never said which one was live: the reviewer's seat directory
  still holds the omp session last written 2026-10-17T04:00:35Z, while its agy transcript was written
  minutes before each alarm.
- `internal/seatlog/seatlog.go`: no log now yields no runtime (`("", "")`), not the label `claude` for
  a path that does not exist.
- `internal/clientwatch/seatlog.go`: the watch view's own resolution (newest omp session vs agy
  transcript) is deleted; `seatLog` delegates to `internal/seatlog`, so both callers share one parser
  instead of two that drift.
- `internal/clientlifecycle/idleguard.go`, `internal/clientlifecycle/supervisor.go`: the binding sites
  record why they are the one resolver - the idle guard's stall clock and the compaction supervisor's
  context reading both depend on the agy route.
- `internal/seatlog/seatlog_test.go`: `TestSeatLogReadsAnAgySeatsTranscriptBesideAnOmpEraLog` is the
  red-first case: an agy seat whose sessions directory still holds a 14-hour-old omp file resolves to
  the agy transcript, and an omp log written after the transcript still wins.

## Tested

- `go test ./...` in the client: pass.
- Red before the fix: the new test resolved to `sessions/2026-10-16T22-47.jsonl` with runtime `omp`.
- Measured on the live room (`4genthub-min`, reviewer): old resolution `sessions/2026-10-16T22-47-14-848Z_01a146e5.jsonl`, age 14h9m; new `~/.gemini/antigravity-cli/brain/77c1b588-.../logs/transcript.jsonl`, age 1m40s.
- `cargo test --offline` in `rust/agenthub-runtime`: 166 pass, 1 fails on
  `supervisor::tests::a_multi_line_message_reaches_a_runner_seat_as_one_text_frame` with
  `path must be shorter than SUN_LEN` for a reply socket under `t.TempDir()` - an environment limit of
  the temp path, unrelated to this change.
