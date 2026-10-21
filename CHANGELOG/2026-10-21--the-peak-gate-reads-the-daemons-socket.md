# The peak gate reads the daemon the verb was given, not the machine's default

`room reseat` and `heal` both take `--socket PATH`, stop a seat and start it again. The start is a launch, so
both ask the DeepSeek peak gate first - and the gate read the room's node records through
`clientruntime.RoomNodes`, which builds a client from the DEFAULT socket. The flag never reached it.

Two consequences, and the first is why this was visible at all. Client `main` (`6c25e31`) had a red Go job
since 2026-10-20T20:53Z (runs 39495814105, 39498910845, 39503871523): `TestRoomReseatWritesStopSeatThenRestartSeat`,
`TestRoomReseatCarriesTheHubsPolicyDocument` and `TestRoomReseatReportsASeatThatDidNotComeBack` point the verb
at a fake daemon and expect exit 0 (and 2 for the partial case). The gate dialled the default socket instead,
found no daemon in CI, and refused with exit 3. The tests were right; the gate was reading the wrong daemon.
And it is a real defect on a machine with more than one: `room reseat --socket PATH` could refuse a fine room,
or pass a DeepSeek room at peak, because the decision was taken over whatever this machine's default daemon
happened to record.

The fix is one path, not a second one. `clientruntime.RoomNodesAt(socket, room)` is the reading with the
socket passed, and `RoomNodes(room)` is now `RoomNodesAt("", room)` - an empty socket is the default, which is
what the in-process agent, the supervisor, the idle guard and the restore path want. `clientlifecycle`'s
`roomNodes` seam takes `(socket, name)`, `PeakGateAt(socket, name, verb, allowPeak, stderr)` is the exported
gate with the socket, and `PeakGate` is `PeakGateAt("", ...)` - `up` and `compact` keep the default socket
because neither takes a `--socket`. `clientroom` calls `PeakGateAt(inv.client.SocketPath, ...)` and
`clientheal` passes `client.SocketPath` through `gateRestart`.

The failure path is unchanged: a reading that cannot be taken still refuses in words at exit 3, and a
malformed schedule is still exit 2. What changed is WHICH daemon is asked.

## Evidence

- `internal/clientroom/room_test.go`:
  - `TestRoomReseatGatesTheDaemonTheSocketFlagNames` is the defect with two daemons answering differently: the
    one `--socket` names hosts a DeepSeek seat, the environment's hosts an omp seat. At peak the refusal names
    the DeepSeek seat, and the environment daemon's request log is EMPTY - it was never asked. Observed RED on
    `origin/main` (`exit 0, want 3`: the gate passed the room over the environment's daemon) and green here.
  - `TestRoomReseatRefusesAtPeakBeforeTheSeatIsStopped` and `TestRoomReseatProceedsAtPeakWithTheFlag` no longer
    pin `AGENTHUB_RUNTIME_SOCKET` at the fake daemon: the flag is the only socket either run is given, so a
    refusal (and a pass) is the gate having read the flag's daemon.
  - The three CI-red tests register the `GetRoomStatus` frame the gate now honestly sends, and assert it first
    in their request sequence.
- `internal/clientheal/heal_test.go`: `stubGate` carries the socket, and both gate tests assert the recorded
  call names the socket `machineDeps` was built with - so heal's flag reaches its gate too.

Run: `env -u AGENTHUB_RUNTIME_SOCKET XDG_RUNTIME_DIR=/tmp/nosock go test ./... -count=1` (no daemon at the
default socket, the CI configuration) - all packages ok. `go test ./... -count=1` with the box's own daemon -
green. `cargo test --offline` in `rust/agenthub-runtime` - 192 + 1 + 20 + 1 passed, no Rust change.
