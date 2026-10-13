## The seat guard's identity and delivery are native, and the site inventory is NOT disposed: four declared sites, plus three seams the assignment key never listed

### What was wrong

`cmd/seatcheck` is what a seat runs instead of `rig send`, and it reached the two halves of that job by
exec-ing the OpenRig CLI:

```go
out, err := exec.Command("rig", "whoami", "--json").Output()      // main.go rigWhoami
cmd := exec.Command("rig", "send", "--", target, text)            // main.go rigSend
```

After the cutover there is no `rig` on PATH to exec — and on a machine with this client's own rename
installed, the name is a symlink to the client, so the "fallback" was a self-call. The identity half also had
a rung the exec never had: since c55fa87 the client's own `whoami` resolves the runtime socket first and falls
back to the seat's own environment, so an identity exists with no daemon at all.

### What it is now

The chain moved out of the verb and became one exported function, `internal/clientruntime.ResolveIdentity`
(transport.go): the runtime socket first, a rig's OpenRig daemon second, the seat's own environment
(`AGENTHUB_ROOM`/`AGENTHUB_SEAT`, else `OPENRIG_SESSION_NAME`) last. `whoamiCommand.Run` PRINTS what it
returns and `cmd/seatcheck` REASONS about it, so the two cannot resolve identity differently. A document the
daemon answered with is still carried whole (`ResolvedIdentity.Document`), because the CLI prints that payload
unmodified and projects it compactly unless `--full`; the runtime's and the environment's answers are values
the one rendering (`printWhoami`) marshals.

```go
func rigWhoami() (identity, error) {
	resolved, err := clientruntime.ResolveIdentity(context.Background(), "", false)
	if err != nil {
		return identity{}, err
	}
	return identityFromDocument(&resolved.Response)   // the roster rule is still the guard's
}
```

The delivery became `internal/clientruntime.DeliverToSession` — the same runtime-first / daemon-fallback
chain the `send` verb runs, with NO daemon-only switch set, which is exactly the shape `rig send SESSION TEXT`
had (no `--verify`, `--wait-for-idle`, `--dangerously-interact`, `--raw`). It is exported from
`clientruntime` rather than from `clientseat` because `reseat.go` was held by the author of the U3 gate fix
while this landed; the flag-aware send verb keeps its own path there.

```go
func rigSend(target, text string, stdout, stderr io.Writer) int {
	return clientruntime.DeliverToSession(context.Background(), target, text, stdout, stderr)
}
```

The failure shape is ONE, which is what the ruling asked for: whatever the delivery answers (1 refused, 3 no
transport, a transport's own code), `runSend` maps every non-zero to `exitDeliveryFailed` (5), so a policy
result (2/3/4) can never be read as a delivery failure and a transport's code never leaks as the guard's exit.
The `delivery failed` line no longer names `rig send`.

`whoami` also asks the runtime for this seat's NATIVE session name (`agenthub--<room>--<seat>`, the shape the
runtime's own helper renders) when the environment names the halves and no session, instead of an empty
session — the reading a bare empty session invites is "the first seat the room lists", which is not
necessarily this one. `AGENTHUB_SESSION_NAME` still wins when it is set, and an explicit `--session` still
wins over both.

### Verified

```
go build ./...   → rc 0
go vet ./...     → rc 0
go test ./... -count=1
  ok cmd/4genteam, cmd/seatcheck, internal/clientbridge, clientcmd, clientenv, clientevidence,
     clientfeedback, clientlifecycle, clientoffpeak, clientoutbox, clientpolicy, clientps,
     clientrestore, clientroom, clientruntime, clientseat, clientservices, clientskills,
     clientsync, clientteam, clientwatch, commpolicy, seatlog, secretscan   (24 packages)
```

The new tests are the acceptance's instrument and they fail on the pre-change code. Run against a pristine
`HEAD` tree with the SAME test file (`git archive HEAD | tar -x -C /tmp/u2-check/old`, the old test files
removed, `exec_test.go` copied in):

```
--- FAIL: TestIdentityIsResolvedNativelyWithNoRigOnPath   exec_test.go:173: rigWhoami: parse rig whoami: unexpected end of JSON input
--- FAIL: TestIdentityComesFromTheRuntimeSocket            exec_test.go:195: rigWhoami: parse rig whoami: unexpected end of JSON input
--- FAIL: TestIdentityFallsBackToTheOpenRigDaemon         exec_test.go:231: rigWhoami: parse rig whoami: unexpected end of JSON input
--- FAIL: TestDeliveryGoesToTheRuntimeSocket              exec_test.go:259: stdout = "", want the delivery's own line
--- FAIL: TestDeliveryFallsBackToTheOpenRigDaemon         exec_test.go:309: asked "", want the daemon's send route
--- FAIL: TestDeliveryWithNoTransportAtAllIsOneRefusal    exec_test.go:327: rigSend = 0 with no transport; stdout = ""
```

The instrument is a fake `rig` FIRST on PATH that records its own execution, and its record not existing:
that is the only honest way to assert "no process was started", and it is what fails above (the old seam
execs it and parses its empty answer). The tests also install a fake runtime socket or a fake OpenRig daemon,
so the native sources are arranged rather than assumed, and every test pins the runtime socket AWAY from this
machine's real one.

### The site inventory, and why it is not disposed

Keyed as the merged tree's `wt/r7-shim` (`go build`-identical line numbers to the assignment; the worktree's
own locations differ because earlier commits already moved some of them).

| site (r7-shim) | what it was | disposition |
| --- | --- | --- |
| `internal/clientseat/reseat.go:137` | `rigNodes`: `rig ps --nodes --rig RIG --json` | already converted before this row (234e162) — now `roomNodes` via `clientruntime.GetRoomStatus` |
| `internal/clientseat/reseat.go:258` | `sendCommand`'s `rig send` fallback | already deleted before this row (d777b52) — and the same functions were held by the U3 gate fix author here |
| `internal/clientseat/sync.go:214` | `seatActivity`: `rig ps` → `agentActivity.state` | NOT CONVERTED — no native source exists; see "the decision" below |
| `internal/clientseat/sync.go:268` | `relaunch`: `rigID`/`rig launch` through the CLI | already converted before this row (0905d48) — now `clientruntime.RestartSeat` |
| `internal/clientlifecycle/supervisor.go:301` | `rigIdle`: `rig ps` → `agentActivity.state` | NOT CONVERTED — no native source exists; landed DECLARED EXCEPTION (7d07eca, 10dc456, 0905d48) |
| `internal/clientlifecycle/supervisor.go:328` | `rigSay`: `rig send … --wait-for-idle 120` | already converted before this row (7d07eca) — now `runtimeSendText` |
| `internal/clientlifecycle/supervisor.go:349` | `rigAbort`: `rig send … /abort --raw` | already converted before this row (056d155) |
| `internal/clientsync/respawnverb.go:56` | `runRigCapture`: `rig ps`, `rig seat launch` | NOT CONVERTED — two thirds are convertible, the verb's DECISION is not; see below |
| `internal/clientcmd/command.go:134` | `RequireRig`'s `LookPath("rig")` (the eleventh) | NOT CONVERTED — the shared platform matrix, not a site fix; see below |
| `cmd/seatcheck/main.go:346` | `rigWhoami`: `rig whoami --json` | CONVERTED — `clientruntime.ResolveIdentity` + `identityFromDocument` |
| `cmd/seatcheck/main.go:418` | `rigSend`: `rig send -- TARGET TEXT` | CONVERTED — `clientruntime.DeliverToSession` |
| `internal/clientsync/bundleverb.go:42-48` | `bundleRunRig`: `exec.LookPath(argv[0])` then `exec.Command(path, argv[1:]...)`, argv built at :192 as `rig bundle create …` | NOT CONVERTED, and ABSENT FROM THE ASSIGNMENT'S KEY — no conversion attempted in this unit; the runtime's own IPC surface (listed under "the decision") has no bundle operation |
| `internal/clientsync/connectorverb.go:354-355` | `runRig`: `exec.CommandContext(ctx, "rig", args...)` — the only literal `"rig"` CommandContext anywhere in the tree | NOT CONVERTED, and ABSENT FROM THE ASSIGNMENT'S KEY — no conversion attempted in this unit |
| `internal/clientsync/switchverb.go:53-58` | `switchRunRig`: `exec.LookPath(argv[0])` then `exec.Command(path, argv[1:]...)`, called as `rig ps --json --nodes --rig ROOM` at :350 and again at :322 | NOT CONVERTED, and ABSENT FROM THE ASSIGNMENT'S KEY — the read is `GetRoomStatus`-shaped (the same substitution `respawn`'s canonicalSession half uses), but the swap is the verb's own decision and is not in this unit |

**A NOTE ON HOW THIS TABLE WAS BUILT, because it is why the count kept moving.** Three of these seams take their
PROGRAM from an argument list (`argv[0]`), not from a literal in the exec call: a grep for `exec.Command("rig"` cannot
see them, and a grep for the name `rig` sees them only in the argv builder, which may live in the same file or
another. Any count built from one pattern is therefore a count of one pattern's blind spots as much as of the work.
This table names sites and their dispositions; the reviewer's own pre-pin site list, built from the seams and who
supplies their argv, is the artifact the gate reads.

### The decision the remaining sites need

Three of the four are blocked on ONE missing capability, and it is the same one the two landed
declarations name: **a native per-seat agent-activity read**. The runtime's IPC surface is
`StartRoom`, `StopRoom`, `StopSeat`, `RestartSeat`, `SendText`, `GetRoomStatus`, `Whoami`, `ForceCompact` and
the queue routes (`internal/clientruntime/ipc.go`, `queue.go`), and `SeatStatus` carries `state`
(Running/Stopped), `detail`, `last_active` — not `agentActivity`. The runtime's own idle reading
(`seat_is_idle`) is a private read of the seat's state file and is NOT on the wire. So `rigIdle` and
`seatActivity` cannot be converted without inventing a state source (the thing their comments forbid), and
`respawn`'s `agentIsGone` (`agentActivity.state == "unknown" && reason == "no_runtime_hook"`) has no native
substitute either — only its `canonicalSession` (`GetRoomStatus().SessionName`) and its launch
(`clientruntime.RestartSeat`) halves do.

Deleting those execs instead of converting them is the ruling's other branch, but it is a DATE change rather
than a mechanical one, because the live rig still has a `rig` on PATH today: it would stop reseat's quiet gate
and the supervisor's quiet-triggered compaction now, one release before the cutover, and turn the hard-limit
path into an interrupt loop for every non-omp runtime. That is a product call for the seat, and it is exactly
what the two declarations were written to make visible.

`internal/clientcmd/command.go`'s `LookPath("rig")` is the shared platform matrix: the supervisor's `Nodes`
seam (`supervisor.go:100`, runtime-first with a `rig ps` fallback), `PeakGate`, and every remaining rig-tool
verb (bundle create, seat switch, connector, bridge, watch grid) reach `rig` through it. Deleting it is the
cutover of those verbs, not a line in this row — and deleting its in-scope consumer in the supervisor would
remove the peak gate's reading on a rig whose runtime cannot answer, which the gate's own design treats as
"nothing to refuse" rather than as a refusal.
