# 4genthub-client

The local client for a 4genthub team: it runs on your machine and works on any project. It pulls and
pins a room's seats from the server, keeps each seat's context small, reports this machine's status
to the server and gives you a terminal view of the whole team.

It does not need the 4genthub server's source code. It needs a server to talk to (`AGENTHUB_URL`,
`AGENTHUB_TOKEN`) and the OpenRig orchestrator that launches the seats.

## Install

```bash
pip install .            # Python >= 3.12; installs the `4genteam` and `agenthub-client` commands
```

## Use

```bash
4genteam up [ROOM]       # daemon, the room's seats, compaction supervisor, status bridge, watch view
4genteam sync rig ROOM   # materialize a room into rig.yaml + one directory per seat
4genteam compact ROOM    # (re)start the compaction supervisor for one room
4genteam watch --rig ROOM
4genteam <tool> --help   # sync, seat, policy, team, bridge, watch, feedback
```

`sync rig` writes everything a new room needs to launch in one step: the provider key linked into the
room directory, an absolute working directory no rebuild deletes, the unattended seat policy and the
per-seat state directories.

## Configuration

| Variable | Meaning |
|---|---|
| `AGENTHUB_URL`, `AGENTHUB_TOKEN` | the server and your key |
| `AGENTHUB_REPO_ROOT` | the project the client works in (default: the checkout containing it) |
| `COMPACT_LIMIT_TOKENS`, `COMPACT_HARD_TOKENS`, `COMPACT_QUIET_SECONDS` | the compaction supervisor's safe point, ceiling and quiet period |

A `.env` in `AGENTHUB_REPO_ROOT` is read; values already in the environment win. Never commit it.

## Test

```bash
PYTHONPATH=src python3 -m pytest tests -q
```

## Design notes

How a supervisor compacts seats, the traps of bootstrapping a new room and the working agreements for
several seats in one repository are written up, generically, in
[multiple-agent-orchestration](https://github.com/phamhung075/multiple-agent-orchestration).
