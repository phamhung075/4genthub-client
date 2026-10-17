# One log home, under the root the verb resolved

The client kept two kinds of log home. `continue` and `watchdog` wrote under the root their verb resolved,
while everything else - the supervisor's log, agent's log, the bridge's, the message deliverer's, the two
guards', the heartbeat files and `alerts.log` - came from `clientservices.logDir`, which read
`$XDG_STATE_HOME/4genthub/logs` else `<real home>/.local/state/4genthub/logs` and IGNORED the
`--state-root` the verb had just parsed. So one run could report rows from the tree it was pointed at while
the children it started beat into another one, and a machine's logs sat outside its one state home
(`~/.config/4genthub`) that C7 ruled.

### Changed

- **`clientservices.logDir` is a parameterised path, not an environment read.** `LogDir(root)` is
  `<root>/logs`; `HeartbeatDir`, `HeartbeatPath`, `WriteHeartbeat`, `ReadHeartbeat`, `AlertsLogPath`,
  `AppendAlert`, `TailAlerts` and `Verify`'s `Env` all take the root. The `XDG_STATE_HOME` read and the
  second derivation from the real home are deleted, so there is one way to name a log directory and it is
  the caller's resolved root.
- **Every writer and reader threads the root it resolved once.** The supervisor and the two guards keep the
  root on their struct; `agent` gained `--state-root DIR` and hands it to every child it supervises
  (`compact-run`, `watchdog`, `sync messages`, `idle-run`, `budget-run`, `bridge run` all accept it);
  `sync messages` and `bridge run`/`once` gained the flag; `doctor` and `status` gained it (their rows and
  the alert tail are read under it), and `log` gained it for the file it follows. `up` already resolved the
  root (`--state-dir`) and now passes that same string to agent and to its own report wait, `compact`,
  `heal` and `agent status` use their resolved root.

### Not in this change

- `agent.json` lives in the seat store (`clientenv.ResolveSeatStore`), which resolves independently of
  `--state-root`. It is the `seats/` entry of the target-layout audit, not a log; it stays as it is until
  that row is ruled, and a `--state-root`ed report still reads the machine's own `agent.json`.
- No file is moved. The move from the old home is the owner's, below; until it runs, a machine keeps
  reporting whatever the old tree holds at the old paths as absent (the rows read stopped/unknown), which
  is the honest reading of "no beat under the root".

### The owner's move (one command pair, old renamed `.obsolete`)

`<root>/logs` is NOT an empty target: it already holds `rig-continue.log`, `rig-watchdog.log` and
`heartbeat/bridge.json` (the two restore verbs and the bridge already wrote under the root). So the source
is spelled with a trailing `/.` - that copies the CONTENTS into the existing directory, where
`cp -a src dst` would create `<root>/logs/logs/`, and `heartbeat/` then merges file by file:

```
cp -a ~/.local/state/4genthub/logs/. ~/.config/4genthub/logs/ \
  && mv ~/.local/state/4genthub/logs ~/.local/state/4genthub/logs.obsolete
```

The copy ADDS and cleans nothing: it deletes nothing in the target, replaces nothing, and leaves every
file the root already held where it is; the old tree is kept whole under `logs.obsolete`. An
`$XDG_STATE_HOME` that pointed somewhere else is no longer consulted: move that tree by the same pair to
`~/.config/4genthub/logs` if it holds the logs the operator wants to keep.

### Testing

- `go test -count=1 ./...` in the client: green (32 packages). The seam-shaped tests were updated to hand
  the root in rather than fake a directory the production code no longer reads from the environment, and
  `TestEveryLogPathIsUnderTheResolvedRoot` pins that every lifecycle log path is `<root>/logs/<name>`.
  `TestTheChildListIsSevenOne` pins the `--state-root` every supervised child is started with.
- Measured with a throwaway root: two roots each holding a `heartbeat/compact-demo.json` (ages 3600 s and
  120 s) and a distinguishing `alerts.log` line. `doctor demo --state-root A` printed `age 3600s` and A's
  alert line; `--state-root B` printed `age 120s` and B's line; `status` likewise. With `XDG_STATE_HOME`
  pointed at a decoy tree holding a FRESH beat (age 1 s), both verbs still printed A's age: the old
  location is not read. `budget-run --room demo --once --state-root A` wrote its beat to
  `A/logs/heartbeat/budget-demo.json` - the new location, under the resolved root.
