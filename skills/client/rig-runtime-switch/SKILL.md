---
name: rig-runtime-switch
description: Switch a team rig to another runtime variant (e.g. claude-code to omp/DeepSeek) to keep a parked team working through a usage limit, revive its companion rigs, and open it on herdr to watch. Use when rig seats sit on a rate-limit dialog or a runtime pool is exhausted.
---

# Rig Runtime Switch — keep a team working through a limit

Swap a rig's runtime variant instead of waiting for a reset. Whether the switch took, and whether a
pool is still capped, are measurements rather than dates: `4genteam ps <rig> --nodes --json` reports
every node's `runtime`, `model` and `startupStatus`, and the predecessor CLI's `provider signals`
reports each account's usage window with the `resetsAt` it prints — `4genteam` has no verb for that
read (task e7c7d763).

## Which `rig`: `4genteam` or the rig CLI

`4genteam` is this client's own binary: it answers no `rig` verb, and its `stop` is machine-wide, not a room teardown. **It cannot switch a runtime in place, and it prints no sequence that completes the switch** — `4genteam sync switch` says exactly that and names the two steps that do exist:

- The room's definition and its seats' files are written by the launcher: `4genteam up <room> --team <dir>`. (The client's `sync room` verb rewrote them from the cloud's rigspec until 2026-10-15, when the route behind it was deleted - see the client CHANGELOG.)
- `4genteam seat reseat <rig> --seat <name>` stops and restarts a seat so it loads the configuration it now has.

Neither runs inside `sync switch` (both write, and a restart is peak-gated for a DeepSeek seat), and **whether a restart carries a changed RUNTIME is not established**. The switch is the owner's call.

The spec- and node-level verbs the predecessor CLI had (`down --delete`, `up <spec>.yaml`, `launch`, `remove`, `parked`, `provider signals`) have no `4genteam` counterpart, and **this skill no longer prints the teardown sequence**: measured on this box, its middle line stopped the MACHINE's safeguards (`agent` reaped with its children, for every room) and its last could not run at all (`--team DIR is required`). That CLI's daemon is not running either — it answers `daemon unreachable ... Resolved: partial via session name`.

The reads are native:

```bash
4genteam ps <rig> --nodes --json        # runtime, model, sessionStatus, startupStatus per seat
4genteam ps <rig> --nodes               # one row per seat: RUNTIME MODEL STATUS ... CTX ACTIVITY ERROR
4genteam capture <session> --lines 25   # one pane, verbatim (a canonical `<room>-<seat>@<room>` is translated)
4genteam status <rig>                   # the safeguard rows
```

`4genteam ps <rig> [--nodes|--compact|--grid|--json]` reads tmux and the seat transcripts, marks a seat `quota` when its transcript tail shows a quota or credit message, and prints this client's own JSON — not the predecessor's `rig ps --nodes --room R --json` shape.

## When to use

- A seat sits in the `/rate-limit-options` dialog (stop / wait for reset / upgrade): `4genteam ps <rig> --nodes` shows its RUNTIME/MODEL/STATUS and `4genteam capture <session> --lines 25` shows the dialog itself. The predecessor CLI's `parked` reported these; `4genteam` has no `parked`.
- `4genteam capture <session> --lines 25` shows that dialog; quota evidence confirms the cap (e.g. transcript
  `quotaLimits.rate_limit_type = seven_day`, or a clean `env -u ANTHROPIC_BASE_URL claude -p …`
  probe returning "You've hit your weekly limit").
- The team's own brief authorizes the switch (e.g. "the omp/DeepSeek switch if Claude is capped").
- Note: a pool's cap is a usage signal, not a property of the pool — read it before switching
  (the predecessor CLI's `provider signals` printed each account's used percent and its `resetsAt`; `4genteam` has no verb for that read), and do not switch to
  a pool whose signal says it is capped.

## Why it is safe

- **A room's seats are its definition**: `~/.config/4genthub/rooms/<room>/spec.json` holds each seat's
  `runtime`, `model`, `permission_policy` and `cwd`, and the launcher (`4genteam up <room> --team <dir>`)
  rewrites it. (The predecessor kept `rig*.yaml` variants under
  `~/.config/4genthub/seats/<rig>/rig/`, a path this client does not use.)
- Stable seat keys ⇒ canonical session names (`<rig>-<seat>@<rig>`) survive the rebuild, so
  **queue obligations and `4genteam send` destinations keep resolving** (WORK counts carry over).
- `4genteam room stop <room>` leaves the room's directory in place (`spec.json`, `seats/`, `logs/`), so
  the room can be started again — it is a stop, not a delete. omp reads the launch dir's `.env` and
  `.mcp.json`, and needs `permission_policy: builtin:yolo` (the `full_bypass` warning at up-time is expected).

## Steps

1. **Record the switch in cloud**, once per seat:
   ```bash
   4genteam sync switch <rig> <seat> --runtime omp --model deepseek/deepseek-flash --apply none --reason "<why>"
   ```
   This ONLY records the occupant in 4genthub cloud; it never changes a running seat. **Do not use `4genteam seat reseat` to change a runtime**: it restarts a seat so that it reloads the skills, guide and cwd it is now configured with — its own help claims no runtime change — and it kills the seat's tmux session, so a seat in the middle of a turn loses that turn.
2. **Author or select the variant spec**:
   In `~/.config/4genthub/rooms/<room>/spec.json` (written by the launcher, `4genteam up <room> --team <dir>`):
   set per seat `runtime: omp` + `model: deepseek/deepseek-flash`, keep `permission_policy`.
   Check the definition's seats equal the live seats — `4genteam ps <rig> --nodes` lists the live ones — since a definition can carry a seat the room does not run.
3. **DeepSeek key**:
   Ensure `~/.config/4genthub/.env` and `~/.config/deepseek/.env` contain `DEEPSEEK_API_KEY`.
4. **Read both sides before restarting anything**: `4genteam room status <room>` prints one line per seat (SEAT RUNTIME STATE SESSION) as the runtime sees it, and the definition it will load next is written by the launcher (`4genteam up <room> --team <dir>`). A definition that disagrees with the live room is visible here, before a seat is touched.
5. **Apply it** — the two native steps, neither of which `sync switch` runs for you (both write, and a restart is peak-gated for a DeepSeek seat):
   ```bash
   4genteam seat reseat <rig> --seat <seat> [--allow-peak]     # stop and restart one seat, once it is quiet
   ```
   `seat reseat` waits for the seat to be quiet; `--force` skips that wait and a seat mid-turn loses the turn. **Whether the restart carries a changed RUNTIME is not established — measure it after the restart** (`4genteam ps <rig> --nodes --json` per seat) rather than assuming it. The predecessor CLI's teardown/relaunch sequence is deliberately not printed here: measured, its middle line stopped the machine's safeguards rather than the room's, and its last could not run at all.
6. **Ensure the client lifecycle is running**:
   ```bash
   4genteam up <rig> --team <the room's team dir> [--allow-peak]
   ```
   `--team DIR` is required — the room's seats come from the local team definition that directory holds, and there is no default. For a room that is stopped rather than new, `4genteam continue <rig>` restores it and briefs its seats.
7. **Verify & watch**:
   `4genteam watch --room <rig>` opens the styled 2-column live monitor.
   `4genteam ps <rig> --nodes --json` must show `runtime: omp`, `model: deepseek/deepseek-flash`, `startupStatus: ready` for every seat; `4genteam ps <rig>` must show 0 needing attention.
   `4genteam status <rig>` verifies compaction supervisor and safeguards are running.

## Gotchas seen live

- A seat's own role and instructions arrive as **skills** — the client skill tree's share, room and seat
  tiers — and its tool surface as `<seat dir>/mcp.json`, written by the runtime at every start. On a seat
  whose seat type declares no mcp block that file carries no server, so tool instructions in a skill are
  inert, and the kickoff message is what says so. The kickoff is still what makes the seat's *brief*
  runtime-correct.
- The old runtime's `needs-input` dialog dies with the seat's restart — the user's "wait for reset" option
  is superseded by the switch (the handover files are the continuity).
- omp seats are per-token billed and share one `DEEPSEEK_API_KEY`; concurrent sessions can make
  requests fail as if the model were unavailable — diagnose key contention before aborting.
- Prefer this playbook when the reset is far.
