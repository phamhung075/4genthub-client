# Respawn a room on fresh seat processes in one command

### Added

- `scripts/respawn.sh ROOM [--test] [--no-build] [--no-reseat] [--allow-peak] [--state-root DIR]`: builds
  (`build.sh --no-test`), stops agent, closes the room's `agenthub--ROOM--*` sessions on `<state-root>/tmux`,
  runs `up ROOM --respawn` with every credential-shaped variable unset from the calling shell, waits for the
  detached `up`, reseats every seat, reopens the watch view, and verifies that every seat session is newer
  than the run and that as many came back as were closed. It refuses inside a seat (`AGENTHUB_SEAT`), refuses
  a room with no `rooms/ROOM/spec.json` record (exit 2, before the machine-wide `stop`), and refuses at PEAK
  before building (exit 3). The PEAK check reads `4genteam offpeak`'s exit status - 1 is the peak ANSWER, not
  a failure - so no pipeline can hide it.
- The credential strip keeps `COMPACT_HARD_TOKENS`, `COMPACT_LIMIT_TOKENS` and `COMPACT_WARN_TOKENS`: they
  match `*TOKEN*` but are compaction thresholds, so unsetting a shell-exported value would drop it silently.
- A failed `up` does not end the run at that line: the log is printed, the verify step runs, and the script
  then exits non-zero, naming `up`'s status.
- `skills/client/room-respawn/SKILL.md`, and its row in the README's client skill table.

### Why

`up --respawn` replaces the daemon, but `up` adopts a seat process that survives in its tmux session, and a
reseat restarts the conversation inside that same process. Measured 2026-10-21: after a key rotation and a
reseat, all five seats still ran the old `AGENTHUB_TOKEN` and `DEEPSEEK_API_KEY` until their sessions were
closed by hand. A variable exported in the calling shell also wins over `.env`.

The detached `up` is identified by `argv[1] == up`, `argv[2] == ROOM` and `FOURGENTEAM_UP_RESPAWN_DETACHED=1`.
The marker alone is wrong because the agent that `up` starts inherits it and never exits, and the room is
what tells this run's restart from another room's lingering one, which writes the same per-state-root log.
The detached run truncates `up-respawn.log`, so the script prints the whole file after the wait.

### Verification

On room 4genthub-min, from the owner shell: the full run (build included) and a `--no-build` run both ended
`respawned 4genthub-min: 5 fresh seat(s)`; the `--no-build` run took 29s. Every seat process carries the
`.env` token hash. The strip loop, run alone with fake exported `*_TOKEN`/`*_SECRET_KEY` names, unsets them
and keeps other variables and `PATH`. Argument guards: no room, two rooms, an unknown flag and
`AGENTHUB_SEAT=lead` each exit 2.

After the gate's changes, in a temp state root with a fake `4genteam` recording every call: a PEAK answer
(prints `PEAK`, exits 1) exits 3 with only `offpeak` in the call log - no build, no stop, no session close;
a missing `rooms/demo/spec.json` exits 2 before any call at all; an `up` that exits 5 leaves the log printed
and the verify step run (`ps --nodes` in the log) before exit 1; a detached `up otherroom --respawn` carrying
the marker matches the old predicate but not `argv[2] == demo`, and a run for `demo` beside it finished in
0s instead of waiting 900s; `AGENTHUB_TOKEN` and `DEEPSEEK_API_KEY` are unset for `up` while the three
`COMPACT_*_TOKENS` and an ordinary variable survive. `bash -n` is clean.
