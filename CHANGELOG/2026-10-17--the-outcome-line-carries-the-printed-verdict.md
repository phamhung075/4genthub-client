# The outcome line carries the printed verdict

A send's audit pair had two halves that disagreed. The printed line refuses to claim a delivery for every
runtime answer except `submitted` (`clientruntime.RuntimeSendLine`, since 2026-10-14), while the outcome line
`runtimeSend` wrote for the same send was `delivered` for **every** answer the daemon accepted
(`internal/clientseat/reseat.go`). A reader of the audit log therefore saw a delivery the caller had just been
told was unconfirmed.

### Fixed

- **The outcome line reads the runtime's own verdict.** `clientruntime.SendVerdict` is now the one reader of the
  answer document (the vocabulary moved beside it: `VerdictSubmitted`, `VerdictRelayed`,
  `VerdictPastedUnconfirmed`), and `runtimeSend` audits `delivered` only for `submitted`. `relayed`, an answer
  with no verdict, and an unreadable answer are audited `delivery_unconfirmed` - the same three facts the
  printed line already named.
- Measured at client `84bcd8c` with the built binary and a daemon stub: SendText answered
  `{"delivered":…,"outcome":"relayed"}`; stdout said `Relayed …, not claimed as delivered` and exited 0, and
  the audit line written for that send said `"Outcome":"delivered"`. Same for an answer with no verdict. After
  this change both are `delivery_unconfirmed`.
- An outcome line that cannot be written is still a failed exit, and its sentence no longer claims the message
  was delivered: the sentence states what could not be recorded, which is true of every verdict.

### Testing

- `go test -count=1 ./...` in the client: green. `internal/clientseat` gained
  `TestASuccessfulSendAuditsTheVerdictItPrints`: three accepted answers - `submitted`, `relayed`, and one with no
  verdict - each run through the real verb against a socket, asserting the exit code, the printed line, and the
  outcome line; the two unconfirmed answers must not say `Sent to`, and the audit must repeat what stdout said.
- The policy harness's default answer now carries the runtime's real success document
  (`internal/clientseat/sendpolicy_test.go`), so its two `delivered` assertions still measure a delivered send instead
  of pinning the answer shape that produced the disagreement.

### What this does not do

- It does not make `relayed` deliverable or reportable: the runtime does not report the agent's own answer back
  to the caller, so a relayed frame remains unconfirmed until that channel exists.
- It does not touch the printed lines, the exit codes, or the decision line: the change is which outcome token
  the second audit line carries.
