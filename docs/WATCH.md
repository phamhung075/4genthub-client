# Watching a room

How to see what every seat of a room is doing, live: each tool call and its result, the agent's
own reasoning, what it says back, and the messages it receives. The view is **read-only** — it
reads the seats' session logs and never types into a seat.

It needs `herdr` (the terminal provider the panes live in) and the room's seats running in tmux.

## Open it

```bash
4genteam watch --rig <RIG>                        # the seat grid, plus the lead window
4genteam grid  --rig <RIG>                        # only the grid
4genteam feed  --rig <RIG> [--seat <SEAT> ...]    # one merged stream, for a plain terminal or ssh
```

Always pass `--rig`; there is no default room. `4genteam up <RIG>` opens the same wall as part of
the lifecycle, so the view is there whenever the room is brought back up.

Run the verb again for a fresh view. Each one closes the workspaces it owns and rebuilds them, so
a relaunch, a restore or a runtime switch ends with one wall rather than a stack of them. The
layout lives in the client, not in saved terminal state, so it cannot drift.

## What you get

| Workspace | Holds |
|---|---|
| `<RIG> grid` | one pane per live seat, 2 columns by default, seats sorted by name and filled top to bottom then left to right; each pane runs that seat's own feed |
| `<RIG> lead` | the lead's detailed feed on top, with a `lead > input` pane under it: a line typed there is sent to the lead |

`4genteam watch` and `4genteam grid` print `grid: <n> seats in <cols> columns`.

A pane exists for every seat the room is running (they come from the rig's live tmux sessions).
The feed reads a seat's newest session log — the omp state directory under
`~/.openrig/state/omp/` first, else the seat's Claude Code project log. A runtime the client has no
log reader for (`codex`, `agy`) keeps its pane and its header and shows no events.

## What a line means

Each pane starts with a header naming the seat and its model, and a bar of its context use against
the compact limit. Every event line after that is prefixed with the same percentage, so the meter
does not scroll out of view.

| Mark | Meaning |
|---|---|
| `→ <tool> key=value` | a tool call; `(+a, b)` lists arguments that are not shown |
| `← text` | the tool's result, dim |
| `✗ text` | an error result, red |
| a white-on-red `BLOCKED` block | the seat's own policy refused the call |
| `~ think` | the agent's reasoning before it acts |
| `▸ say` | what the agent writes back |
| `◂ in` | a message the agent received: the user, another seat, or a system notice |
| indented lines | the rest of that call, result or message, as real lines |
| `… +N more lines` | N lines beyond `--lines` were not shown; nothing is dropped silently |

Colours: reads (`read`, `grep`, `find`, `ls`) light blue, writes (`write`, `edit`) yellow, shell
(`bash`, `eval`) green, `mcp__*` calls pink. All are light tones chosen for a dark terminal. The
timestamp is when the viewer printed the line, not when the seat ran it, so the lines replayed at
startup share one time.

## Options

| Option | Effect |
|---|---|
| `--rig <RIG>` | the room to watch; always pass it |
| `--cols N` | columns in the grid (default 2) |
| `--back N` | events replayed per seat when a pane starts (default 40; 3 for `feed`). A pane holds only what was printed since it started, so raise this to see older work |
| `--width N` | characters kept of each line (default 200; 170 for `feed`) |
| `--lines N` | lines shown of each call, result or message (default 60) |
| `--detail` | also show the reasoning, what the agent says and what it is told; on for `grid` and `watch`, off for `feed` |

## Type to a seat from the grid

The grid panes are read-only. An input pane is the way to talk to a seat from the grid:

```bash
4genteam inputs open --rig <RIG>                # one "<SEAT> > input" pane under every seat pane
4genteam inputs open --rig <RIG> --seat <SEAT>  # only that seat gets one
4genteam inputs hide --rig <RIG>                # close them again
```

A line typed there is sent to the seat with `rig send`, so it goes through the same route as any
other message. `/hide` or Ctrl-D leaves the loop. Opening it twice adds nothing. The lead window's
input pane exists already.

## Scrolling back

herdr keeps 10 MB of scrollback per pane: the mouse wheel, or copy mode (`prefix+[`, then
`PageUp`/`PageDown`, `q` or `Esc` to leave). Output stays live and follows the bottom. Anything
older than the replay is not in the pane — start the view again with a larger `--back`.

## Verify it, and what to do when it is silent

```bash
herdr workspace list      # look for "<RIG> grid" and "<RIG> lead"
rig ps --nodes --rig <RIG>   # one row per seat, with its lifecycle and activity
```

Run these from the shell that started `herdr`: it resolves its server from the home directory of
the process that calls it, so a process whose home is elsewhere does not find it.

If nothing appears, check those two first: a stopped seat has no pane, and a missing `herdr` or
`tmux` is a prerequisite to fix rather than work around.

## Rules

- **Never press Ctrl-C or type into a raw `tmux attach` pane of a seat.** The seat's runner reads
  standard input: Ctrl-C aborts the work it is doing, and typed text becomes a chat message from
  you to that agent. Use `4genteam inputs` or the lead window's input pane instead.
- `rig terminal open <RIG>` (one raw tile per seat) is the **secondary** view: use it to see a
  seat's own screen, never as the default way to watch a room.
- Every pane runs the client with `exec`, so when that client exits, the pane closes. That is why
  `q`, Ctrl-D or Ctrl-C in a pane ends the pane rather than leaving a dead one; reopen the view
  with the verb.
- A pane does not pick up a change while it runs: it started with the code and settings it read at
  launch. Reopen the view — and restart the compaction supervisor with `4genteam compact <RIG>` —
  after changing the client or a `COMPACT_*` value.
- Keep backticks, apostrophes and `$` out of anything you type into an input pane: your own shell
  expands a backtick substitution before the client ever sees the text.
