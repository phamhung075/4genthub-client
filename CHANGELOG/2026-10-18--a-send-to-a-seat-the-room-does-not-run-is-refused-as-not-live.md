## A send to a seat the room does not run is refused as `not_live`

Batch 8 of the supervisor-port table, part 1: the live-set half, onto `01a2837`.

`commpolicy.DecideLive` and `LiveSet` were already on main from `4e2a173`, and nothing called them: `send`
decided on the room's declared links alone. A team definition keeps a link after the seat it names has gone, so
a message to a stopped seat was authorized and delivered into a session no runner reads — and the sender then
waited for a closure that could not come.

### Changed

- `internal/clientseat/sendpolicy.go`: `authorizeSend` reads the target room's seat states from the same runtime
  socket the delivery goes to (`sendRoomLive`, a seam beside `sendSessions`) and decides with
  `commpolicy.DecideLive`. The policy's own verdicts are unchanged; the live set adds one terminal reason.
- A liveness read that FAILS refuses the send in words naming the room and the socket. A send decided on a
  guessed live set is a send decided on nothing.
- The refusal is audited like any other: one decision line, `allowed:false`, `not_live`. Nothing is delivered.

### Tests

- `TestASendToASeatTheRoomDoesNotRunIsRefusedAsNotLive` runs the real verb against the audit harness with the
  peer's state set to `Stopped`: non-zero exit, `not_live` and `nothing was sent` on stderr, no `SendText` frame,
  one audit line. Induction: with `DecideLive` replaced by `Decide`, it fails with
  `a send to a stopped seat exited 0; stdout "Sent to agenthub--4genthub-min--reviewer"`.
- Three existing transport tests move by one question each, deliberately: `GetRoomStatus` is now asked for the
  live set as well as for the name, so the aimed-daemon sequence is
  `GetRoomStatus,GetRoomLinks,GetRoomStatus,SendText`, the canonical case asks twice and the native case once
  (the native name is still not read as a record). Their comments say which read is which.
- `gofmt -l` empty, `go vet ./...` rc 0, `go test -count=1 ./...` 34 packages ok.

### Not carried in this part

`seatcheck`'s `livePeers` half: main's `seatcheck` no longer delivers a message at all (the verb was deleted, not
stubbed), so there is no second caller to give the live set to.
