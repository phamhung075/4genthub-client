---
name: room-respawn
description: Rebuild the 4genthub client and bring a room back on fresh seat processes in one command, `agenthub_client/scripts/respawn.sh ROOM`. Use when the owner says "rebuild", "reseat", "respawn", "rebuild reseat respawn", "restart the team", after a client change lands, and after a key in ~/.config/4genthub/.env is rotated. Owner shell only, never from a seat.
---

# Respawn a room

One command, from the owner's shell:

```bash
agenthub_client/scripts/respawn.sh 4genthub-min
```

Run it, read its last line, and report it. Do not repeat its steps by hand.

## What it does, in order

1. Refuses inside a seat (`AGENTHUB_SEAT` set): closing the room's sessions would close the caller.
2. Refuses a room with no `<state-root>/rooms/ROOM/spec.json` (exit 2, naming the room). `4genteam stop` is
   MACHINE-WIDE, so a mistyped room would stop supervision for every room.
3. Refuses at DeepSeek PEAK before building (exit 3), unless `--allow-peak` is given. That flag is the
   owner's call, never a default. The answer is `4genteam offpeak`'s exit status: 0 off-peak, 1 at peak.
4. `scripts/build.sh --no-test`: installs `~/.local/bin/4genteam` and `agenthub-runtime` from THIS checkout.
5. `4genteam stop ROOM`: agent and its children stop.
6. Closes every `agenthub--ROOM--*` session on the room's own tmux server (`<state-root>/tmux`).
7. `4genteam up ROOM --respawn`, with every credential-shaped variable unset from the shell, so
   `~/.config/4genthub/.env` is the only source of secrets. The detached `up` of THIS room is waited out.
   `up --respawn` stops every daemon for this state root AND any daemon on the runtime socket (whatever
   its argv), then fails if the daemon it reaches does not run the installed `agenthub-runtime`.
8. `4genteam room reseat ROOM SEAT` only for a seat whose session predates this run; a seat `up` just
   started already has its seat document and is skipped. A failed reseat is counted, not an abort. Then
   `4genteam watch --room ROOM`.
9. Verifies that every seat session was created by this run, that as many came back as were closed, and
   that every runner of the room runs the installed `agenthub-runtime` (`OLD BINARY runner pid N` otherwise).
   The last line is `respawned ROOM: N fresh seat(s) in Ts`. Otherwise it exits 1 and names the stale seat or the log.
   A failed `up` still prints the log and runs this step before the script exits non-zero.

## Flags

| Flag | Effect |
|---|---|
| `--test` | run `go test` in the build (default skips it for speed) |
| `--no-build` | respawn on the binaries already installed |
| `--no-reseat` | skip the reseat step (it only touches seats this run did not start) |
| `--allow-peak` | pass the peak override to `up` and `reseat` |
| `--state-root DIR` | state root (default `~/.config/4genthub`) |

## Why not `build.sh --respawn` or a plain reseat

- `up --respawn` replaces the runtime daemon, but a seat's process survives in its tmux session and `up`
  adopts it. A reseat restarts the conversation inside that same process. Either way, the seat keeps the
  environment it started with, so a rotated key in `.env` never reaches it. Measured 2026-10-21: seats
  ran the old `AGENTHUB_TOKEN` and `DEEPSEEK_API_KEY` after a reseat, until their sessions were closed.
- A variable exported in the calling shell wins over `.env`. The script unsets those names; it never
  reads or prints a value.
- `up --respawn` re-execs itself detached and returns at once, so that a seat calling it survives its own
  restart. The script waits for that detached `up`, not for the agent it starts, which inherits the same
  marker and keeps running.

## Which build it installs

The script builds the checkout it lives in. `agenthub_client/` in the hub repo is the pinned client, so
that is what gets installed. To run an unlanded branch, run the script from that branch's worktree.

## When it fails

- `no seat session of ROOM is running`, or fewer seats came back than were closed: read
  `<state-root>/up-respawn.log`, then `4genteam ps ROOM --nodes`.
- `STALE <seat>`: that seat kept a process from before the run. Close its session on the room's tmux
  server and run the script again with `--no-build`.
- `up ROOM --respawn exited N`: the restart itself failed. The log is printed above; fix the cause and
  rerun. The verify step still ran, so a missing seat is named too.
- Exit 2: a mistyped room (no `rooms/ROOM/spec.json`), two rooms, an unknown flag or a missing value -
  nothing was built or stopped. Check the room name against `4genteam room list`.
- Exit 3: PEAK. Wait for off-peak (`4genteam offpeak`) or ask the owner for `--allow-peak`.
