## The send tests pin the identity their fakes license

### Fixed

- `internal/clientseat/send_transport_test.go`: every send test in the file states the sender's identity in the
  test, through one helper, `pinGoDevShell(t)` (AGENTHUB_ROOM, AGENTHUB_SEAT, AGENTHUB_SESSION_NAME). Before
  this, five of them pinned only AGENTHUB_SESSION_NAME and left the ROOM/SEAT pair to the environment - and the
  resolver reads that pair (`clientruntime`: the seat's own environment IS the identity when the runtime has not
  named one), so the SHELL THAT RAN THE TEST decided the sender. Measured 2026-10-15, both directions, on
  `TestSendSocketAimsTheVerbAtThatDaemon`: green in go-dev's shell, and in the reviewer's shell
  `send exit = 2, stderr = "error: refused by isolated's communication policy: reviewer may not send a notice to
  dev (no link)"` - the guard correct, the test reading the machine. This is the reviewer's ask on their row of
  that day: `go test -count=1 ./...` must be exit 0 in EVERY seat's shell, not only in one whose identity happens
  to match the fake.
- The same file carried a mis-indented callback in
  `TestSendTranslatesTheCanonicalSessionNameAndPassesTheNativeOneThrough`; `gofmt` now prints nothing for it.

### Testing

- `go test -count=1 -run TestSendSocketAimsTheVerbAtThatDaemon ./internal/clientseat/` with
  `AGENTHUB_ROOM=4genthub-min AGENTHUB_SEAT=reviewer AGENTHUB_SESSION_NAME=agenthub--4genthub-min--reviewer`:
  ok (it FAILED with those exact variables before the fix).
- `go test -count=1 ./...` with those same reviewer-shell variables: exit 0, every package ok. Before the fix
  the same command was 28 ok / 1 FAIL, which is what the wiring gate measured.
- The five other send tests in the file report ok in that shell too (same command, whole package).
