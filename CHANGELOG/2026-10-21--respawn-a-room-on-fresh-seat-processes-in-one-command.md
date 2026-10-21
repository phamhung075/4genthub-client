# Respawn a room on fresh seat processes in one command

### Added

- `scripts/respawn.sh ROOM [--test] [--no-build] [--no-reseat] [--allow-peak] [--state-root DIR]`: builds
  (`build.sh --no-test`), stops agent, closes the room's `agenthub--ROOM--*` sessions on `<state-root>/tmux`,
  runs `up ROOM --respawn` with every credential-shaped variable unset from the calling shell, waits for the
  detached `up`, reseats every seat, reopens the watch view, and verifies that every seat session is newer
  than the run and that as many came back as were closed. It refuses inside a seat (`AGENTHUB_SEAT`) and
  refuses at PEAK before building (exit 3).
- `skills/client/room-respawn/SKILL.md`, and its row in the README's client skill table.

### Why

`up --respawn` replaces the daemon, but `up` adopts a seat process that survives in its tmux session, and a
reseat restarts the conversation inside that same process. Measured 2026-10-21: after a key rotation and a
reseat, all five seats still ran the old `AGENTHUB_TOKEN` and `DEEPSEEK_API_KEY` until their sessions were
closed by hand. A variable exported in the calling shell also wins over `.env`.

The detached `up` is identified by `argv[1] == up` plus `FOURGENTEAM_UP_RESPAWN_DETACHED=1`. The marker
alone is wrong because the agent that `up` starts inherits it and never exits. The detached run truncates
`up-respawn.log`, so the script prints the whole file after the wait.

### Verification

On room 4genthub-min, from the owner shell: the full run (build included) and a `--no-build` run both ended
`respawned 4genthub-min: 5 fresh seat(s)`; the `--no-build` run took 29s. Every seat process carries the
`.env` token hash. The strip loop, run alone with fake exported `*_TOKEN`/`*_SECRET_KEY` names, unsets them
and keeps other variables and `PATH`. Argument guards: no room, two rooms, an unknown flag and
`AGENTHUB_SEAT=lead` each exit 2.
