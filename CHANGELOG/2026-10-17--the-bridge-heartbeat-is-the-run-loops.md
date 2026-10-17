# The bridge heartbeat is the run loop's, not the one-shot cycle's

## Fixed

- `internal/clientbridge/bridge.go`: the heartbeat write moves out of `Cycle` and into `Run`'s loop,
  and a one-shot (`bridge once`, `bridge run --once`) no longer writes it. The bug: the check verb ran
  a full cycle, and the cycle's FIRST act was `WriteHeartbeat` with the *one-shot's own pid*, which
  exits with it. `verifyRow` then read a fresh beat (`age 0s`) whose pid was not in the live set and
  published `silent` - the row's `!slices.Contains(live, beat.PID)` branch - until the daemon's next
  beat, up to 20 s later. That is the owner's six `Safeguard_alert 'bridge silent (last beat 0s ago)'`
  messages (13:52:43, 13:56:55, 14:01:34, 14:02:30, 14:11:00, 14:38:12Z) while pid 461592 never
  restarted and beat every 20 s. Evidence, not inference: a sampler on
  `<root>/logs/heartbeat/bridge.json` caught the daemon's beats at 14:41:51 and 14:42:11 with a foreign
  `{"pid":1616534,...}` at 14:41:56 - a `bridge once` from the client's own test suite, pid gone by
  the read. pgrep was not the cause: a probe running the exact `Live` call every 50 ms for 150 s (2345
  reads) saw zero misses and zero disagreements with a direct `/proc` scan, so the row was right about
  a beat it had been handed wrongly.
- `cmd/4genteam/main_test.go`: `TestBridgeIsNotGovernedByThePlatformMatrix` now points `bridge once` at
  a temp `--state-root` and clears `AGENTHUB_URL`/`AGENTHUB_TOKEN`. It was driving the verb at the
  machine's real state root, which is how the suite wrote the real heartbeat (and, with a credential
  in the environment, would have sent a real report).
- `internal/clientbridge/runloop_test.go`: `TestCycleWritesOneHeartbeatPerCycle` - the pinned shape of
  the defect - is replaced by `TestTheRunLoopBeatsAndTheOneShotDoesNot`: a one-shot beats zero times,
  one run-loop iteration beats exactly once. Red first: the new test failed on the parent commit with
  `a one-shot cycle beat 1 time(s)`.

## Notes

- The fix is not a debounce: no alert is filtered. The heartbeat file can now only ever name a process
  that is the bridge, which is the precondition `verifyRow` was already entitled to.
- A running daemon built before this change still beats from `Cycle`; the corrected beat starts with a
  reseat/restart onto the rebuilt binary.

## Verified

- `go test ./internal/clientbridge ./cmd/4genteam -count=1` passes; `go test ./... -count=1` passes.
- Binary smoke: `/tmp/4genteam-fix bridge once --state-root <temp>` with a refused cloud
  (`AGENTHUB_URL=http://127.0.0.1:1`) ran a full cycle (payload built, send attempted, exit 1) and
  wrote no files under the temp root; the daemon's own heartbeat kept its pid 461592.
