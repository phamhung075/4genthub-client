# A room read that failed holds the automatic resume

C3 tranche 1, after the node seam landed (`968b2b6`) and the seat reads were rebound (`d6c0749`). Three
findings from the review of those two commits, two of them the same mistake in different files: a reading that
FAILED was collapsed into an empty answer, and the caller then stated a fact about a room nobody had read.

## The one that starts a seat

`supervisor.go` wires the off-peak gate's reading with `supervisor.Nodes`, and the wired closure did
`nodes, _, _ := rigNodes(rig)`: both the explicit answer and the error were thrown away. That looked harmless
where I read it, and it was not, which is worth writing down as a correction rather than a fix. The compaction
supervisor's witnessed compaction ends in an AUTOMATIC resume — `s.Say(s.Rig, seat, resumeText, false)` — and
that resume passes the same gate every other automatic wake passes. With the error discarded, a failed read
reaches `clientoffpeak.Check` as `nil` nodes; `SeatsFromNodes(nil)` is an empty slice; `Decide` finds no
DeepSeek seat to refuse over and returns `proceed = true`; the seat is told to resume. So the failure direction
was fail-open on a money gate, on the one path in that file that starts a seat, and it contradicted
`lifecycle.go`'s own stated rule that an unreadable reading is never read as this room is safe. Reachability is
narrow — the read has to fail after the verb's own `PeakGate` already passed — but the direction is the whole
point of this pass.

The seam now carries the reading's three answers, exactly as `rigNodes` itself does:
`Nodes func(rig string) ([]map[string]any, bool, error)`. The automatic path:

- `err != nil` → the resume is HELD, and the log says the room's node records could not be read. Nothing is
  sent.
- `!ok` with no error → NO RECORDS is not a failure: a room whose records list no seats has nothing to bill, so
  the resume proceeds and the log says exactly that rather than leaving it to be inferred from silence.
- answered → the gate decides as before.

The three cases stay distinguishable at the call site, which is what makes the guard testable rather than a
matter of reading the wiring.

## The same mistake one file over

`watchdog.go`'s `expectedSeats` did `out, _ := watchdogNodesJSON(rigName)` and returned
`countRunningSeats(out)`. The seam was already honest and already documented the hazard — "the caller counts
zero either way, but only one of those is a fact about the room" — so the discard was the caller's, and each
caller had a sentence built on it. `--check` printed `daemon reports no running seats (expected 0, live N) —
deliberate stop or already handled; no action`: a deliberate stop asserted about a room that was never read.
`--watch` treated `(0, 0)` as `verdictDead`, incremented the consecutive-miss counter toward the restore, and
printed "MISS 1/2". The inaction was right in both cases — identical to what the shell's `2>/dev/null || echo 0`
produced — but the sentences were not, and the second caller was two polls away from acting on them.

`expectedSeats` now returns `(int, error)`. `check` names the failure and returns `ExitOK` without a verdict;
`watch` names the failure, does not increment the miss counter, and skips the heartbeat line for that poll
(which claims the room is "alive, daemon expects N, tmux has N" and has no reading to claim it with).

## The note that named a command that is gone

`switchverb.go` told the user `seat ROOM/SEAT is not in \`rig ps\``. The verb stopped exec-ing `rig ps` in
`d6c0749`; the note now names the reading — the room's records — and the assertion in
`TestSwitchSeatNotInRigPsExitsZero` moved with it. The test's own name stays: in this suite a test name mirrors
its upstream parity anchor (`test_switch_seat_not_in_rig_ps_exits_0`), so renaming it would break the pairing to
buy nothing.

## Evidence

Each fix was shown to be load-bearing by reverting only it and running the tests that pin it:

- Supervisor branch collapsed back to `nodes, _, _ := s.Nodes(s.Rig)` then straight into `Check`:
  `TestAFailedRoomReadHoldsTheResume` FAILS with `a resume went out over a room read that failed: [Context was
  compacted. Continue the job you were on …]` — the seat action itself, not a proxy for it.
- `expectedSeats` discarding the error again: `TestAnUnreadableRoomIsNotReportedAsStopped` FAILS on the printed
  false sentence, `TestWatchDoesNotCountAnUnreadableRoomAsAMiss` FAILS on the missing name.

Both files were restored byte-identically (md5 compared against copies taken before the revert) and the two
packages are green: `go build ./...` 0, `go vet` 0, `go test ./internal/clientlifecycle/... ./internal/clientrestore/...`
ok.
