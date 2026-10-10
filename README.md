# 4genthub-client

Instructions for an AI agent (or a person) setting up the 4genthub client on a machine so that **any
project** on it can use a 4genthub team. This repository is standalone: it needs no other 4genthub
repository. The project you work on is the directory you run
commands from.

Setting a machine up, and then building a room and its seats, is in
[docs/AGENT-SETUP.md](docs/AGENT-SETUP.md). Watching a running room is in
[docs/WATCH.md](docs/WATCH.md), and the same procedure is the skill
[skills/client/watch-team/SKILL.md](skills/client/watch-team/SKILL.md), which any project can copy into
its own skills path.

## What it is

A command-line client, `4genteam`, for one machine. It:

- pulls a room's seats from the 4genthub server and pins them (`sync`),
- keeps each running seat's context small by compacting it for you (`compact`),
- reports this machine's seat status to the server (`bridge`),
- shows the whole team in a terminal grid (`watch`),
- applies a team definition and imports a project's MCP servers and skills (`team`).

The server is hosted. The seats run in OpenRig (the local orchestrator) and are not part of this
repository.

## Prerequisites (check each before you start)

| Need | Check |
|---|---|
| Python 3.12 or newer | `python3 --version` |
| A 4genthub account key | you hold a value for `AGENTHUB_TOKEN` |
| OpenRig daemon on `127.0.0.1:7433` | `rig ps` answers |
| `herdr` and `tmux`, for the watch view | `herdr --version`, `tmux -V` |
| Rust toolchain, only for the force-compaction helper | `cargo --version` |

If a prerequisite is missing, stop and tell the user which one. Do not invent a workaround.

## Install

```bash
git clone git@github.com:phamhung075/4genthub-client.git
cd 4genthub-client
python3 -m venv .venv && . .venv/bin/activate
pip install -e .          # installs `4genteam` and `agenthub-client`
cp .env.sample .env       # then fill in AGENTHUB_TOKEN; never commit .env
4genteam --help
```

`.env` is read from the root of this checkout. A variable already in the environment wins over it.
`logs/` (supervisor and bridge logs) is created in the same place and is git-ignored.

## Connect a project to the 4genthub MCP server

Every project reaches the same hosted MCP endpoint, `https://api.4genthub.com/mcp`, with the same
key. Add this to the **project's** `.mcp.json` (create it if absent):

```json
{
  "mcpServers": {
    "agenthub_http": {
      "type": "http",
      "url": "https://api.4genthub.com/mcp",
      "headers": {
        "Accept": "application/json, text/event-stream",
        "Authorization": "Bearer ${AGENTHUB_TOKEN}"
      }
    }
  }
}
```

`${AGENTHUB_TOKEN}` is expanded from the environment, so export it (or load `.env`) in the shell that
starts the agent. Never write the token into `.mcp.json`. Check the connection by listing the
`agenthub_http` tools in the agent's MCP status view.

## Launch a team for a project

Run from the project's directory. `ROOM` is the room's name on the server.

```bash
export AGENTHUB_URL=https://api.4genthub.com   # or set it in .env
4genteam sync rig ROOM      # writes ~/.openrig/agenthub-seats/ROOM/rig/rig.yaml and the seat files
rig up ~/.openrig/agenthub-seats/ROOM/rig/rig.yaml
4genteam up ROOM            # supervisor, status bridge and watch view for the running room
```

`sync rig` does everything a new room needs in one step: it links the provider key file
`~/.config/deepseek/env` into the room directory when a seat uses a `deepseek/` model (the sync
stops and names the path if that file is missing), sets every seat's working directory to the room
directory, sets the unattended seat policy `builtin:yolo`, and creates each seat's state directory.
A regular `.env` already in the room directory is refused and named, never overwritten.

Seats run unattended with the bypass policy: only launch them in a project whose commit and deny
rules you accept.

## Commands

| Command | Does |
|---|---|
| `4genteam up [ROOM]` | start the daemon's seats, force-compaction helper, status bridge, supervisor and watch view |
| `4genteam compact\|stop\|status\|log [ROOM]` | run, stop, inspect or follow the compaction supervisor |
| `4genteam sync rig ROOM [--out DIR] [--update]` | materialize a room |
| `4genteam sync install-checker --go-dir DIR` | build the seat checker from the server's Go module (`DIR` holds `cmd/seatcheck`) |
| `4genteam seat …`, `4genteam policy …` | seat status and the per-seat policy |
| `4genteam team apply --team DIR` | create the room, seats, links and overlays defined in `DIR/team.json` |
| `4genteam team import-project` | push the current project's `.mcp.json` servers and `.claude/skills` as modules |
| `4genteam team publish-skills --inventory FILE` | push a skill inventory |
| `4genteam bridge run\|once\|install-service` | report this machine's seat status |
| `4genteam watch --rig ROOM` | herdr grid, one pane per seat, plus a lead window |
| `4genteam feedback` | send friction to the server when a runtime has no MCP |

Every command answers `--help`. There is no default team and no default inventory: `team apply`
needs `--team`, `publish-skills` needs `--inventory`.

## Configuration

All variables are optional except the first two. They go in `.env` or the environment.

| Variable | Meaning |
|---|---|
| `AGENTHUB_URL` | the server, for example `https://api.4genthub.com` |
| `AGENTHUB_TOKEN` | your key; the one credential, read by `sync`, `team`, `seat` and `bridge` |
| `OPENRIG_URL`, `OPENRIG_PORT` | the OpenRig daemon (default `http://127.0.0.1:7433`) |
| `COMPACT_LIMIT_TOKENS` | context size above which a seat is compacted at the next quiet moment (150000) |
| `COMPACT_WARN_TOKENS` | urgent size, compacted without waiting for quiet (250000) |
| `COMPACT_HARD_TOKENS` | ceiling the supervisor treats as an emergency (300000) |
| `COMPACT_QUIET_SECONDS` | seconds without output that count as quiet (15) |
| `OPENRIG_SKILLS_ROOT` | the OpenRig checkout `publish-skills` paths are relative to |

## Verify an installation

```bash
4genteam --help                                  # exit 0, lists the commands
PYTHONPATH=src python3 -m pytest tests -q        # the client's own tests; none needs a server or a rig
rig ps --nodes                                   # after `rig up`: every seat running, none parked
4genteam status ROOM                             # the supervisor's process and last log lines
```

A healthy supervisor log shows `WITNESSED (<before> -> <after>)` lines after a compaction. A log that
shows only `supervising` is not broken: no seat has reached the limit yet.

## Failure map

| Message or symptom | Cause | Action |
|---|---|---|
| `AGENTHUB_URL is not set` | no server configured | set it in `.env` |
| `No API key found for deepseek` on a seat | the provider key file is missing | create `~/.config/deepseek/env`, then run `sync rig` again |
| a seat's working directory shows `(deleted)` | the room was built by an older client | run `sync rig`, then relaunch the seat |
| a seat waits at an approval prompt | the room was not synced by this client | run `sync rig` and `rig up` again |
| `… is not a symlink: refusing to replace a file this client did not create` | a real `.env` sits in the room directory | move it aside or point it at the key file |
| `4genteam: error: the following arguments are required: --team` | no default team exists | pass the team directory |
| the supervisor never compacts | the runtime reports no context size | give those seats a short-session rule instead |

## Rules for an agent working in this repository

- Do not print, log or commit a token. `.env` is git-ignored; keep it that way.
- Do not add a fallback to a path outside this repository. The client takes the project from the
  current directory and everything else from `.env` or an argument.
- A change to behavior needs a test in `tests/`, and the suite must stay free of network, rig and
  seat dependencies.
