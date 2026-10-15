## The seat checker stops sending

### What was wrong
`cmd/seatcheck` carried a **second** send path. `4genteam send` (`internal/clientseat`) already builds the
sending seat's policy at send time, decides, audits and delivers; `seatcheck send` was a copy of the decision
that refused every send it was given, and the only writer of the audit pair's decision half whose delivery was
another process's. It is DELETED — the verb, its delivery, the pinned-policy read, and the `README.md` /
`docs/AGENT-SETUP.md` lines that taught it — not deprecation-stubbed and not aliased. `send` is an unknown
subcommand now, and its refusal names the surviving route (`4genteam send`).

### What that audit work was for (the history this removal must not erase)
The checker's audit work existed for ONE hole, recorded on 2026-10-10 (late night) and fixed then: a send that
never reached a decision wrote NO line at all — `seatcheck send` exited 2 on a policy path resolved under the
seat state dir and recorded nothing — while the box this guard exists to satisfy claims every message is
audited, allowed or denied, with its reason. So the three failures that can block a decision — `policy
unreadable: …`, `policy unparsable: …`, `policy belongs to seat "x", not "y"` — were given an `Allowed:false`
line carrying the reason *before* the same non-zero exit. A seat DIRECTORY that does not exist stayed refused
WITHOUT a line: there is no store to append to, and the guard does not create one to record its own refusal. A
failed audit WRITE kept exit 1, because the reason is then unrecorded and the caller must see that. The same
entry's sibling work pinned the NAME, because the seat's rendered settings allowed exactly
`Bash(seatcheck send:*)` and `pull` refuses when `seatcheck` does not resolve to the binary this client built.

### What stops being audited, and why nothing needs it
What stops is the checker's own **refusals**, and those refusals were the retired implementation's. With no
`send` verb there is no policy read here, no decision, no delivery and no pinned store to append to: an unknown
subcommand decides nothing, so there is no decision whose line is owed. Nothing a seat can legitimately send
loses a line — the checker never delivered a message, so it never wrote the half that describes one.

The audit is not lost: it moved to the ONE process that can close the pair, `internal/clientseat`.
`4genteam send` builds the sending seat's policy at send time from the room's declared links (`authorizeSend`),
decides (`commpolicy.Decide`), writes the decision line, delivers (`runtimeSend`, `internal/clientseat/reseat.go`),
and writes the outcome line (`handle.outcome` → `delivered` / `delivery_failed`). Both halves therefore belong
to the process that sends, which is exactly the half the checker could never own: its delivery was another
process's, so an allowed decision line could be left with no outcome and the outcome unknown. That is the hole
`commpolicy.AuditRecord.Outcome`'s comment describes, and it no longer names the retired process as the writer.

### Removed / Changed
- `cmd/seatcheck/main.go`: the `send` verb and everything only it reached — identity resolution, the pinned
  policy read, the decision, the delivery, the decision/outcome writes. What is left is `audit-scan`, which only
  reads observed command lines for sends that never met the audited path.
- `cmd/seatcheck/exec_test.go`: deleted with the verb (its cases were the verb's delivery seam, its refusals and
  its audit pair); `cmd/seatcheck/main_test.go` is cut to help, the unknown-subcommand refusal, and `audit-scan`.
- `cmd/4genteam/main.go`: `4genteam seatcheck --help` no longer names a `send` spelling — it points at
  `seatcheck audit-scan` and at `4genteam send`.
- `README.md`, `docs/AGENT-SETUP.md`: the port record no longer teaches the verb; it says the checker does not
  send and that `4genteam send` is the audited path.
- `internal/commpolicy/policy.go`: the `Outcome` comment says "the sender stopped (or could not write the
  outcome)", not the retired process.
- `internal/clientservices/services.go` and `services_test.go`: `gofmt` realignment of the `Agent` constant and
  its expectation, so `gofmt -l cmd internal` is empty (a pre-existing misformat on the acceptance path).

### Verified
- `go run ./cmd/seatcheck send --to x --intent task -- hi` -> stderr `seatcheck: unknown subcommand "send"` and
  a usage naming `4genteam send`; the built binary exits 2 (`rc != 0`, and the refusal names the route).
- `go run ./cmd/seatcheck --help` -> usage on stdout, rc 0. `go run ./cmd/seatcheck audit-scan --known seatcheck
  --file <one observed line>` still flags it — `bypass-suspected: 4genteam send bob hello`, binary rc 4 — and
  exits 0 on a clean file.
- `gofmt -l cmd internal` empty · `go build ./...` rc 0 · `go test ./...` rc 0 (30 packages ok).
- The surviving route itself was not re-implemented or re-measured here: `4genteam send SESSION MESSAGE` /
  `4genteam send [--from NAME] RIG SEAT MESSAGE`, usage and policy denial on exit 2, link-source/audit failures
  and a delivered-but-unrecorded outcome on exit 3, a runtime delivery refusal on exit 1.
