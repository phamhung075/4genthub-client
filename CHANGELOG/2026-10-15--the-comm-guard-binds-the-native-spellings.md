## The comm guard binds the native spellings, not the retired shim

### What was wrong
`commpolicy.BypassSuspected` — the predicate behind `seatcheck audit-scan`, which reports a direct send that
skipped the checker — matched `rig send|queue|broadcast` and `tmux send-keys|paste-buffer` only. `rig` is not
the client: `~/.local/bin/rig` does not exist and `rig` is not on this host's PATH at all, while the seat's
native send verbs are `4genteam send`, `4genteam input` and the rail's `4genteam room queue create`. So the
control bound nothing on the path a seat on this host actually sends over.

Measured, same binary, same input file, before and after (`seatcheck audit-scan --known seatcheck --file`, the
lines are the room's own recorded sends): **before** — the four native lines are all silently GREEN, only the
`tmux send-keys` line is flagged; **after** — `4genteam send <seat> <text>` (both arities), `4genteam room
queue create …` and `tmux send-keys …` are flagged, and `4genteam seatcheck send …` and `4genteam ps …` are
not.

### Changed
- `internal/commpolicy/policy.go`:
  - the deny list names the native client: `4genteam send`, `4genteam input`, and `4genteam room queue
    create` (the rail delivers to a seat's mail the way `rig queue` did, and it never met the checker). The
    retired `rig` spellings stay: `rig` on PATH is a foreign CLI and must not carry a seat's message.
  - the allow list (`known`) accepts a **shape** as well as a program name: an entry is either `prog` (any
    invocation of it, unchanged) or `prog sub-verb` (that shape alone), so naming the checker
    (`--known "4genteam seatcheck"`) cannot become a licence for `4genteam send` beside it — the same class of
    hole the retired-verb pin hit with an allowlist entry that matched the verb's own form.

### Tested
- `internal/commpolicy/policy_test.go`: 20 native cases added to `TestBypassSuspected` (send by session, by
  rig+seat, absolute path, after an env assignment, collapsed spaces, `input`, the rail's `create`, and the
  non-sends: the checker, `audit-scan`, `queue list`, `queue claim`, `room stop`, `ps`, bare `4genteam`, a
  truncated `room queue`), plus `TestBypassSuspectedKnownShapeIsNotALicence` (a `prog sub-verb` entry allows
  that shape and no other; a bare program name keeps its whole-program meaning; another program's entry does
  not touch this one) and the nil-known case extended to the native send.
- `go test ./internal/commpolicy/ ./cmd/seatcheck/ -count=1` ok, `go build ./...` ok, and `go test ./...` in
  the client: **all 28 packages ok**.
- Not done here, by scope: the guard is still not wired onto the send path. `internal/commpolicy` is reached
  from `cmd/seatcheck` and `internal/clientbridge` only — the reviewer's `FINDING-comm-guard-spelling-hole`
  stays C5's acceptance, and this commit closes its spelling half by measurement.
