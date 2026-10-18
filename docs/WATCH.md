# Watching a room

How to see what every seat of a room is doing, live: each tool call and its result, the agent's
own reasoning, what it says back, and the messages it receives. The view is **read-only** — it
reads the seats' session logs and never types into a seat.

It needs `herdr` (the terminal provider the panes live in) and the room's seats running in tmux.

## Open it

```bash
4genteam watch --room <RIG>                        # the seat grid, plus the lead window
4genteam grid  --room <RIG>                        # only the grid
4genteam feed  --room <RIG> [--seat <SEAT> ...]    # one merged stream, for a plain terminal or ssh
```

Always pass `--room`; there is no default room. `4genteam up <RIG>` opens the same wall as part of
the lifecycle, so the view is there whenever the room is brought back up.

Run the verb again for a fresh view. Each one closes the workspaces it owns and rebuilds them, so
a relaunch, a restore or a runtime switch ends with one wall rather than a stack of them. The
layout lives in the client, not in saved terminal state, so it cannot drift.

## What you get

| Workspace | Holds |
|---|---|
| `<RIG> grid` | one pane per live seat, in the owner's layout by default: the lead in one tall pane on the left, the next two seats stacked in the column to its right, every remaining seat side by side along the bottom - no pane left empty. Seats are read in the wall's order (lead, architect, go-dev, fe-dev, reviewer, then any other seat by name). `--cols N` keeps the older equal-columns grid instead. Each pane runs that seat's own feed |
| `<RIG> lead` | the lead's detailed feed on top, with a `lead > input` pane under it: a line typed there is sent to the lead |

`4genteam watch` and `4genteam grid` print `grid: <n> seats in <cols> columns`; the default layout
prints the 2 of its two columns, with the bottom row spanning both.

A pane exists for every seat the room is running (they come from the rig's live tmux sessions).
The feed reads a seat's newest session log — the omp state directory under
`~/.config/4genthub/` first, else the seat's Claude Code project log. A runtime the client has no
log reader for (`codex`, `agy`) keeps its pane and its header and shows no events.

## What a line means

Each grid pane is titled with the seat, its model and a bar of its context use against the compact
limit (`== lead | deepseek-flash ==  ████████░░░░░░░░ 50% 75k/150k to compact`), renamed at most every
5 seconds as the reading moves. The feed itself carries no header line there. A feed in a plain
terminal (no `HERDR_PANE_ID`) prints the same line into the stream instead. The title ends with the
seat's status: `● working` while an event is under 30 s old, then `○ idle` and `○ quiet Nm`.
An event line is prefixed with the time the seat logged it, shown only when the minute changes, and
with the context percentage, shown only when it crosses a 10% step.

In a one-seat pane (a single `--seat`, or `feed` in a plain terminal) the time and the percentage ride on the
rule above the step instead of in a prefix, and step lines start at column 0. A wrapped row stays indented
under its head (width minus the head's length, at least 4), so a long line never returns to the left edge.
The percentage is re-shown at each 5-point step in that mode.

| Mark | Meaning |
|---|---|
| `<glyph> <tool> <intent>` | a tool call, its glyph and colour set by the kind of work: `◉` read (blue), `⌕` search (cyan), `✎` write or edit (yellow), `❯` shell (green), `⌁` web (blue), `⇉` delegate or todo (violet), `?` ask (orange), `→` anything else (white); the dim italic intent is the purpose the seat stated, and the main argument (command, path, prompt) follows on the lines below, with no `key=` |
| `✎ edit <path> +N −M` | an edit shows the change itself as a line diff: removed lines in red behind `-`, added lines in green behind `+`, one line of context, and the counts in the head |
| `⬡ mcp <server_tool>` | a call to an MCP tool, made as a read or write on its `xd://mcp__<server>_<tool>` device; the body is the request |
| `← <tool> text` | the result, dim, headed by the tool that answered and the call's brief and duration. A read shows `· N lines`; a shell result shows its first 3 lines; `--expand` shows every result in full |
| `↺ repeated ×N` | the same event printed N more times in a row, folded into one |
| `──────` | a plain rule in the kind's colour before every step; `┄┄┄┄┄┄` thin for `~ think` and `◂ in`; none before a call that directly follows the reasoning that led to it; red when the step before failed; `▸ say` and tool calls use the full rule |
| `✗ <tool> text` | an error result, red |
| an orange `HOOK <name>` block | a reminder or notice the runtime injected into the seat (the `<system-reminder>` tags are removed; three lines at most) |
| an orange `RULE` block | a rule the runtime injected, by name |
| an orange `EXIT` block | the seat's session ended, with the reason |
| a white-on-red `BLOCKED` block | the seat's own policy refused the call |
| `~ think` | the agent's reasoning before it acts |
| `▸ say` | what the agent writes back |
| `◂ in` | a message the agent received: the user, another seat, or a system notice |
| indented lines | the rest of that call, result or message, as real lines |
| `… +N more lines` | N lines beyond `--lines` were not shown; nothing is dropped silently |

Colours: reads (`read`, `grep`, `find`, `ls`) light blue, writes (`write`, `edit`) yellow, shell
(`bash`, `eval`) green, `mcp__*` calls pink. All are light tones chosen for a dark terminal. The
time on a line is the seat's own record time, so replayed lines keep their real times.

## Options

| Option | Effect |
|---|---|
| `--room <RIG>` | the room to watch; always pass it |
| `--state-root DIR` | the omp state root the session logs are read from: the flag, else `~/.config/4genthub` |
| `--seat SEAT` | which seat: for `feed` and `inputs`, limit the view to this seat, repeatable; for `input`, the seat to send to, required |
| `--cols N` | grid columns (default 2); `grid` and `watch` only |
| `--back N` | events replayed per seat when a pane starts (default 40; 3 for `feed`). A pane holds only what was printed since it started, so raise this to see older work |
| `--width N` | characters kept of each line (default 0: the width of the pane, read with `stty size`; 200 when it cannot be read) |
| `--lines N` | lines shown of each call, result or message (default 60) |
| `--only KINDS` | keep only these kinds, comma separated: `read`, `search`, `write`, `shell`, `web`, `delegate`, `ask`, `mcp`, `other`, `think`, `say`, `in`, `hook`; an unknown kind is a usage error |
| `--errors` | keep only failures, refusals and the end of a session |
| `--changes` | keep only what the seats changed (same as `--only write`) |
| `--expand` | keep every result in full instead of folding it |
| `--detail` | also show the reasoning, what the agent says and what it is told; on for `grid` and `watch`, off for `feed` |

## Type to a seat from the grid

The grid panes are read-only. An input pane is the way to talk to a seat from the grid:

```bash
4genteam inputs open --room <RIG>                # one "<SEAT> > input" pane under every seat pane
4genteam inputs open --room <RIG> --seat <SEAT>  # only that seat gets one
4genteam inputs hide --room <RIG>                # close them again
```

A line typed there is sent to the seat with `4genteam send <session> 'message'`, so it goes through the
same route as any other message. `/hide` or Ctrl-D leaves the loop. Opening it twice adds nothing. The lead
window's input pane exists already.

## Scrolling back

herdr keeps 10 MB of scrollback per pane: the mouse wheel, or copy mode (`prefix+[`, then
`PageUp`/`PageDown`, `q` or `Esc` to leave). Output stays live and follows the bottom. Anything
older than the replay is not in the pane — start the view again with a larger `--back`.

## Verify it, and what to do when it is silent

```bash
herdr workspace list      # look for "<RIG> grid" and "<RIG> lead"
4genteam ps <RIG> --nodes   # one row per seat, with its lifecycle and activity
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
