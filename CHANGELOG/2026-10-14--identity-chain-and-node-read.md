# The identity chain answers feedback, and a room's nodes are read without rig

C3 tranche 1, first landings. Branch `4genthub-min/u2-native-runner`, commits `3840b8e` (feedback) and
`4203d96` (restore brief, node read, gate refusal). Two reported sites plus one seam they both needed.

## What was removed, and why each one was not a deletion first

**`internal/clientfeedback/feedback.go` — the identity default.** The verb filled `--room`/`--seat`/
`--session` by exec-ing `4genteam whoami --json`. The replacement is `clientruntime.ResolveIdentity`, the chain
the `whoami` verb itself prints: the runtime socket, then a rig's 4genteam daemon, then this shell's own seat
environment. `needsRig` was already false, so the exec was lazy inside `Run`; the parameter is now unused and
named `_`. The refusal and the three usage lines no longer name `4genteam whoami --json`, which is a name that does
not exist after the cutover.

**`internal/clientrestore/restore.go` — the restore brief.** Two defects, not one. The native branch was
unreachable: it tried `SendTextToSeat` only inside a guard for the Python-era `agenthub--<room>--<seat>`
session shape, and this rig's sessions (`4genthub-min-reviewer@4genthub-min`) split into one part, so the
guard was false. Control then reached `4genteam send`, and with rig unresolvable the closure returned nil — a brief
logged "briefed" and never sent. The seam is now `clientruntime.DeliverToSession`, which takes the session AS
GIVEN, prefers the runtime, falls back to the 4genteam daemon, and returns a refusal in words when neither
answers, which the closure returns as its error. The regression test drives the REAL seam; the rest of the
suite stubs it, which is exactly why the defect survived there.

## The seam both needed: reading a room's nodes

`ReadSeatActivity` could not serve the gate and the `ps`-shaped callers: it answers ONE seat's activity word
(`SeatActivity{Word, Present, Read, Why}`), while they need the room's NODE ROWS. The route existed unexported
beside it — `GET /api/ps` to resolve the rig id, then `GET /api/rigs/<rigId>/nodes` — and there is now ONE
exported reading of a room's node records, `clientruntime.RoomNodes(ctx, room) (nodes []map[string]any, ok
bool, err error)`: the runtime first, that daemon route second. A later landing (see the root changelog entry
for 2026-10-14, "the node reading becomes one seam") folded the watchdog and the compact supervisor onto it and
made the daemon route its unexported `daemonNodes`, so no verb can take a rung by accident.

- **Rows are decoded in the CLI's own keys**, which is what every consumer of `rig ps --nodes --json` already
  parses: `logicalId`, `canonicalSessionName`, `rigName`, `runtime`, `sessionStatus`, `startupStatus`,
  `model`. The seat half is derived from the logical id (`<rig>.<seat>`), because the daemon does not send it.
- **It is narrower than `rig ps --json --nodes` in exactly one key: `pid`.** The daemon's node table does not
  carry it, and nothing that consumes this read asks for it — the peak gate reads a seat's name and model
  (`clientoffpeak.SeatsFromNodes`), and the restore, switch and reseat paths read the keys above. A consumer
  that ever needs `pid` has to grow this read deliberately; a test pins its absence so the gap is visible.
- **NO RECORDS and a FAILED READ are different answers**, because a caller that must refuse needs to know
  which one it has. The seam carried a reason string at first, in which no records and success were both the
  empty string; the later landing replaced it with an explicit `ok` plus an `error`, so a caller reads three
  distinct answers instead of inferring one from a string that is empty in two of them. NO RECORDS is `!ok`
  with no error — this shell names no 4genteam daemon, or the daemon answered that it knows no rig by that
  name. A failed read is an error whose sentence names the path and the HTTP status.

## The gate's contract change

`PeakGate` no longer resolves a rig tool, and a reading that FAILS is a refusal in words
(`ExitUnavailable`) instead of `ExitOK`. The old shape returned OK when it could not resolve `rig`, which is
how a peak-time resume could proceed with the gate never having decided — the silent degrade the bar calls
out. A room with NO records still proceeds, and prints nothing: there are no seats to bill, which is the shape
of a room being started for the first time on a machine that has no 4genteam daemon at all, and refusing there
would block the cutover's own target.

## Evidence, and its limit

`go build ./...` and `go vet ./...` clean; `go test ./...` every package ok, including the new
`internal/clientruntime` contract tests (the CLI's key shape, `pid`'s absence, no-records versus a failed
read) and the restore regression test that drives the real delivery seam. The test world in
`internal/clientrestore` now serves its node JSON over the daemon's own route, so the gate and the restore
read through the real seam rather than a stub beside it.

**Limit, stated so nobody inherits more:** the gated verbs were NOT run against the live rig — every one of
them mutates it — so the gate's refusal path is proven in-process against a fake daemon, and the live route
was read once, read-only, to establish the row shape. The end-to-end proof belongs on a lab room.
