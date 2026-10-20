A probe that could not run is not an absence, and every reader refuses by name

Three client readers collapsed "the probe could not run" into "the probe ran and found nothing", and each
turned a missing tool into a state an operator acted on (board row `6e1171ba`; ruling
`seats/architect/RULING-probe-unavailable-is-not-absence-2026-10-20.md`, §7.12 in
`ai_docs/core-architecture/rigd-boundaries.md`):

- `clientservices.livePIDs` answered `nil` pids when `pgrep` was not on PATH. `Verify` then printed EVERY
  safeguard as `stopped` whatever its heartbeat said, and `respawnDaemon` read the empty table as "the
  daemon is not running" and started a second one.
- `clientlifecycle.stopPattern` answered `false` for "pkill is not here" as well as for "nothing matched",
  so `stopAgent` printed `agent: was not running` and `4genteam stop` exited 0 on a stop it never attempted.
- `clientbridge.PinnedHash` answered `""` for a `pinned.json` it could not read - the value the report sends
  for a seat no client has pinned - so the server stored `no pin` over a pin that exists.

The shape is one: `(value, error)`, where the zero value with a nil error is the absence the probe observed
(pgrep exit 1, pkill exit 1, a missing pin file) and an error is the probe that did not run. `Verify` stops
at the first error and answers no rows, because a partial report reads as complete. Every verb refuses in
words naming the probe:

| Verb | On a probe error |
|---|---|
| `doctor` | exit 3, `no safeguard row was decided: <probe>`, no row line |
| `up` | `clientservices.Probe()` (pgrep + pkill) before the daemon, agent or seat; exit 3, nothing started |
| `up --respawn` | refuses before signalling or starting |
| `stop` | exit 3, `agent was NOT stopped`, never `was not running` |
| `agent` | the pass returns, the refusal is printed once, the daemon exits 3 |
| `heal`, `compact` | exit 3, no findings, no repair |
| `status`, `agent status` | no rows, the probe named on stderr, exit unchanged |
| `bridge once --print` | exit 3, nothing on stdout, the file named |
| a bridge cycle | sends nothing and records the note |

7.2's four states and field set are unchanged, and so is every precedence rule; only the answer for an
unread probe changed.

Proven: `go test ./...` and `go vet ./...` green in `agenthub_client` on 2026-10-20, plus the operator
surface on a `PATH` holding no pgrep/pkill - `doctor` exit 3 with the named line and no row, `up` exit 3 with
nothing started, `stop` exit 3 without `was not running` - and `bridge once --print` against a corrupt
`pinned.json` in the store: exit 3, empty stdout, `... pin could not be read: <path>: ...; the seats report was
NOT sent`.
