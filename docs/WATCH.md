# Watching a room

How to see what every seat of a room is doing, live: each tool call and its result, the agent's
own reasoning, what it says back, and the messages it receives. The view is **read-only** — it
reads the seats' session logs and never types into a seat.

It needs `herdr` (the terminal provider the panes live in) and the room's seats running in tmux.

## Open it

```bash
4genteam watch --room <ROOM>                        # the seat grid, plus the lead window
4genteam grid  --room <ROOM>                        # only the grid
4genteam feed  --room <ROOM> [--seat <SEAT> ...]    # one merged stream, for a plain terminal or ssh
```

Always pass `--room`; there is no default room. `4genteam up <ROOM>` opens the same wall as part of
the lifecycle, so the view is there whenever the room is brought back up.

Run the verb again for a fresh view. Each one closes the workspaces it owns and rebuilds them, so
a relaunch, a restore or a runtime switch ends with one wall rather than a stack of them. The
layout lives in the client, not in saved terminal state, so it cannot drift.

## What you get

| Workspace | Holds |
|---|---|
| `<ROOM> grid` | one pane per live seat, in the owner's layout by default: the lead in one tall pane on the left, the next two seats stacked in the column to its right, every remaining seat side by side along the bottom - no pane left empty. Seats are read in the wall's order (lead, architect, go-dev, fe-dev, reviewer, then any other seat by name). `--cols N` keeps the older equal-columns grid instead. Each pane runs that seat's own feed |
| `<ROOM> lead` | the lead's detailed feed on top, with a `lead > input` pane under it: a line typed there is sent to the lead |

`4genteam watch` and `4genteam grid` print `grid: <n> seats in <cols> columns`; the default layout
prints the 2 of its two columns, with the bottom row spanning both.

A pane exists for every seat the room is running (they come from the room's live tmux sessions).
The feed reads a seat's newest session log — the omp state directory under
`~/.config/4genthub/` first, else the seat's Claude Code project log; an agy seat's transcript is
read from agy's own home. A runtime the client has no log reader for (`codex`) keeps its pane and its
header and shows no events.

An agy seat's pane reads like an omp seat's: `internal/clientwatch/agy.go` maps agy's tools onto omp's
(`run_command` is `bash`, `read_file` and `view_file` are `read`, `write_file` is `write`, `edit_file` is
`edit`, `call_mcp_tool` is the MCP tool it calls), decodes the arguments and strips the envelope off a
result, and the one renderer does the rest. A tool it does not know prints its name and one `key: value`
row per argument.

## What a line means

Each grid pane is titled with the seat, its model and a bar of its context use against the compact
limit (`== lead | deepseek-flash ==  ████████░░░░░░░░ 50% 75k/150k to compact`), renamed at most every
5 seconds as the reading moves. The feed itself carries no header line there. A feed in a plain
terminal (no `HERDR_PANE_ID`) prints the same line into the stream instead. The title ends with the
seat's status: `● working` while an event is under 30 s old, then `○ idle` and `○ quiet Nm`.

An **agy** seat's pane keeps the usage limits of the agy sign-in for the seat's model group on its bottom row,
under the stream and out of the title: `agy limits · 5h 89% left ↻1h29m · week 15% left ↻4d7h` (percent
remaining, time to reset). The row is written without a newline and erased before the next event, so it stays at
the bottom without a scroll region and never enters the scrollback; it is cut to the pane width. agy keeps its quota in memory
and writes it to no file, so the only reader is `agy -p /usage --output-format json`, which runs no model turn and
spends no tokens. The answer is shared by every agy seat on the machine: it is cached in
`~/.config/4genthub/cache/agy-usage.json` and refreshed by at most one probe per `AGY_USAGE_REFRESH_SECONDS`
(default 300, floor 60) across all panes, and a failed probe backs off for the same interval. A window agy does not
report for the group (Argon has only a weekly one), a reading not taken yet, or a window whose reset has passed
shows as `5h: unknown` / `week: unknown`. Other runtimes show no usage text: the grid has no reader for Claude's
5h/weekly windows (those live in Claude Code's own status line JSON, which an RPC-runner seat does not render).
An event line is prefixed with the time the seat logged it, shown only when the minute changes, and
with the context percentage, shown only when it crosses a 10% step.

With no `--width`, a pane follows its terminal: a zoom (`Ctrl -` / `Ctrl +`) or a resized pane changes the
column count, and the feed clears the view and draws the backlog again at the new width, so the lines fill the
pane. A `--width` the caller set is kept.

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
| `--room <ROOM>` | the room to watch; always pass it |
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
4genteam inputs open --room <ROOM>                # one "<SEAT> > input" pane under every seat pane
4genteam inputs open --room <ROOM> --seat <SEAT>  # only that seat gets one
4genteam inputs hide --room <ROOM>                # close them again
```

Each pane runs `4genteam input --room <ROOM> --seat <SEAT>`. A line typed there is delivered to the seat's
runtime session through this client's own transport, attributed to the pane's `AGENTHUB_SESSION_NAME`
(none in the owner's shell). `/hide` or Ctrl-D leaves the loop. Opening it twice adds nothing. The lead
window's input pane exists already.

### Send to a seat from a script

The same verb reads standard input, so the owner's shell (or a script) sends one message with a pipe:

```bash
printf 'message\n' | 4genteam input --room <ROOM> --seat <SEAT>   # prints "<SEAT> > sent"
```

Piped standard input is ONE message, however many lines it has, and prints one `sent`; a failed send exits
non-zero. Only the typed pane (a terminal) sends one message per line. This is the scripted owner-to-seat path: `4genteam send` decides a
send by the **sender's own seat links**, so from the owner's shell, which is no seat, it is refused ("this
shell's own seat could not be resolved"). A seat that is mid-run gets the text queued as a steer, after its
current tool calls. A runner older than `e86fa45f` dropped such a text after printing `sent`
(measured 2026-10-25: an idle seat had it within 20 s, busy seats never). Restart the seats on a
rebuilt `agenthub-runtime` to get the fix.

## Scrolling back

herdr keeps 10 MB of scrollback per pane: the mouse wheel, or copy mode (`prefix+[`, then
`PageUp`/`PageDown`, `q` or `Esc` to leave). Output stays live and follows the bottom. Anything
older than the replay is not in the pane — start the view again with a larger `--back`.

## Verify it, and what to do when it is silent

```bash
herdr workspace list      # look for "<ROOM> grid" and "<ROOM> lead"
4genteam ps <ROOM> --nodes   # one row per seat, with its lifecycle and activity
```

Run these from the shell that started `herdr`: it resolves its server from the home directory of
the process that calls it, so a process whose home is elsewhere does not find it.

If nothing appears, check those two first: a stopped seat has no pane, and a missing `herdr` or
`tmux` is a prerequisite to fix rather than work around.

## Seat loop guard

A seat that repeats one tool call turn after turn spends a model turn each time and never stops on its own
(aa2-gemini, 2026-11-01: `manage_task status task-249`, 20 times in three minutes, a `schedule` or an `lsof` between
every few). `4genteam stuck ROOM --loops --every 15s` reads every live seat's session log through the feed's own
adapters, so it covers agy, omp and Claude Code alike, and it uses no model.

- **It fires** when a seat makes the same call with the same arguments `LOOP_REPEAT_LIMIT` (8) times inside
  `LOOP_WINDOW_SECONDS` (600), or gets the same non-empty answer that many times in a row (numbers ignored), with no
  write between. A successful write that was not made before, or a message reaching the seat, starts the count over.
- **It does not fire** on a seat inside one long command (it logs nothing, and a repeat older than three minutes does
  not count), or on different work such as reading different files.
- **The ladder**, at least `LOOP_COOLDOWN_SECONDS` (120) apart and each rung earned by repeats made after the last:
  a steer through the seat's input path; an interrupt and a second steer; the seat is stopped and one row goes to the
  room's human. Each rung is logged in `logs/loop-guard-ROOM.log`, and the record shows on the seat's row in
  `4genteam room status`, in `4genteam ps` (status `looping` or `loop-stopped`) and in its pane title. A seat clear for
  a whole window is forgotten. A stopped seat comes back with `4genteam room reseat ROOM SEAT`; `stuck ROOM --loops
  --clear` drops the records. `stuck ROOM --loops --dry-run` shows what it would do. `LOOP_REPEAT_LIMIT=0` turns it off.
- **Who runs it:** `agent`, for every room it lists, and `up ROOM --room-only`, which starts this one guard and nothing
  else beside the room (it leaves when the room has had no live seat for five minutes). Running `up ROOM --room-only`
  again on a room that is up reads the room, restarts no seat, and starts the guard if it is missing. `room status`
  prints a `protection:` line when a running room has no guard, or a guard and no supervisor.
- **The defaults were measured** by replaying this machine's 458 agy and omp logs (2026-11-01, sampling every sixth
  record): 12 firings, 11 of them `manage_task status` polls of a background task and one a repeated identical answer.
  Replayed on the incident's own transcript it fires at its 8th call, 57 s in, 2 min 18 s before the owner's message.

## Rules

- **Never press Ctrl-C or type into a raw `tmux attach` pane of a seat, and never use `tmux send-keys`
  to message one.** The seat's runner reads that pane as input: Ctrl-C aborts the agent's turn, and a
  typed line becomes a prompt with no sender attached. It is not the documented route. On 2026-10-25,
  `tmux send-keys -t agenthub--aa2-rs--lead -l '...'` plus Enter left no trace in the busy lead's
  session log: a runner older than `e86fa45f` loses any prompt sent to a busy seat, whatever route it
  takes. Use `4genteam inputs`, the lead window's input pane, or `printf 'message\n' | 4genteam input
  --room <ROOM> --seat <SEAT>` instead.
- **A key, never a message, when a seat's own dialog holds it.** An agy seat has no runner, so a
  prompt agy draws itself (a "Run this command?" menu, a "... esc to dismiss" notice after launch)
  waits for a key. The first `4genteam input` sent while such a notice is up can be swallowed
  by it. Check with `4genteam capture <SESSION> --lines 25`, then send the one key to the room's own
  tmux server. Seats do not run on the default socket, so a plain `tmux send-keys` reports
  `error connecting to /tmp/tmux-1000/default`. The runtime's server lives under
  `<state dir>/tmux` (`~/.config/4genthub/tmux` by default):

  ```bash
  TMUX_TMPDIR=~/.config/4genthub/tmux tmux send-keys -t agenthub--<ROOM>--<SEAT> Escape
  TMUX_TMPDIR=~/.config/4genthub/tmux tmux send-keys -t agenthub--<ROOM>--<SEAT> 1   # a menu digit
  ```

  Send a single named key (`Escape`, `Enter`, a digit) and nothing else. Escape also cancels a
  running turn, so capture the pane first. Text still goes through `4genteam input`. A seat whose
  policy is `yolo` or omitted is started with `--dangerously-skip-permissions` and does not show
  the "Run this command?" menu (see AGENT-SETUP.md, the permission policy).
- A seat's own screen is the **secondary** view: read it to see what that seat is doing,
  never as the default way to watch a room.
- Every pane runs the client with `exec`, so when that client exits, the pane closes. That is why
  `q`, Ctrl-D or Ctrl-C in a pane ends the pane rather than leaving a dead one; reopen the view
  with the verb.
- A pane does not pick up a change while it runs: it started with the code and settings it read at
  launch. Reopen the view — and restart the compaction supervisor with `4genteam compact <ROOM>` —
  after changing the client or a `COMPACT_*` value.
- An agy seat is compacted by the same supervisor and limits as an omp seat, through agy's own
  `/compact` typed into its pane. agy has no runner, so its idle state is read from its transcript
  (the newest record is a final answer with no tool call), and `/compact` is typed only then:
  mid-turn agy would queue it as a plain message. An agy seat at its prompt gets `/compact` without
  the soft-limit notice, because a pasted notice starts a model turn. `4genteam room compact ROOM`
  does the same for agy seats, and refuses a mid-turn agy seat by name (exit 2). On its own,
  agy 1.4.9 compacts only near 270k tokens (measured), and its `/config` has no compaction setting.
  A seat that never goes idle is not left to that: past the warn limit it gets one witness window (180 s) to reach a
  safe point, past the hard limit none, then it is interrupted (Escape), and once its log has been quiet for
  `COMPACT_QUIET_SECONDS` it gets `/compact`. Measured on agy 1.4.12: `/compact` at the prompt and after an interrupt
  each wrote a `CHECKPOINT`; the interrupted turn's transcript ends on the cut call, so it never reads as idle.
- An agy seat also compacts itself, at the tool call that crossed the limit, with no supervisor needed (`up --room-only`).
  The seat start adds a `compact-trigger` entry to agy's global `~/.gemini/config/hooks.json`, and a server
  `agenthub_compact` (`4genteam mcp compact`) to its global `mcp_config.json` (agy has no per-seat file: the seat's
  `mcp.json` is omp's). Both do nothing outside a seat.
  - **What triggers it:** every tool call, built-in or MCP. agy runs `PreInvocation` before each model call, so after
    each batch of tools (measured on 1.4.12: `run_command`, `read_file`, MCP calls). A `PostToolUse` answer cannot reach
    the model, and no reading changes between a tool and the next model call, so nothing runs per tool.
  - **Under `COMPACT_LIMIT_TOKENS`:** the hook answers `{}`, with no file written. It costs about 4 ms per model call
    (50 runs in 0.20 s on a 128 KB transcript).
  - **Past it:** the hook takes the seat's claim and injects a one-turn message: end the turn now. A model that calls a
    tool anyway is stopped by the next `PreInvocation` (`terminate`), before the model is called.
  - **At the prompt:** agy's `Stop` hook starts a detached sender, which types `/compact` once, waits for the `CHECKPOINT`
    and sends the resume through the off-peak gate. A `Stop` past the limit with no claim (a turn with no tool, an
    interrupted turn) arranges it there. A `Stop` on an error, or with background work still running, sends nothing.
  - **The tools:** `compact_me` arranges the compact at any size. `context_usage` reads the context; past the limit its
    answer carries the same instruction. Both go through the function the hook uses.
  - **With the supervisor:** `<seat dir>/compact-claim.json` is taken under a lock by whoever will type `/compact`, and is
    fresh for 180 s, the supervisor's witness window. The supervisor skips a seat whose hook holds the claim, and it
    sends no notice or warning to a seat whose current conversation has run the hook, because a text typed mid-turn is
    queued and starts a turn in front of the `/compact`. Past the window the supervisor is the backstop, as above.
  - **Measured on a scratch seat (limit 25k):** crossing at 65k after a built-in `echo`, the model ended its turn, and
    `/compact` went out 5 s later: 66.3k to 23.3k in 7 s, resumed, remaining steps done. With the supervisor running
    beside it: one `CHECKPOINT`, no notice. `compact_me` at 19k and a forced `terminate`: the same.
  - The sender's log is `<seat dir>/compact-trigger.log`.
- An agy seat whose pane shows `agent executor error: ... maxMessageTokens must be positive, got 0`
  at its bottom for two supervisor passes (30 s apart) is restarted by the same supervisor, then
  given its last instruction again in the fresh conversation. That error breaks the conversation:
  every later input fails the same way, and `/compact` is one more such input. It is not the context
  limit (measured at 16k tokens). One restart per seat per 30 minutes; a second break inside that
  window is only logged.
- A text sent to an agy seat that agy puts in its queue (`alt+m to send immediately` under the
  prompt) is sent with alt+m by the client that sent it (`send`, `input`), so a brief typed just
  after `up` runs instead of waiting under an empty prompt.
- Keep backticks, apostrophes and `$` out of anything you type into an input pane: your own shell
  expands a backtick substitution before the client ever sees the text.
