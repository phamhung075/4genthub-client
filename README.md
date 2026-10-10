# 4genthub-client

Instructions for an AI agent (or a person) installing the 4genthub client on a machine so that **any
project** on it can use a 4genthub team. One binary, `4genteam`; no Python, no pip, no virtualenv.
This repository is standalone: it needs no other 4genthub repository. The project you work on is the
directory you run commands from.

Setting a machine up, and then building a room and its seats, is in
[docs/AGENT-SETUP.md](docs/AGENT-SETUP.md). Watching a running room is in
[docs/WATCH.md](docs/WATCH.md), and the same procedure is the skill
[skills/client/watch-team/SKILL.md](skills/client/watch-team/SKILL.md), which any project can copy into
its own skills path.

## What it is

`4genteam` is a command-line client for one machine. It:

- pulls a room's seats from the 4genthub server and pins them (`sync`, `seat`),
- starts a room's local stack — daemon, seats, status bridge, compaction supervisor, watch view (`up`),
- keeps each running seat's context small by compacting it (`compact`, `compact-run`),
- reports this machine's rigs and seat status to the server (`bridge`),
- shows the whole room live in a terminal grid (`watch`, `grid`, `feed`, `inputs`, `input`),
- creates the cloud room from a definition and pushes a project's MCP servers and skills as modules
  (`team`), shows and applies the per-seat policy (`policy`), and sends friction reports (`feedback`).

The server is hosted. The seats run in OpenRig (the local orchestrator) and are not part of this
repository.

## Requirements (check each before you start)

| Need | Check |
|---|---|
| A 4genthub account key | you hold a value for `AGENTHUB_TOKEN` |
| OpenRig daemon on `127.0.0.1:7433` | `rig ps` answers |
| `herdr` and `tmux`, for the watch view | `herdr --version`, `tmux -V` |
| Go 1.26 or newer, only to build from source | `go version` |
| Rust toolchain, only for the force-compaction helper | `cargo --version` |

If a prerequisite is missing, stop and tell the user which one. Do not invent a workaround.

## Install

**A machine that has only the binary.** Put it on `PATH` and check it answers:

```bash
install -m 0755 4genteam ~/.local/bin/     # or any directory that is already on PATH
4genteam --help                            # exit 0, lists every command
4genteam version                           # the binary's own version
```

A binary installed on its own has no `skills/` tree beside it, and `sync rig` refuses until it finds
the tree it delivers to the seats: set `OPENRIG_CLIENT_SKILLS=/path/to/4genthub-client/skills`, or
pass `--skills-root DIR` to that verb. A checkout has it at `skills/`.

**From source, with Go 1.26 or newer.** One module, one dependency (`gopkg.in/yaml.v3`):

```bash
go install github.com/phamhung075/4genthub-client/cmd/4genteam@latest   # into $(go env GOPATH)/bin
```

or, from a checkout, which is also what you do to verify a change:

```bash
go build -o 4genteam ./cmd/4genteam        # builds the one binary
go test ./...                              # the client's own tests; no server, rig or network
install -m 0755 4genteam ~/.local/bin/
```

The seat-side checker is a **second** binary, built from this repository's `cmd/seatcheck` and put
where the seats find it:

```bash
4genteam sync install-checker --go-dir .
```

The force-compaction helper for `omp` seats is the Rust program in `rust/forcecompact`; build it with
`cargo build --release` and point the client at it with `4GENTEAM_FORCECOMPACT`, or leave that unset
and let the client find `forcecompact` on `PATH`.

## The one credential and the one env file

There is exactly one credential, `AGENTHUB_TOKEN`, paired with `AGENTHUB_URL`, and one file for it:

```bash
mkdir -p ~/.config/4genthub
cp .env.sample ~/.config/4genthub/.env      # then fill in AGENTHUB_TOKEN
```

- The client reads `~/.config/4genthub/.env` at startup. Resolved from the real home, it is the same
  file inside a seat and out.
- **A variable already in the environment wins over the file**, so the file is a default and never
  overrides a shell that already has the value. A missing file is not an error: the client works from
  the environment alone.
- The file takes `KEY=VALUE`, an optional `export ` prefix, blank lines, `#` comments and quoted
  values.
- A blank `COMPACT_*` value means the documented default; a value that is present and not a whole
  number is refused by name rather than silently ignored.
- Never commit the file, and never copy the token anywhere else: not into `.mcp.json`, not into a
  command line, not into a message. `.env.sample` carries no token value.

## Commands

Every command answers `--help`. This is the list the binary itself prints (`4genteam --help`):

| Command | Does |
|---|---|
| `sync` | pull and inspect resolved seats: `status`, `pull`, `messages`, `connector`, `rig`, `bundle`, `switch`, `install-checker`, `offline-install`, `respawn` |
| `bridge` | report this machine and its rigs to the cloud |
| `up` | start the local stack for a rig: daemon, seats, bridge, supervisor, UI |
| `compact` | (re)start the compaction supervisor for a rig, detached |
| `compact-run` | the compaction supervisor loop, in the foreground (what `compact` starts) |
| `stop` | stop the compaction supervisor and the status bridge |
| `status` | the supervisor's process and the last log lines |
| `log` | follow the supervisor's log |
| `ui` | open the OpenRig web UI |
| `offpeak` | is DeepSeek billing at off-peak right now: exit 0 off-peak, 1 peak, so it works as a gate; `status` prints the setting, `set on\|off` writes it |
| `seat` | status, sync and watch of a room's seats |
| `policy` | per-seat policy: `show`, `apply` |
| `team` | create the cloud room: `apply`, `import-project`, `publish-skills`, `drift-check` |
| `watch` | the live view of a room: the seat grid and the lead window |
| `grid` | open the seat grid alone |
| `feed` | one merged stream of every seat, for a plain terminal or ssh |
| `inputs` | open or hide an input pane under each seat pane |
| `input` | read lines and send each to one seat |
| `feedback` | report friction to the owner |
| `seatcheck` | the seat-side checker (also reachable as `seatcheck` via argv[0]) |
| `version` | the client's own version |

There is no default team and no default inventory: `team apply` needs `--team`, `publish-skills`
needs `--inventory`. A command that needs the rig and cannot reach it exits non-zero with the reason;
nothing downgrades silently.

## Configuration

Every key lives in `~/.config/4genthub/.env` or the environment; only the first two are required.
`.env.sample` is the same list with the same comments.

| Variable | Meaning |
|---|---|
| `AGENTHUB_URL` | the server, for example `https://api.4genthub.com` |
| `AGENTHUB_TOKEN` | your key; the one credential |
| `OPENRIG_URL`, `OPENRIG_PORT` | the OpenRig daemon (default `http://127.0.0.1:7433`); the port variable wins if both are set |
| `OPENRIG_STATE_ROOT` | the state root every verb that needs one resolves: `--state-root` where the verb takes it, else this, else `~/.openrig/state/omp`; a verb that can resolve none of the three refuses by name instead of guessing |
| `OPENRIG_SKILLS_ROOT` | the skill-library checkout `team publish-skills` and `team drift-check` take their recorded paths from |
| `OPENRIG_CLIENT_SKILLS` | this client's own skills tree, when no `--skills-root` flag is given; the default is `skills/` beside the binary, and `sync rig` refuses when there is no tree there |
| `4GENTEAM_FORCECOMPACT` | the force-compaction helper's path; blank means: find `forcecompact` on `PATH` |
| `4GENTEAM_CLAUDE_PROJECTS` | a Claude Code projects directory to read session logs from, as the fallback for a `claude-code` seat |
| `COMPACT_LIMIT_TOKENS` | context size at which a seat is told to find a safe point (150000) |
| `COMPACT_WARN_TOKENS` | urgent size, compacted without waiting for quiet (250000) |
| `COMPACT_HARD_TOKENS` | ceiling the supervisor treats as an emergency (300000) |
| `COMPACT_QUIET_SECONDS` | seconds without output that count as quiet (15) |
| `OFFPEAK_ONLY` | the DeepSeek peak gate: `1`/`on` (the default) refuses to start or resume a seat whose model is `deepseek/` at peak, `0`/`off` allows it; a value that is neither is refused by name, never read as the default |
| `OFFPEAK_UTC` | pins the instant the schedule is read at (`YYYY-MM-DD HH:MM`, UTC) — for a test, or a machine whose clock you do not trust |
| `DEEPSEEK_HOLIDAYS` | a file of `YYYY-MM-DD` dates, one per line, that bill at the off-peak rate all day |

The client also respects three variables it does not own: `XDG_STATE_HOME`, which is the base of the
supervisor's log directory (`$XDG_STATE_HOME/4genthub/logs`, else `~/.local/state/4genthub/logs`),
`PATH`, which is where it looks for `herdr`, `tmux` and `forcecompact`, and `DEEPSEEK_HOLIDAYS` and
`OFFPEAK_UTC`, which it reads with the same names the shell gate used so a machine switching to this
client keeps the same behavior. Only `OFFPEAK_ONLY` is a key this client both reads and writes.

## DeepSeek peak billing

DeepSeek bills at half rate outside its peak windows: 01:00-04:00 and 06:00-10:00 UTC, Monday to
Friday. Every other hour, all of Saturday and Sunday, and any date listed in the `DEEPSEEK_HOLIDAYS`
file are off-peak.

`4genteam offpeak` answers the one question an operator has — is it off-peak right now — and its exit
code is the answer, so it doubles as a gate:

```bash
4genteam offpeak            # PEAK 02:00 UTC (Fri) PEAK: 01:00-04:00 UTC (Mon-Fri)
                            #  next: 04:00 UTC (off-peak resumes)
4genteam offpeak --quiet    # prints nothing; the exit code is the answer (0 off-peak, 1 peak)
4genteam offpeak --json     # state, utc, weekday, holiday, reason, next
4genteam offpeak status     # the OFFPEAK_ONLY setting, where it came from, and the state now
4genteam offpeak set off    # writes OFFPEAK_ONLY=0 to the env file, and prints nothing else from it

4genteam offpeak --quiet && start-the-expensive-work
```

The setting gates the three verbs that **start or resume** DeepSeek seats — `up`, `compact` and
`sync respawn` — judged on the model each seat's own record carries. With `OFFPEAK_ONLY` on (the
default) they refuse at peak with exit 3, naming the seat, its model and when off-peak resumes, and
nothing is resumed or launched:

```
4genteam up: 1 seat(s) run a DeepSeek model (room-alpha.dev: deepseek/deepseek-flash) and DeepSeek
bills at PEAK right now - PEAK: 01:00-04:00 UTC (Mon-Fri), next: 04:00 UTC (off-peak resumes).
Refusing to start or resume them at the peak rate: pass --any-time (or set OFFPEAK_ONLY=0) to run
now, or wait and the gate opens by itself
```

`--any-time` skips the check for one run and wins over the setting in the environment or the env
file; `--offpeak-only` forces it back on. Passing both is a usage error. A room with no `deepseek/`
seat is never refused, and neither is one whose seats cannot be read — a reading that fails is not a
refusal, and the verb's own error says what is wrong.

The same procedure packaged for an agent that runs the client itself is
[skills/client/offpeak-gate/SKILL.md](skills/client/offpeak-gate/SKILL.md).

## Verify an installation

```bash
4genteam --help                 # exit 0, lists the commands
4genteam version                # the binary's own version
go test ./...                   # from a checkout: the client's tests, no server, rig or network
rig ps --nodes                  # after `rig up`: every seat running, none parked
4genteam status ROOM            # the supervisor's process and last log lines
```

A healthy supervisor log shows `WITNESSED (<before> -> <after>)` lines after a compaction. A log that
shows only `supervising` is not broken: no seat has reached the limit yet.

## Launch a room for a project

Run from the project's directory. `ROOM` is the room's name on the server.

```bash
4genteam sync rig ROOM      # writes ~/.openrig/agenthub-seats/ROOM/rig/rig.yaml and the seat files
rig up ~/.openrig/agenthub-seats/ROOM/rig/rig.yaml
4genteam up ROOM            # supervisor, status bridge and watch view for the running room
```

`sync rig` does everything a new room needs in one step: when a member's model names a provider
(`provider/model`), it links this client's own env file, `~/.config/4genthub/.env`, to the room's
`.env`, and refuses naming that path when the file does not exist (omp reads the key from the launch
directory's `.env`); it sets every seat's working directory to the room directory, sets the
unattended seat policy `builtin:yolo`, and creates each seat's state directory. A regular `.env`
already in the room directory is refused and named, never overwritten.

Seats run unattended with the bypass policy: only launch them in a project whose commit and deny rules
you accept.

## Connect a project to the 4genthub MCP server

Every project reaches the same hosted MCP endpoint, `https://api.4genthub.com/mcp`, with the same key.
Add this to the **project's** `.mcp.json` (create it if absent):

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

`${AGENTHUB_TOKEN}` is expanded by the MCP client from **its own** environment, so export it in the
shell that starts that agent — the client's env file is read by `4genteam`, not by the agent:

```bash
set -a; . ~/.config/4genthub/.env; set +a     # or export AGENTHUB_TOKEN for this shell only
```

Never write the token into `.mcp.json`. Check the connection by listing the `agenthub_http` tools in
the agent's MCP status view.

## Watch a running room

`4genteam watch --rig ROOM` opens a herdr grid with one pane per live seat plus a lead window with an
input pane; `4genteam grid`, `feed`, `inputs open` and `input` are the pieces of it. The procedure,
the options and the rules are in [docs/WATCH.md](docs/WATCH.md), which is also the skill
[skills/client/watch-team/SKILL.md](skills/client/watch-team/SKILL.md).

## The skills tree

`skills/` holds this repository's skills in four kinds, and the path says which is which:

| Path | Kind | Who gets it |
|---|---|---|
| `skills/share/<skill>/SKILL.md` | shared | every seat of every room |
| `skills/rooms/<room>/<skill>/SKILL.md` | room | every seat of that one room |
| `skills/rooms/<room>/<seat>/<skill>/SKILL.md` | seat | that one seat |
| `skills/client/<skill>/SKILL.md` | client | an LLM that runs `4genteam` itself, not a seat |

The loader reads the tree by one rule: **a directory holding `SKILL.md` is a skill, and a directory
without one is a seat folder.** So `skills/rooms/<room>/<name>` is a room-level skill when it holds
`SKILL.md`, and the seat folder `<name>` when it does not. A shared or client skill reads generic — no
rig, seat, host or personal path; a room or seat skill may name its own room or seat, because that is
what it is scoped to.

`docs/AGENT-SETUP.md` describes how a skill gets to a seat.

## Failure map

| Message or symptom | Cause | Action |
|---|---|---|
| `AGENTHUB_URL and AGENTHUB_TOKEN must be set (in the environment or the client's env file)` | no credential configured | set both in `~/.config/4genthub/.env` |
| `a ROOM is required` | the verb needs a room and none was given | pass the room: `4genteam seat status ROOM` |
| `error: team apply: --team DIR is required (the directory that holds team.json)` | no default team exists | pass the team directory |
| `error: team publish-skills: --inventory FILE is required (the skill library inventory JSON)` | no default inventory exists | pass the inventory file |
| `the room has a provider/model seat and … does not exist: omp reads the key from the launch directory's .env` | this client's env file is missing, so the room's `.env` has nothing to link to | create `~/.config/4genthub/.env`, then run `sync rig` again |
| `the client's skill tree is not at … (no share/ directory)` | `sync rig` writes the skills this client owns into each seat, and could not find that tree | pass `--skills-root DIR`, or set `OPENRIG_CLIENT_SKILLS` to a tree that holds `share/` |
| `seatcheck does not resolve to … on PATH` | the seat checker is not where the seats will look for it | run `4genteam sync install-checker --go-dir .` and put that directory on `PATH` |
| a seat's working directory shows `(deleted)` | the room was built by an older client | run `sync rig`, then relaunch the seat |
| a seat waits at an approval prompt | the room was not synced by this client | run `sync rig` and `rig up` again |
| `… is not a symlink: refusing to replace a file this client did not create` | a real `.env` sits in the room directory | move it aside or point it at the key file |
| a supervisor is still running after `4genteam stop ROOM` | `stop` and `status` find one by the argv fragment `compact-run --rig ROOM`, and this one was started by an older client whose argv does not carry it | stop it where it was started, and do not run `4genteam compact ROOM` beside it, which would start a second supervisor for that room |
| the supervisor never compacts | the runtime reports no context size | give those seats a short-session rule instead |
| `… run a DeepSeek model (…) and DeepSeek bills at PEAK right now` from `up`, `compact` or `sync respawn` | the room holds a `deepseek/` seat and `OFFPEAK_ONLY` is on (the default), so the client refuses to start or resume it inside a peak window | nothing is broken: pass `--any-time` to run it now at the peak rate, set `OFFPEAK_ONLY=0` in the env file to allow it from now on, or wait for the next off-peak window the message names |
| `4genteam: unknown command <name>` | the verb does not exist | run `4genteam --help` for the list |

## Rules for an agent working in this repository

- Do not print, log or commit a token. `~/.config/4genthub/.env` is outside the repository; keep it
  that way, and never copy the token into a tracked file.
- Do not add a fallback to a path outside this repository. The client takes the project from the
  current directory and everything else from its env file or an argument.
- A change to behavior needs a Go test beside the package it changes, and the suite must stay free of
  network, rig and seat dependencies: `go test ./...`.
- The client names no private path, host, person or rig: a default that points at one team belongs in
  an argument or the env file, not in the code.
