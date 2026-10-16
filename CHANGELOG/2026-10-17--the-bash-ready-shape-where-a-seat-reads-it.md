# The bash ready shape, where a seat reads it

The `bash` tool's long-lived-service shape was documented nowhere a seat reads, and two refusals follow
from it: `ready` must be an object and never a boolean, and a named service takes no `async`/`timeout`.
Over the window 2026-10-15T22:51Z-2026-10-16T22:51Z the room's own seat logs carry 20 service-mode
refusals - architect 6, fe-dev 3, go-dev 4, reviewer 7 - and no `ready`-boolean row at all: a boolean
`ready` is answered while the call is validated, before dispatch, so it never enters `tool-calls.jsonl` and
is counted from the seats' own reports instead (the architect's row names 11 of them beside its 6
service-mode ones; the reviewer's row names 11 service-mode ones, all-time). Hub task
`1b0740aa-3c3f-4a30-9172-7037c5de3468`; the harness half of the gap is the owner's feedback record
`4f60f2cc`.

### Changed

- **`skills/share/verification-before-completion/SKILL.md` gains "Running the thing: services and long
  jobs"**, next to the pattern blocks a seat reads before it smoke-runs the changed surface. It states
  the accepted shape and the two refusals verbatim: `ready` is an object `{log, port, host, timeout}`
  (`host` defaults to `127.0.0.1`, `timeout` to 30s, give `log`, `port` or both), never a boolean; a call
  that carries a `name` is service mode, where `async` and `timeout` are refused and the wait belongs in
  `ready.timeout`, the process is left running and read at `proc://<name>`; a one-shot command carries no
  `name` and takes the plain `timeout`.
- Front matter: `last_upstream_check` no longer claims the file is identical to the plugin source it was
  vendored from - `vendoring_pattern: modify-the-file` says local edits are the point - so it records the
  historical comparison and this addition, the same way the locally adapted sibling skill records it.

### Testing

- `go test -count=1 ./internal/clientskills/` green: that package is the one check that reads the shipped
  skill tree (`TestShippedSkillLinksResolve`), and the new section adds no local link - it quotes commands
  in a fenced block, which that check's scope excludes on purpose.
- `go test -count=1 ./...` in the client: exit 0, 32 packages `ok`, no `FAIL` (the count read from the
  package lines, not from a filtered stream).
- Both refusal strings are evidence, not paraphrase: `ready: true` was measured at the tool (it answers
  `ready: ready must be an object (was boolean)`), and `Service mode does not accept async or timeout; use
  ready.timeout for readiness.` is the recorded answer of this seat's own four service-mode calls in that
  window. The census paragraph is counted the same way it can be reproduced: every `seats/*/tool-calls.jsonl`
  in the room, rows whose `error`/`rule` carries the refusal text, inside the window - and a boolean `ready`
  appears in none of them, because it is refused before dispatch.
- The accepted shape was smoked, not just written: a call carrying `name` and
  `ready={"log": "listening on", "timeout": 15}` came up `ready pid=...` with no `async`/`timeout`
  argument, and `proc://<name>` read its status and its output back (`ready`, pid, up 1.9s); the same
  path stopped it.

### What this does not do

- It changes no tool. The harness's own description of `bash` is the other half of the gap and is filed
  as feedback `4f60f2cc`; prose in a skill cannot correct the tool's schema text.
- It reaches a seat only at its next reseat: a running seat never re-reads its skills directory, and
  `4genteam seat reseat` (through the runtime's relaunch path) is what replaces it from the store.
