## The peak-held resume is deferred not dropped, and a send's outcome decides the witness

### Fixed (four supervisor fixes, ported onto the client's rewritten supervisor)

- **A peak-held resume is owed, not lost.** A refusal from the off-peak gate used to clear the compaction
  witness before asking: `hasBefore` went false, the seat was under the soft limit, and nothing re-armed
  it, so the seat stayed silent after the window opened. The witness now stays in place, `seatState.held`
  marks the resume as OWED, the next pass asks the gate again, and the resume goes out at the first open
  pass. The hold and the witness line are logged once per hold, not once per pass.
- **A second compaction during a held resume is reported.** The witness line is news, not a heartbeat: the
  same witness is written once per hold, and a witness whose compaction COUNT moved - a second compaction
  landing while the resume waits - is written again (`seatState.reported`).
- **The witness is cleared by the transport's outcome, not by the attempt.** `Say` now returns the
  transport's own error, and the resume's witness is cleared only when the send was accepted. A refused
  send is logged (`resume not delivered (...), the witness stays and the gate is asked again`) and the
  next pass re-asks; before this, clearing the witness first made that loss final.
- **A hold armed by the drop clause keeps its witness.** The window still stops the supervisor waiting for
  a compaction that never came, but expiring it no longer abandons a resume that is owed: with a hold, the
  witness survives and the gate is re-asked even when `done` has regressed because the seat worked again.

### Reconciled rather than cherry-picked

The branch's commits (`834dbaa`, `628f267`, `460ea6f`, `8da2966`, `1f70e92`) were written against a
`rig send` subprocess with `--wait-for-idle`. Main's supervisor sends through the runtime's `SendText`,
which refuses a mid-turn seat instead of waiting, so the send is synchronous and the outcome is its
returned error: the branch's outcome inbox, its observing goroutine and `seatState.sending` are not
needed, and `sendDeadline`/`sendWaitDelay` are not either - `clientruntime.CallDeadline` (10 s, applied to
the socket) is the bound on the send, so no second timer is invented. `idleguard` still ignores the
outcome: its nudges are latched by its own clock, and the resume is the one send whose loss must keep its
witness.

### Tests

- `internal/clientlifecycle/supervisor_test.go`: `TestAPeakHeldResumeIsSentWhenTheWindowOpens` (peak pin
  sends nothing, a second pass leaves the log byte-identical, the pass past the window sends
  `resumeText`), `TestASecondCompactionDuringAHeldResumeIsReported`,
  `TestAResumeTheTransportRefusedKeepsItsWitness` (witness kept on refusal, cleared on acceptance),
  `TestAHoldArmedByTheDropClauseKeepsItsWitness` (expiry keeps it, the open minute releases it). The
  fixture's `Say` returns the transport's outcome now.
- Run at this HEAD: `gofmt -l cmd internal` (nothing), `go build ./...`, `go vet ./...`,
  `go test -count=1 ./...` all green; `cargo test` in `rust/agenthub-runtime` = **194 passed, 0 failed**
  (174 unit + `held_send` 1 + `seat_guard` 18 + `umask_at_the_entry` 1).
- Not run: no live `compact-run` pass. Stepping the real supervisor acts on live seats (it sends
  `/compact`, interrupts and resumes), so the behaviour is pinned by the pass tests and the daemon was
  not exercised against the room.
