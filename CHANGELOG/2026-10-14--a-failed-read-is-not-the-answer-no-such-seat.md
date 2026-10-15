# A failed read is not the answer "no such seat"

Found live, not by reading: while delivering the C3 report, `4genteam send 4genthub-min lead <text>` answered

    error: no record of 4genthub-min/lead: the room's runtime does not list that seat; no name was composed - a
    seat's session name is read from the record that owns its pane, never built from a convention

and the truth was that there was **no socket to ask**:

    cannot connect to agenthub-runtime at /mnt/wslg/runtime-dir/agenthub.sock: dial unix ...: no such file or directory

## What was wrong

`internal/clientruntime/seataddress.go` collapsed a failed read into the empty string:

    statuses, err := NewClient("").GetRoomStatus(room)
    if err != nil {
        return ""            // the error is discarded here
    }

and `ResolveSeatAddress` turned `""` into the no-record refusal above. So a transport failure was reported as a
positive fact about the room, sending the operator to the room's definition when the thing to repair was the
socket — the same shape the node seam (`RoomNodes`), the peak gate and the watchdog were each fixed against this
week: **an unreadable record is not an answer about the room.** The file's own doc comment already promised the
distinction ("a seat the runtime does not list is a refusal that names it"); only the error path broke it.

## The change

- `seatAddressFromRuntime` answers THREE things — the name, whether the room's record listed the seat, and the
  error the read made — and `ResolveSeatAddress` reports each as itself:
  - `err != nil` → `the session name of <room>/<seat> could not be read: <the read's own words>`, `ExitUnavailable`.
  - found → `SeatAddress{SessionName, Source: "the runtime's room record"}` (unchanged).
  - not found → the existing no-record refusal, word for word (unchanged).
- One package-level var, `seatStatuses`, is the room read — the repo's rule for a seam (a var, not an interface
  invented for a test), so both answers are driveable without a daemon.

## Evidence

- `TestAFailedReadIsNotTheAnswerNoSuchSeat` (new) points the runtime socket at a path that does not exist and
  requires the refusal to name the connect failure and NOT to contain "does not list that seat".
  **Falsified, not read**: with the error collapsed back to `""`, the test fails with exactly the live message
  above — the defect reproduced in a test — and `seataddress.go` was then restored byte-identically (md5 compared).
- `TestTheRoomRecordAnswersBothWays` (new) pins that the fix did not soften the other branch: a room that answers
  without the seat still gets the no-record refusal, and one that lists it still yields the session name and its
  source.
- Measured on a binary built from this change:
  - `AGENTHUB_RUNTIME_SOCKET=/tmp/no-such-plane.sock 4genteam send 4genthub-min lead probe` → `error: the session
    name of 4genthub-min/lead could not be read: cannot connect to agenthub-runtime at /tmp/no-such-plane.sock:
    dial unix /tmp/no-such-plane.sock: connect: no such file or directory`.
  - `4genteam send 4genthub-min definitely-not-a-seat probe` → the unchanged no-record refusal.
- `go build ./...` 0, `go vet ./...` 0, `go test ./...` 25 packages ok, 0 FAIL.

## The outage this was found in, recorded

Measured 2026-10-14 between 19:53Z and 19:58Z: the machine runtime daemon
(`agenthub-runtime --state-dir ~/.config/4genthub --socket /mnt/wslg/runtime-dir/agenthub.sock daemon`, pid 24859)
was gone and its socket file unlinked, while all five seat runners (24886 lead, 24978 go-dev, 25094 fe-dev, 25225
reviewer, 27123 architect) stayed alive and listening on their own per-room sockets; `4genteam ps` still showed
5 seats running. Every verb needing the shared socket failed in that window: `send`, `room queue`, the
`RIG SEAT` resolution. At 19:58Z a NEW daemon (pid 6512, same state dir and socket) owned the socket again and
`send` was delivered — the room's state (`spec.json`, `queue.sqlite`, per-seat sockets) was intact, so the
restart re-adopted the running seats.

**Mechanism NOT established.** One candidate worth measuring later: the C4 harness's rollback kills *every*
process matching `[/]agenthub-runtime .*daemon` by cmdline, which cannot tell the lab daemon from this machine's;
run 11 printed only `SIGTERM 31305` (a lab daemon) while its own footer claims "the live room ... were never
addressed", and pid 24859 was still alive after run 10's rollback. Candidate, not cause.
