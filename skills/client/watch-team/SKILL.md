---
name: watch-team
description: Open or reopen the live view of a room — a herdr grid with one pane per seat plus a lead window with an input pane. Use whenever the user asks to "spawn grid", "give watch grid", "show me grid", "open the grid", "reopen grid watch", "watch the team", "show me the agents", "watch session", or after any restore, relaunch, resume, runtime switch or script change. ALWAYS use `4genteam watch --room <RIG>` instead of manual terminal/tmux panes.
---

# Watch a room

The live view of a running room: every seat's tool calls, results, reasoning and incoming
messages, read-only. **NEVER use raw terminal tiles or `tmux attach` when asked for the grid/watch view** — always run `4genteam watch --room <RIG>`. It reads the seats' session logs, displays compaction token gauges (`used/150k to compact`), colors reasoning and tool calls, and provides a proper input pane. It needs `herdr` (the terminal provider the panes live in) and the room's
seats running in tmux.

Full reference: `docs/WATCH.md` in the `4genthub-client` repository.

## Open it

```bash
4genteam watch --room <RIG>
```

`4genteam` is that repository's Go binary (`cmd/4genteam`). Every workspace and pane it opens is
created in the directory you ran it from (`--cwd`), so run it from the project you are working in.
`4genteam up <RIG>` opens the same wall as part of the lifecycle — safeguards,
this view, then the UI.

That opens both workspaces:

- `<RIG> grid`: 2 columns, one pane per live seat, sorted by name and filled top to bottom then left
  to right, each pane running `feed --back 40 --width 0 --lines 60 --detail`. Every pane header
  names the seat and its model and carries a token bar, `used/150k to compact`.
- `<RIG> lead`: the lead's detailed feed on top, a `lead > input` pane below it (ratio 0.8) — a line
  typed there is sent to the lead.

`4genteam grid --room <RIG>` opens the grid alone, and `4genteam feed --room <RIG> [--seat <SEAT> ...]`
gives one merged stream in the terminal you are already in.

Expected output: `grid: <n> seats in <cols> columns`. Only a seat whose runtime the client can read
session logs for shows events; a runtime it has no log reader for keeps its pane and header and
shows nothing.

Always pass `--room`: there is no default room.

The verb is idempotent. It closes the `<RIG> grid` and `<RIG> lead` workspaces it owns and
rebuilds them, so run it again whenever the user asks for the grid — and after every restore,
relaunch or runtime switch, because the layout lives in the client and is not saved in the terminal.

## Verify, do not trust silence

```bash
herdr workspace list          # "<RIG> grid" and "<RIG> lead"
4genteam ps <RIG> --nodes      # one row per seat, with its lifecycle and activity
```

If `watch` prints nothing and no workspace appears, check those two first: a stopped seat has no
pane, and a missing `herdr` or `tmux` is a prerequisite to fix rather than work around. Run them
from the shell that started `herdr` — it resolves its server from the home directory of the process
that calls it.

Panes come from the rig's live tmux sessions. The feed reads a seat's newest session log — the omp
state directory first, else the seat's Claude Code project log — so a `codex` or `agy` seat keeps a
pane and header with no events, and a stopped seat has no pane at all.

## Options

| Option | Effect |
|---|---|
| `--room <RIG>` | the room to watch |
| `--state-root DIR` | the omp state root the session logs are read from: the flag, else `~/.config/4genthub` |
| `--seat SEAT` | which seat: for `feed` and `inputs`, limit the view to this seat, repeatable; for `input`, the seat to send to, required |
| `--cols N` | grid columns (default 2); `grid` and `watch` only |
| `--back N` | events replayed per seat at startup (default 40; 3 for `feed`) |
| `--width N` | characters kept per line (default 200; 170 for `feed`) |
| `--lines N` | lines shown of each call, result or message (default 60) |
| `--detail` | reasoning and messages too; on for `grid` and `watch`, off for `feed` |

## Type to a seat

```bash
4genteam inputs open --room <RIG>                # a "<SEAT> > input" pane under every seat pane
4genteam inputs open --room <RIG> --seat <SEAT>  # only that seat
4genteam inputs hide --room <RIG>
```

A line typed there is sent with `4genteam send`, so it takes the same route as any other message.
`/hide` or Ctrl-D leaves the loop.

## Context limits and the supervisor

These are **settings, not recovery**. The client reads `COMPACT_LIMIT_TOKENS` (default 150_000),
`COMPACT_WARN_TOKENS` (250_000), `COMPACT_HARD_TOKENS` (300_000) and `COMPACT_QUIET_SECONDS` from
the environment, with a variable already in the environment winning over its one env file
(`~/.config/4genthub/.env`); a blank value means the default, and they are changed there, not by
editing code.

**The live ladder:** 150k — the seat is told to finish the job and stop; 250k — it is warned once and
told to write down what it would need after compaction; 300k — the supervisor stops waiting for the
seat to go idle: if it is working it ends the turn for it (`tmux send-keys Escape`, at most one
interrupt per 60s) and sends `/compact` as soon as the log is silent, and a seat already idle is
compacted straight away. An older supervisor waited for idleness and said as much — "a `/compact`
sent to a working seat is read as text" — which is how a busy seat could sit over the limit
indefinitely. Text that still quotes a 200k ceiling or a 400k hard limit is stale.

Restart the supervisor with `4genteam compact <RIG>` (it stops the running one first), read it back
with `4genteam status <RIG>`, and its log is `logs/compact-supervisor-<RIG>.log`.

## Restoring: the zero-size-file trap

**Before restoring a zero-size file, ask which kind of file it is: does its value live in HEAD, or in
the working tree?** `git restore --source=HEAD --worktree -- <file>` returns a file whose value is
committed to exactly what you want, and **silently reverts** one whose value is uncommitted. On
2026-10-09 exactly that operation dropped the uncommitted layers of a `CHANGELOG.md` and an
operations guide — both then read as "restored" while holding the wrong revision (a passing size and
a passing presence check, a failing identity). Decide with two reads: `git diff --stat -- <file>`
(is there an uncommitted layer?) and `git log --oneline -1 -- <file>`; if there is, **copy the file
out first** — a `/tmp` copy with its `(mtime, ctime)` pair, or a stash patch. **If the value is
uncommitted, the recovery source is the staged or dangling blob, never HEAD**, and a candidate must
be confirmed by blob id before it is trusted.

`4genteam continue <RIG>` already runs `watch` at the end of every restore, so a restored room comes
back watched; skip that with `RIG_CONTINUE_WATCH=0`.

## Rules

- **Never press Ctrl-C or type into a raw `tmux attach` pane of a seat.** The runner reads standard
  input: Ctrl-C aborts that seat's work, and typed text becomes a chat message to the agent.
- The secondary view is a seat's own screen, and the client has no verb that opens one raw
  `tmux attach` tile per seat: read that pane with `4genteam capture <session>`, or attach by
  hand. It is not the way to watch a room.
- Each pane runs the client with `exec`, so the pane closes when that client exits. Reopen the view
  with the verb instead of expecting a dead pane to come back.
- A running pane never picks up a code change: each pane runs the binary it started with. After
  editing the client — or changing a `COMPACT_*` value in its env file — run `4genteam watch --room
  <RIG>` again: it closes and rebuilds both workspaces, which is the only way the panes pick up the
  new value. The supervisor has the same property, so restart it as above.
- Keep backticks, apostrophes and `$` out of any message sent with `4genteam send`: **your own shell
  executes a backtick substitution before `4genteam` sees the text**, and the seat policy does not catch
  it (a refusal matches the command prefix, not a substitution inside an argument). Avoid them, or
  wrap the whole message in SINGLE quotes.
