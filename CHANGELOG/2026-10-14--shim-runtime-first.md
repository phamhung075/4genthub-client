## The shim's verbs resolve the runtime socket first (U3)

### What was wrong

`whoami`, `queue` and `send` resolved their transport from the SEAT ENVIRONMENT, not from what was actually
running: a shell carrying `OPENRIG_SESSION_NAME` (and any `OPENRIG_URL`) sent every one of them straight to
the OpenRig daemon over HTTP, and this client's own runtime — the daemon that holds the seat's rail — was
never asked. Reproduced on the seat machine with a dead `OPENRIG_URL` and the runtime up, from the pre-change
tree (`/tmp/shim-check-old`, built from `HEAD` before the change):

```
$ OPENRIG_URL=http://127.0.0.1:1 /tmp/shim-check-old whoami --json
4genteam whoami: the OpenRig daemon at http://127.0.0.1:1 is not answering: Get "http://127.0.0.1:1/api/whoami?sessionName=4genthub-min-go-dev%404genthub-min&compact=1": dial tcp 127.0.0.1:1: connect: connection refused
exit=3

$ OPENRIG_URL=http://127.0.0.1:1 /tmp/shim-check-old queue list
4genteam queue: the OpenRig daemon at http://127.0.0.1:1 is not answering: Get "http://127.0.0.1:1/api/queue/list?activeOnly=1&compact=1&destinationSession=4genthub-min-go-dev%404genthub-min": dial tcp 127.0.0.1:1: connect: connection refused
exit=3
```

### What it is now

The preference order lives in one file, `internal/clientruntime/transport.go`:

```
runtime present (with or without an OpenRig daemon also up)  -> the RUNTIME answers;
runtime absent, OpenRig daemon present                       -> the OpenRig path answers, unchanged;
neither                                                      -> the refusal names the RUNTIME.
```

`ResolveTransport` reads that from a dial of `DefaultSocketPath()` (the one socket resolution point, override
`OPENRIG_RUNTIME_SOCKET`), so a verb reports the transport that really carried it. `FallbackMissing` builds the
refusal, naming the runtime's socket and the knob, and the OpenRig clause from this machine's own
configuration — `no OpenRig daemon is configured in this environment (set OPENRIG_URL or OPENRIG_PORT)` when
none is named, `the OpenRig fallback at <url> is not available either` when one is named but did not answer.

`whoami` keeps its identity-recovery chain on top of that: a runtime that ANSWERS with an identity is the
answer; a runtime that cannot place this seat falls through; a daemon that ANSWERS (including its 404 for an
unknown session) keeps its own words and code; and with no daemon at all the seat's environment is the
document, `resolved_by: environment`.

### After, from the same shell (the acceptance)

```
$ GOCACHE=$(pwd)/.gocache TMPDIR=$(pwd)/.gotmp go build -o /tmp/shim-check ./cmd/4genteam
(exit 0)

$ OPENRIG_URL=http://127.0.0.1:1 /tmp/shim-check whoami --json
{
  "resolved_by": "environment",
  "identity": {
    "rigName": "4genthub-min",
    "memberId": "4genthub-min-go-dev",
    "runtime": "omp",
    "sessionName": "4genthub-min-go-dev@4genthub-min",
    "logical_id": "4genthub-min-go-dev",
    "role": ""
  },
  "peers": [],
  "edges": {
    "outgoing": [],
    "incoming": []
  },
  "transcript": {
    "enabled": true
  }
}
exit=0

$ OPENRIG_URL=http://127.0.0.1:1 /tmp/shim-check queue list
4genteam queue: the runtime at /home/daihu/.openrig/state/omp/4genthub-min-go-dev@4genthub-min/.config/4genthub/run/runtime.sock refused it: invalid request JSON: unknown variant `QueueList`, expected one of `StartRoom`, `StopRoom`, `StopSeat`, `RestartSeat`, `SendText`, `GetRoomStatus`, `ListAllRooms`, `Whoami`, `ForceCompact`, `Ping` at line 1 column 21
exit=1
```

That second answer is the runtime's OWN words, which is the point: the verb went to the runtime. The daemon
installed on this machine (`/home/daihu/.local/bin/agenthub-runtime`, the process serving this seat's socket)
predates the `Queue*` IPC verbs that this tree's `rust/agenthub-runtime` already implements and that
`CHANGELOG/2026-10-13--native-room-queue.md` documents, so the refusal is "unknown variant `QueueList`" rather
than a list. Against a runtime built from this tree, `QueueList`/`QueueShow` are the verbs this verb sends —
pinned, frame and all, by `TestQueueListPrefersTheRuntimeEvenWhenADaemonIsUp`.

The banned sentence is gone from the queue path:

```
$ OPENRIG_URL=http://127.0.0.1:1 /tmp/shim-check queue list 2>&1 >/dev/null | grep -c "the OpenRig daemon at http://127.0.0.1:1 is not answering"
0
```

A shell that carries NO seat identity at all is still refused rather than invented (the runtime is reachable
but cannot resolve an unnamed seat, and the daemon at :1 does not answer):

```
$ env -u OPENRIG_SESSION_NAME -u AGENTHUB_SESSION_NAME -u AGENTHUB_ROOM -u AGENTHUB_SEAT OPENRIG_URL=http://127.0.0.1:1 /tmp/shim-check whoami --json
4genteam whoami: no runtime is answering at /home/daihu/.openrig/state/omp/4genthub-min-go-dev@4genthub-min/.config/4genthub/run/runtime.sock (set OPENRIG_RUNTIME_SOCKET, or start agenthub-runtime with --socket /home/daihu/.openrig/state/omp/4genthub-min-go-dev@4genthub-min/.config/4genthub/run/runtime.sock daemon); no seat environment names this seat either (set AGENTHUB_ROOM and AGENTHUB_SEAT, or OPENRIG_SESSION_NAME)
exit=3
```

### Testing

```
$ go test ./internal/clientruntime/ ./internal/clientseat/ -count=1
ok  	github.com/phamhung075/4genthub-client/internal/clientruntime	0.715s
ok  	github.com/phamhung075/4genthub-client/internal/clientseat	0.377s

$ go test ./... -count=1       # 24 packages with tests, none failing
$ go build ./... ; go vet ./internal/clientruntime/ ./internal/clientseat/ ./cmd/... ; gofmt -l internal/clientruntime internal/clientseat
(clean)
```

The eight new tests were run against the PRE-CHANGE verb files, restored from `HEAD` into the worktree
temporarily, and fail there — which is what makes them instruments rather than decorations:

```
--- FAIL: TestQueueListPrefersTheRuntimeEvenWhenADaemonIsUp
--- FAIL: TestQueueListNamesTheRuntimeWhenNothingAnswersEither
--- FAIL: TestQueueListReportsTheRuntimesOwnRefusalBySocket
--- FAIL: TestWhoamiJsonAnswersFromTheSeatEnvironmentWithNoDaemonAtAll
--- FAIL: TestWhoamiPrefersTheRuntimesIdentityOverTheDaemon
--- FAIL: TestSendPrefersTheRuntimeSocket
--- FAIL: TestSendJsonPrintsTheRuntimesOwnAnswer
--- FAIL: TestSendNamesTheRuntimeWhenNoTransportAnswers
```

Both runtime fakes are real Unix sockets in a scratch directory (`fakeRuntime`), answering the client's own
line envelope, so nothing in these tests touches the live rig or the developer's daemon. The daemon-path tests
that existed before now call `noRuntimeSocket(t)` for the same reason: the seam asks the runtime FIRST, so a
test of the fallback has to say that no runtime is here.

### Not verified

- `queue list` listing rows from a runtime daemon built from this tree. The runtime serving this seat is the
  older installed binary (no `Queue*` variants), and starting a second daemon was left alone; the frame and
  the rendering are pinned against a fake that speaks the daemon's own envelope (the row JSON in
  `internal/clientruntime/queue_test.go` is the daemon's serialization).
- The live rig's daemon-hosted path was exercised only through its tests, not by a run against the rig.
