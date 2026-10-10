---
name: watch-team-grid
description: Open (or reopen) the user's watch view of an OpenRig team in herdr - a grid with one live pane per seat plus a lead window with an input pane. Use when the user says "open the grid", "reopen grid watch", "watch the team", "show me the agents", or after any restore, relaunch, runtime switch or script change.
---

# Watch a team: grid + lead window

The user's standing choice for watching an omp rig. It is read-only: it reads each seat's session log and never types into a seat. `rig terminal open` tiles are only the secondary view.

## Open it

```bash
cd <project-dir>
4genteam watch --rig <RIG>
```

`4genteam` is the client's Go binary, built from this repository (`cmd/4genteam`). `4genteam up <RIG>` opens the same wall as part of the lifecycle: daemon, compaction supervisor, this view, then the UI.

`watch` is idempotent: it closes the rig's earlier `<rig> grid` and `<rig> lead` herdr workspaces and rebuilds both, so run it again whenever the user asks to reopen.

Result:

- `<rig> grid` workspace: 2 columns, one pane per seat (sorted by name, filled top to bottom, left to right), each running `feed --back 40 --width 200 --lines 60 --detail`. Header per pane: seat name, model, token bar `used/150k to compact`.
- `<rig> lead` workspace: the lead's colored feed on top, a `lead > input` pane below (ratio 0.8); a line typed there goes to the lead with `rig send`.

Expected output: `grid: <n> seats in <cols> columns`. Only omp seats appear (they have session logs); a runtime the client has no log reader for keeps a pane and a header and shows no events.

## Verify (do not trust silence)

```bash
rig ps --nodes --rig <rig>                        # one row per seat, with its lifecycle
herdr workspace list                              # look for "<rig> grid" and "<rig> lead"
```

If `watch` prints nothing and no workspace appears, check `herdr workspace list` and `rig ps --nodes --rig <rig>` first: a stopped seat has no pane, and a missing `herdr` or `tmux` is a prerequisite to fix rather than work around.

**Before restoring any zero-size file, ask which kind of file it is: does its value live in HEAD, or in the working tree?** `git restore --source=HEAD --worktree -- <file>` returns a file whose value is committed to exactly what you want, and **silently reverts** a file whose value is uncommitted. On 2026-10-09 exactly that operation dropped the uncommitted layers of `CHANGELOG.md` and the operations guide — both then read as "restored" while holding the wrong revision (a passing size and a passing presence check, a failing identity). Decide with two reads: `git diff --stat -- <file>` (is there an uncommitted layer?) and `git log --oneline -1 -- <file>`; if the layer is there, **copy the file out first** — a `/tmp` copy with its `(mtime, ctime)` pair, or a stash patch. **If the value is uncommitted, the recovery source is the staged or dangling blob, never HEAD**, and any candidate must be confirmed by blob id before it is trusted.

The limits this line used to call "any local edit" are **settings, not recovery**: the client reads `COMPACT_LIMIT_TOKENS` (default 150_000), `COMPACT_WARN_TOKENS` (250_000), `COMPACT_HARD_TOKENS` (300_000) and `COMPACT_QUIET_SECONDS` from the environment, with a variable already in the environment winning over its one env file (`~/.config/4genthub/.env`) — a blank value means the default, and they are changed there, not by editing code. **The live ladder is: 150k = told to finish the job and stop; 250k = warned once, write down what you would need after compaction; 300k = the supervisor stops waiting for the seat to go idle — if it is working, it ends the turn for it (`tmux send-keys Escape`, at most one interrupt per 60s) and sends `/compact` as soon as the log is silent; a seat already idle is compacted straight away.** The old supervisor waited for idleness and said as much ("a /compact sent to a working seat is read as text"), which is why a busy seat could sit over the limit indefinitely. Any seat text still quoting a 200k ceiling or a 400k hard limit is stale.

## Rules

- Never Ctrl-C or type into a raw `tmux attach` pane of a seat: the runner reads stdin, Ctrl-C aborts the seat's work and typed text becomes a chat message.
- A running pane never picks up a code change (each pane runs the binary it started with). After editing the client — or changing a `COMPACT_*` value in its env file — run `4genteam watch --rig <rig>` again: it closes and rebuilds both workspaces, which is the only way the panes pick up the new value. The supervisor has the same property: restart it with `4genteam compact <rig>` (it stops the running one first) and read it back with `4genteam status <rig>`; its log is `logs/compact-supervisor-<rig>.log`.
- `rig-continue.sh <rig>` already runs `watch` at the end of every restore (skip with `RIG_CONTINUE_WATCH=0`).
- Keep backticks, apostrophes and `$` out of any message sent with `rig send`: **your own shell executes a backtick substitution before rig sees the text**, and the seat policy does not catch it (a refusal matches the command prefix, not a substitution inside an argument). Avoid them, or wrap the whole message in SINGLE quotes.

Full reference: `docs/WATCH.md` in this repository.
