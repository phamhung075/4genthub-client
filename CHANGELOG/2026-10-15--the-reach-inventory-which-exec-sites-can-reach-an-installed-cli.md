## The reach inventory: which exec sites can reach an installed CLI

### What was asked
P1 (MCP `1fc74a14`), the second half of the reviewer's `FINDING-comm-guard-spelling-hole` and the question C5's
acceptance rests on: the guard cannot be said to cover the send path before it is known WHICH reaches in this
client can still exec an INSTALLED CLI. The lead's instrument, run at the merged sha `892f867`:
`grep -rn 'exec\.Command(' --include='*.go' internal/ cmd/ | grep -v '_test.go'` — **26 hits, 25 sites** (the
26th is the prose line in `internal/clientseat/sync.go` quoted below, and it is not a call). `exec.LookPath(` is
counted by the same grep; `installcheckerverb.go` builds its argv from a caller, and the four `os.Executable()`
sites are named here because that is how the SELF class is resolved.

### Answer: no site reaches an installed client
- **Nothing resolves this client's own name.** `grep -rn 'LookPath("4genteam")\|LookPath("rig")' internal/ cmd/`
  (non-test) returns nothing. Measured on this host 2026-10-15: `command -v rig` prints nothing,
  `/home/daihu/.local/bin/rig` does not exist, and `command -v 4genteam` is `/home/daihu/.local/bin/4genteam` — so
  the ONE reach that would resolve an installed client by name is the pane line below, and it is a fallback.
- **`agenthub-runtime` is the only binary identity the client looks up on PATH** (4 sites), each with the same
  per-user fallback `~/.local/bin/agenthub-runtime`.
- The four SELF sites exec the RUNNING binary by absolute path (`os.Executable()`), which is this client's own
  process model — children, not a second installation.

### The inventory (25 sites at `892f867`)
**SELF — this binary, by absolute path, `os.Executable()` (4).** `clientlifecycle/supervisor.go:496`
(`exec.Command(self, args...)`, self at `:485`: a detached supervisor child); `clientlifecycle/lifecycle.go:701`
(`self watch --rig <name>`); `clientheal/command.go:117` (`self up <room>`); `clientseat/sync.go:156`
(`exec.Command(args[0], …)` with `args = SyncRigArgs(self, room, out)` at `:151` — `sync` re-entering its own
`sync rig`).

**RUNTIME — `agenthub-runtime`, PATH first then `~/.local/bin` (4).** `clientruntime/ipc.go:79` (`LookPath`,
fallback `:82-86`) and `:90` (`<bin> --socket <path> daemon`); `clientlifecycle/lifecycle.go:410` (`runtimeBinary()`,
fallback `:413-416`) and `:387` (`<bin> --state-dir … --socket … daemon`).

**TOOL — a system tool or a caller-supplied argv (17).** `clientservices/services.go:264` `LookPath("pgrep")` and
`:268` `pgrep -f <pattern>`; `clientlifecycle/supervisor.go:543`/`:548` (`pgrep -af <SupervisorPattern>`),
`:582`/`:586` (`pkill -f <pattern>`); `clientlifecycle/lifecycle.go:674`/`:675` (`LookPath(opener)`, then
`<opener> <url>`), `:707`/`:712` (`herdr`), `:856`/`:861` (`tail -f <logPath>`);
`clientsync/installcheckerverb.go:57` (`exec.Command(command[0], …)` — the install checker's own `go build`);
`clientwatch/view.go:281` (`stty size`); `clientenv/tmux.go:33` (`argv[0]` from the caller, `4genteam env` → tmux);
`clientteam/publish.go:309` (`git`); `seatlog/seatlog.go:103` (`pgrep -af "claude .*--name <session>"`).

**Name-only surfaces that are NOT exec sites (2), recorded so the class is closed.** `clientlifecycle/rigd.go:154`
and `clientsync/connectorverb.go:95` return the string `"4genteam"` when `os.Hostname()` fails: that is a connector
IDENTITY, no process is started. `clientwatch/watch.go:238` (`selfPath()`) returns `"4genteam"` when
`os.Executable()` fails — and `:246` `paneCommand` builds the one command line this client emits as TEXT
(`clear; exec <path> <verb>`) into a tmux pane, so the pane's own shell resolves that fallback by NAME on PATH.
This is the only such reach, and it fires only when `/proc/self/exe` cannot be read.

**A neighbouring instrument, for completeness:** `exec.CommandContext(` (non-test) is 6 further sites —
`clientsync/installcheckerverb.go:67`, `clientevidence/evidence.go:86` (`git`) and `:100` (caller argv),
`clientbridge/bridge.go:104` and `clientps/ps.go:147` (caller argv), `clientwatch/watch.go:53` (caller name) —
the same three classes, and no new reach.

### Changed
`internal/clientseat/sync.go` — the re-point comment claimed, in the present tense, "`rig` on PATH is the compat
shim to 4genteam". That is measured false on this host (above), so the sentence now states the historical fact and
the dated measurement that the lookup is dead outright: neither the name nor the shim could have answered here.

### Tested
- `go build ./...` and `go test ./internal/clientseat/ -count=1` after the edit: clean. The edit is a comment; no
  behaviour is asserted by it, and the inventory itself is a reading of the tree, not a test.
- The counts are re-runnable as the two greps in "What was asked" (26 hits / 25 sites at `892f867`).
