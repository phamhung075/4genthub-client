---
name: watch-team
description: Open or reopen the live view of an OpenRig room — a herdr grid with one pane per seat plus a lead window with an input pane. Use when the user says "open the grid", "watch the team", "show me the agents", or after any restore, relaunch or runtime switch.
---

# Watch a room

The live view of a running room: every seat's tool calls, results, reasoning and incoming
messages, read-only. Use it instead of raw terminal tiles. It reads the seats' session logs and
never types into a seat.

Full reference: `docs/WATCH.md` in the `4genthub-client` repository.

## Open it

```bash
4genteam watch --rig <RIG>
```

That opens both workspaces:

- `<RIG> grid`: one pane per live seat, 2 columns, seats sorted by name, each pane running that
  seat's feed.
- `<RIG> lead`: the lead's detailed feed on top, a `lead > input` pane below it — a line typed
  there is sent to the lead.

`4genteam grid --rig <RIG>` opens the grid alone, and `4genteam feed --rig <RIG> [--seat <SEAT> ...]`
gives one merged stream in the terminal you are already in. `4genteam up <RIG>` opens the wall as
part of the lifecycle.

Always pass `--rig`: there is no default room.

The verb is idempotent. It closes the `<RIG> grid` and `<RIG> lead` workspaces it owns and
rebuilds them, so run it again whenever the user asks for the grid — and after every restore,
relaunch or runtime switch, because the layout lives in the client and is not saved in the terminal.

## Verify, do not trust silence

```bash
herdr workspace list          # "<RIG> grid" and "<RIG> lead"
rig ps --nodes --rig <RIG>    # one row per seat, with its lifecycle and activity
```

Panes come from the rig's live tmux sessions. The feed reads a seat's newest session log — the omp
state directory first, else the seat's Claude Code project log — so a `codex` or `agy` seat keeps a
pane and header with no events, and a stopped seat has no pane at all.

## Options

| Option | Effect |
|---|---|
| `--rig <RIG>` | the room to watch |
| `--cols N` | grid columns (default 2) |
| `--back N` | events replayed per seat at startup (default 40; 3 for `feed`) |
| `--width N` | characters kept per line (default 200; 170 for `feed`) |
| `--lines N` | lines shown of each call, result or message (default 60) |
| `--detail` | reasoning and messages too; on for `grid` and `watch`, off for `feed` |

## Type to a seat

```bash
4genteam inputs open --rig <RIG>                # a "<SEAT> > input" pane under every seat pane
4genteam inputs open --rig <RIG> --seat <SEAT>  # only that seat
4genteam inputs hide --rig <RIG>
```

A line typed there is sent with `rig send`, so it takes the same route as any other message.
`/hide` or Ctrl-D leaves the loop.

## Rules

- **Never press Ctrl-C or type into a raw `tmux attach` pane of a seat.** The runner reads standard
  input: Ctrl-C aborts that seat's work, and typed text becomes a chat message to the agent.
- `rig terminal open <RIG>` is the secondary view — a seat's own screen, not the way to watch a
  room.
- Each pane runs the client with `exec`, so the pane closes when that client exits. Reopen the view
  with the verb instead of expecting a dead pane to come back.
- A running pane keeps the code and settings it started with. Reopen the view after a change to the
  client or to a `COMPACT_*` value; restart the supervisor with `4genteam compact <RIG>`.
- Keep backticks, apostrophes and `$` out of a typed line: your own shell expands them first.
