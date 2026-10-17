# One state root per verb

A verb's state root was resolved more than once inside a single run, and not always the same way: the
`--state-root` flag reached the briefs and the logs while the tmux server the verb asked was always the
one under this machine's DEFAULT root, and some readers asked the default root again behind the verb's
back. So `4genteam continue --state-root X` wrote its log under `X` and read the seats of another tree,
`ps` never took a root at all, and the compaction supervisor's interrupts went to the default root's tmux
server even when it had been started with `--state-root`.

### Fixed

- **One resolution per run.** Every verb that takes a root now resolves it once (`--state-root DIR` or
  `--state-root=DIR`, parsed by the one `clientenv.StateRootFromFlags`, then `clientenv.ResolveStateRoot`)
  and threads the string down: `continue` and `watchdog` (briefs, logs, the watchdog's server-id file, the
  restorer), `ps` (which gained the flag), and `watch`, whose tmux reads were the last ones still asking the
  default root while the verb's own room and seat checks read the flagged one. `up` already resolved once;
  its supervisor, idle guard and seat list now read tmux under that resolved root too.
- **A root is never half-read.** The tmux read under a root has its own forms (`clientenv.TmuxArgvAt`,
  `TmuxCommandAt`), and an EMPTY root is refused with the one state-root refusal rather than falling back to
  this machine's default - a fallback is exactly how one verb came to read a tree it was not told about.
- **An absent tmux server is "no seats", not a failed read** - in all three readers. tmux spells that fact
  two ways, and only one of them was understood: `no server running on ...` was read as no sessions, while
  `error connecting to ... (No such file or directory)` - what every freshly created state root answers -
  failed `ps` (exit 3), `watch feed` and `watch grid` (exit 3). All three now read a failed read with no
  output as no sessions, the rule `continue` and `watchdog` already applied.

### Testing

- `go test ./...` in the client: green. New tests pin the absent-server reading in `ps`
  (`TestListSessionsReadsAnAbsentServerAsNoSessions`) and in `watch`
  (`TestGridReadsAnAbsentServerAsNoSeats`); the existing grid test now pins the tmux root the verb was
  GIVEN (`clientenv.TmuxRoot(root)`) instead of the machine's default.
- Measured in a throwaway home (a temporary directory bound over the account's home, the other candidate
  roots renamed away, no `XDG_RUNTIME_DIR` and no `AGENTHUB_RUNTIME_SOCKET` so the runtime socket is the
  state root's own): `ps` exit 0, `continue` exit 0, `continue ROOM` exit 1 (no room to restore) with its
  log under the root, `up ROOM --team DIR --room-only` starts the daemon and writes the room under the
  root (exit 3 only because the one-seat room's seat had no `cwd`, which the runtime names), `room status`
  and `room stop` exit 0, `watchdog ROOM` exit 0, `watch feed` streams "following 0 seats". Every file
  written in the sandbox home was under `<home>/.config/4genthub/`; the renamed-away roots were never
  re-created. `watch grid` could not be measured there: herdr itself fails without a terminal host, which
  the verb already names as a prerequisite.

### What this does not do

- It does not move any live data. The legacy per-seat trees still on disk (the Python era's
  `state/omp/<rig>-<seat>@<rig>`, `~/.config/4genthub/agenthub-seats`) are left where they are; the move is
  C5's, and no code in this client reads them.
- It does not add a compatibility reader, a symlink or an `AGENTHUB_STATE_ROOT` alias.
- It does not rename `up`'s `--state-dir` to `--state-root`. The spellings differ (up and room use
  `--state-dir`), and renaming the flag is a caller-visible change that belongs to its own row.
