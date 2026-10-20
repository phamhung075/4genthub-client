# `up --respawn` outlives the seat that runs it

A restart is a replacement of the process tree the caller itself lives in. `up --respawn` stopped agent -
the restart authority, and the parent of every seat - and then went on to replace the daemon and start the
room's seats and agent again. Run from a seat, the agent stop is the CALLER's own death: the run died
between the two halves, so the machine was left with a new daemon and nothing else. Measured three times:
2026-10-18 21:38Z, 2026-10-19 05:06Z and 2026-10-20 17:29:38Z (daemon SIGTERM at 17:30:02Z, four seats and
agent left down).

The verb now RE-EXECS ITSELF, in a new session (`setsid`) with both streams on
`<state root>/up-respawn.log`, BEFORE it signals anything. The calling process answers with that log's path
and returns `0`; the restart keeps running after the caller is gone. The child is marked by
`FOURGENTEAM_UP_RESPAWN_DETACHED` rather than by a flag, because it is not an argument a person writes: it
is the assertion that this process is the restart, not the request for one.

The re-exec carries the whole invocation - the rig, `--team`, an explicit `--state-root`, `--allow-peak`,
`--room-only` and `--respawn` - so the child decides from the same arguments the caller made. The state root
is named explicitly because the child is a new session rather than a continuation of the caller's shell.

Everything after the detach is unchanged: agent, then the daemon by pattern, then the same reads, gate,
worktrees and room start. A detach that cannot be started is an `up` refusal that says nothing was stopped;
a daemon that will not be replaced is still a refusal that names it.

## Evidence

`internal/clientlifecycle/respawn_test.go`:

- `TestUpRespawnReExecsItselfBeforeItStopsAnything` - the calling process detaches with the expected argv,
  signals nothing, starts nothing, and prints the detached run's log path.
- `TestTheDetachedRespawnIsItsOwnSession` - the real detach against `sh`: the child's session id differs
  from the caller's, its output lands on the log, and the call returns without waiting for it.
- `TestUpRespawnStopsTheSupervisionThenTheDaemonBeforeTheRoomStarts` and
  `TestUpRespawnRefusesToStartASecondDaemon` now run as the detached child (the marker set), which is where
  the agent and daemon stops live.

Run: `go test ./internal/clientlifecycle/ -count=1` and `go test ./... -count=1`, both green.

The end-to-end shape was exercised against a scratch state root with a stand-in `agenthub-runtime` on
`PATH`: the caller returned in 18 ms with the log path, and the detached run went on through the agent stop
and the daemon step to the worktrees refusal, writing all of it to `up-respawn.log` - the run outlived the
process that asked for it.
