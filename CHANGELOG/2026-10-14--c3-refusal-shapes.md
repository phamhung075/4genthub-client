# The three refusal shapes, and capture off the 4genteam daemon

C3, the unit the lead routed from the architect's final scope ruling
(`/home/daihu/.config/4genthub/agenthub-seats/4genthub-min/ARCH-RULING-c3-refusal-shapes-final-2026-10-14.md`), after the
callback fix. Commits `a70f5da` (an older runtime is named), `5dfc9d9` (capture off the daemon), `e4b5ccf` (the
rig list from the daemon's route), `a092770` (the gate's silence ended), `2926981` (an undelivered brief fails
the restore), `5fb4aef` (respawn's launch refused in words). Every sha was verified from a clean checkout: build
0, vet 0, 24 packages ok.

## What was measured before anything was touched

The ruling was worked by measuring each item against HEAD, so nothing already landed was re-done:

- **U-a `rigNodes`** — already carries `(nodes, ok, err)`, landed in `968b2b6`. Nothing to do.
- **U-b `PeakGate`** — the `RequireRig` skip the ruling describes is GONE and its error branch prints. What
  remained was the third case: NO RECORDS returned `ExitOK` with NOTHING PRINTED. Open, fixed here.
- **U-c `restore.go`** — the closure already returns an error with the closure's own refusal text and `brief()`
  already logged `send failed for <seat>` (`4203d96`). The lead's guess was right; the open half was that the
  failed brief still did not reach the verb's exit code.
- **M** — the transport already types its refusals (`DaemonRefused`, `UnreadableAnswer`) but an older runtime's
  FRAMING failure was not distinguished from a verb's own refusal. Open, fixed here.
- **R `runRigCapture`** — carries ONE leg, the seat launch; the two `ps --json --nodes` legs the ruling names
  were already rebound to `clientruntime.SeatNode` in `d6c0749`.
- **capture** — still gated on `CLIENT_URL`/`CLIENT_PORT` and POSTed `/api/transport/capture`, while
  `clientruntime.Client.Capture` already reads the pane over the runtime's own socket.

## M: the version boundary is a fact about versions

An installed runtime that predates a verb answers `{"ok":false,"error":"invalid request JSON: unknown variant
`QueueList`, expected one of …"}`. Printed as the verb's answer that reads as something about the ROOM, and
nothing about the room was asked or answered. It is now `*RuntimeTooOld` with `ExitUnavailable`, naming the
action, the socket, the runtime's own words and this client's version.

**The client matches serde's markers rather than a marker of our own, deliberately.** The runtime this must
survive is OLDER than this build — that is the entire case — so a `kind` added to the runtime now cannot be in
the answer that arrives; a marker would only help a future runtime, and a future runtime is the case the
install discipline already wins. The markers are pinned by a test, so a wording drift is visible.

**What it must never do** is rewrite a verb's own refusal: `unknown qitem_id 'x'` is the runtime refusing WORK,
and reporting that as unimplemented would be a lie about versions. Only framing markers convert; every other
`ok:false` still arrives as `DaemonRefused`, and the test that pinned the business refusal passes unchanged.

## U-b: the fail-open start is said

`PeakGate`'s NO RECORDS case keeps its decision — block nothing on a room the client knows nothing about — and
keeps `ExitOK`. But the gate has not decided THAT start, so the fact is printed, once, in the gate, so `up`,
`compact` and a restore cannot each forget it. No caller printed it before, so nothing is duplicated.
`TestTheGateProceedsWhenTheRoomHasNoRecords` had asserted the SILENCE; the assertion moved with the behavior,
with the start itself unchanged.

## U-c: a delivery that did not happen is not reported as one

`brief()` returns the seats its text did not reach; `restore()` answers false and names them when that list is
non-empty. The failure was already logged — `send failed for <seat>` — but the restore still ended `true` and the
verb still said `done`, over a seat nobody briefed. That is precisely the shape R3 forbids, and it is what the
restorer's own doc already promised its `false` was for. `continue <rig>` now exits `ExitRestoreFailed`, and the
`--all` summary says `rigs did not complete` rather than `rigs are not up`, because a rig whose seats are up and
whose brief did not arrive is in that list now and the old wording would state a false fact about the room.

## R: the launch that has no verb, refused in words

The leg was `seat launch <session> --fresh --stop --reason R` — one session, started fresh, the old occupant
stopped first, an audit reason recorded on the `seat.fresh_launched` event. Measured against the native verbs:
`StartRoom` takes a whole room from a spec; `RestartSeat` takes `{room, seat}` and carries **no reason field at
all**; nothing starts one session fresh. So wiring `RestartSeat` would have dropped the operator's `--reason` on
the floor while the verb reported a respawn — the silent skip R1 exists to forbid, and exactly the direction this
pass is closing.

The step is therefore not taken and it is spoken: the session is named, the exit is `ExitUnavailable`, stdout
stays empty, and `rig` is never consulted. `runRigCapture`, `rigResult` and `lastLine` are deleted with the
`os/exec` import. **The ruling's suggested remedy needed correcting here**: it points the operator at `4genteam
seat launch`, and `4genteam seat` has no `launch` verb (its verbs are status, sync, watch, reseat) — so the
refusal names `4genteam room reseat ROOM SEAT`, the CLI door onto the same native `StopSeat`+`RestartSeat` path
the ruling itself describes, with the non-equivalence stated inline.

The suite's command-leg stub is replaced by a `rig` planted FIRST on PATH that records having been run: the
assertion is now stronger than the call list it replaced, because a second reach for the program anywhere on
this path cannot hide behind a stub.

## capture: one call onto an interface that was already there

`captureCommand` gated on `CLIENT_URL`/`CLIENT_PORT`, POSTed `/api/transport/capture` and parsed the document
it got back — so the verb only worked where an 4genteam path existed, which is the path this pass removes. It
calls `Client.Capture` now, over the client's own transport. Verbatim stdout and `--lines` are unchanged;
`--json` prints this client's own capture answer, because there is no daemon body to pass through any more; a
runtime that is absent or refuses is a refusal in words with `ExitUnavailable` and nothing on stdout, and nothing
falls back to the daemon.

Of the five daemon-path tests, four became runtime-socket tests in `capture_test.go` and one was **deleted
rather than converted**: `TestCaptureCommandReadsASuccessWithoutAnOkField` pinned tolerance for a daemon body
that omitted `ok`, and its subject does not exist once the daemon path is gone. Keeping it would have been a test
of a path the verb no longer walks — the failure mode this cutover keeps finding.

## The rebinds the ruling's item 1 asked for

`continue`'s archived listing and the bridge's notification sweep both read `clientruntime.RigEntries`
(`GET /api/ps`) instead of exec-ing `rig ps`. The listing keeps the shell's `head -30` cap, sorts by name with
the id breaking ties, marks `[archived]` because it asked for archived rigs, names a failed read, and — one
judgement beyond the delegated task — says when no 4genteam daemon is named rather than leaving the section empty
under a heading that would then read "no rigs here". The delegated version was silent there; the line it
replaces named the absent rig tool for the same reason, so silence was a regression in information and is
corrected. `ParseRigNames` went with its only caller.

## Evidence

Every new guard was shown FAILING with its fix reverted, then restored byte-identically (md5 compared):

- `TestAFailedRoomReadHoldsTheResume` — the resume text really goes out over a failed read.
- `TestAnUnreadableRoomIsNotReportedAsStopped`, `TestWatchDoesNotCountAnUnreadableRoomAsAMiss` — the false
  "daemon reports no running seats" sentence and the miss counter really fire.
- `TestAnUndeliveredBriefIsAFailedRestore` — exit 0 against the swallowing version, against the wanted 1.
- `TestPeakGateSaysWhenItDidNotRunForARoomItHasNoRecordsOf` — stderr empty for both verbs when the print is
  removed.

`go build ./...`, `go vet ./...` and `go test ./...` (24 packages) pass in the worktree and in a clean checkout
of `5fb4aef`.
