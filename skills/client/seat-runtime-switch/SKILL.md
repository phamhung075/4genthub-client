---
name: seat-runtime-switch
description: Switch ONE seat of a team room to another runtime/LLM (e.g. architect claude-code/opus to omp/deepseek-flash) when its provider is capped, then verify, brief the seat and reopen the watch grid. Use when the owner says "switch <seat> to deepseek", "claude is limited", or one seat's runtime must change. For switching a whole room, use room-runtime-switch.
---

# Switch one seat's runtime / LLM

Nothing changes a *running* seat's runtime in place: the predecessor CLI's `launch` and `remove` acted on a node without changing one, and editing a spec never touched a live seat. `4genteam` says the same of itself — `4genteam sync switch` prints no sequence that completes a runtime change, and names only the two things that exist (the room's definition and each seat's files are written by the launcher, `4genteam up <room> --team <dir>`; `4genteam seat reseat <room> --seat <name>` stops and restarts a seat so it loads the configuration it now has). Example: an architect seat moved from `claude-code`/`opus` to `omp`/`deepseek/deepseek-flash` — a definition change plus a restart.

## Which binary: `4genteam`, and what it can and cannot do

`4genteam` is this client's own binary, not the predecessor's `rig` CLI: its `stop` is machine-wide, not a room teardown. **It cannot switch a runtime in place, and it prints no sequence that completes the switch** — `4genteam sync switch` says exactly that and names the two steps that do exist:

- The room's definition and its seats' files are written by the launcher: `4genteam up <room> --team <dir>`. (The client's `sync room` verb rewrote them from the cloud's roomspec until 2026-10-15, when the route behind it was deleted - see the client CHANGELOG.)
- `4genteam seat reseat <room> --seat <name>` stops and restarts a seat so it loads the configuration it now has.

Neither runs inside `sync switch` (both write, and a restart is peak-gated for a DeepSeek seat), and **whether a restart carries a changed RUNTIME is not established**. The switch is the owner's call.

The spec- and node-level verbs the predecessor CLI had (`down --delete`, `up <spec>.yaml`, `capture`, `launch`, `parked`, `provider signals`) have no `4genteam` counterpart, and **this skill no longer prints the teardown sequence**: measured on this box, its middle line stopped the MACHINE's safeguards (`agent` reaped with its children, for every room) and its last could not run at all (`--team DIR is required`). That CLI's daemon is not running either — it answers `daemon unreachable ... Resolved: partial via session name`.

The reads are native:

```bash
4genteam ps <room> --nodes --json        # runtime, model, sessionStatus, startupStatus per seat
4genteam ps <room> --nodes               # one row per seat: RUNTIME MODEL STATUS ... CTX ACTIVITY ERROR
4genteam capture <session> --lines 25   # one pane, verbatim (a canonical `<room>-<seat>@<room>` is translated)
4genteam room status <room>             # one line per seat: SEAT RUNTIME STATE SESSION
```

## Gate: the restart and the definition change need the owner

A room-wide rebuild kills **every** seat in the room, not just the one switching, and even a single-seat restart ends that seat's turn. Nothing below runs without the owner's explicit yes. Before asking:

1. `4genteam ps <room> --nodes --json`: every seat's `runtime`, `model`, `startupStatus`, `activity`.
2. `4genteam capture <room>-<seat>@<room> --lines 25` for each working seat. **Do not restart over a seat that is mid-task** (a rebase, a commit, a deploy) without telling the owner what will be interrupted.
3. Ask the owner (ask_question): switch now / wait for the busy seat / start the seat as a separate room. Then proceed only on an explicit yes.

## Steps

`<room>` is the room's slug and `<room>` its room name — the same string in this layout. Its definition is `~/.config/4genthub/rooms/<room>/spec.json`, written by the launcher (`4genteam up <room> --team <dir>`).

1. **Record the change in cloud, once per seat.** The cloud is the authority on the occupant, and the verb prints what it did not apply:
   ```bash
   4genteam sync switch <room> <seat> --runtime omp --model deepseek/deepseek-flash --apply none --reason "<why>"
   ```
   Keep seat keys and edges identical: the canonical session names `<room>-<seat>@<room>` then survive, so queue WORK counts and `4genteam send` destinations still resolve.
2. **DeepSeek key.** omp needs `DEEPSEEK_API_KEY` in its environment. The two files this client and its launchers read are `~/.config/4genthub/.env` and `~/.config/deepseek/.env` (both hold it — measured 2026-10-14), and the seats' tmux server needs it too. Check it **before** restarting a seat:
   ```bash
   grep -c DEEPSEEK_API_KEY ~/.config/4genthub/.env ~/.config/deepseek/.env   # each must be 1
   tmux set-environment -g DEEPSEEK_API_KEY <key>                             # for the seats' panes
   ```
   **Pitfall seen live:** a launch environment that lacked the key left omp seats on `runtime_error: No API key found for deepseek`, and the send verb refused with "interactive prompt (runtime_error)".
3. **Read both sides**: `4genteam room status <room>` prints one line per seat (SEAT RUNTIME STATE SESSION) as the runtime sees it, and the definition the seats will load is written by the launcher (`4genteam up <room> --team <dir>`). This client has no dry-run verb for a definition change — these two reads are what makes a disagreement visible before a seat is touched.
4. **Switch** (after the owner's yes) — the two native steps, and nothing that stops the machine's safeguards:
   ```bash
   4genteam seat reseat <room> --seat <seat> [--allow-peak]     # stop and restart that one seat, once it is quiet
   ```
   `seat reseat` waits for the seat to be quiet; `--force` skips that wait and a seat mid-turn loses the turn. **Whether the restart carries a changed RUNTIME is not established — measure it (step 5)** rather than assuming it. If the room is not running at all, `4genteam up <room> --team <the room's team dir>` starts it (`--team DIR` is required), and `4genteam continue <room>` restores a stopped room and briefs its seats.
5. **Verify with a measurement**, not silence:
   ```bash
   4genteam ps <room> --nodes --json              # every seat runtime/model/startupStatus=ready
   4genteam ps <room> --nodes                     # the ERROR column must not read runtime_error
   4genteam capture <room>-<seat>@<room> --lines 10
   ```
   If the ERROR column reads `runtime_error`, read the capture: a missing key is step 2; fix it and repeat step 4.
6. **Brief the seats.** The rebuilt seats are fresh sessions with no memory. Send each busy or relevant seat a kickoff:
   ```bash
   4genteam send --from owner <room-short> <seat> '<message>'
   ```
   Use **single quotes** and no backticks or `$`: your shell runs a backtick substitution before `4genteam` sees it. Say: the runtime changed and why; which files hold its state (`handover/_shared.md`, `state-<seat>.md`, `NEXT_GEN.md`); anything left half-done (check `git status` before continuing a rebase); the rules (commit locally only, stage by explicit path, owner approves every push).
7. **Reopen the watch grid**: `4genteam watch --room <room>` (idempotent; it rebuilds both workspaces).

## Switching back after the reset

Repeat with the original runtime and model through the same steps: `4genteam sync switch` records it, the launcher (`4genteam up <room> --team <dir>`) writes the definition, and `4genteam seat reseat` loads it. The seat's state lives in the handover files and the queue, not in the session.

## Relaying to a seat that refuses (`interactive prompt`)

`4genteam send` refuses when a pane sits at a prompt, and it refuses `--dangerously-interact`/`--reason` outright (`--dangerously-interact is not a switch the runtime has`), so the staging path the old text used no longer exists. Read the pane first with `4genteam capture <session> --lines 25`: if it shows only error text and no menu, the way in is `tmux send-keys -t '<session>'` from that pane's own tmux server. Never do this over a real selection or approval dialog.
