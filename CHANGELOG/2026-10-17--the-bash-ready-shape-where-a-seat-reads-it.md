# The bash ready shape, where a seat reads it

The `bash` tool's long-lived-service shape was documented nowhere a seat reads, and two refusals follow
from it: `ready` must be an object and never a boolean, and a named service takes no `async`/`timeout`.
In the 24h to 2026-10-16T22:56Z the room spent about 28 calls on them - this seat's own tool-call log
carries 5 of the service-mode refusal in that window, and the rest are the `ready`-boolean refusal on
another seat. Hub task `1b0740aa-3c3f-4a30-9172-7037c5de3468`; the harness half of the gap is the
owner's feedback record `4f60f2cc`.

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
  ready.timeout for readiness.` is the recorded answer of the five service-mode calls in this seat's own
  tool-call log for the same window.
- The accepted shape was smoked, not just written: a call carrying `name` and
  `ready={"log": "listening on", "timeout": 15}` came up `ready pid=...` with no `async`/`timeout`
  argument, and `proc://<name>` read its status and its output back (`ready`, pid, up 1.9s); the same
  path stopped it.

### What this does not do

- It changes no tool. The harness's own description of `bash` is the other half of the gap and is filed
  as feedback `4f60f2cc`; prose in a skill cannot correct the tool's schema text.
- It reaches a seat only at its next reseat: a running seat never re-reads its skills directory, and
  `4genteam seat reseat` (through the runtime's relaunch path) is what replaces it from the store.
