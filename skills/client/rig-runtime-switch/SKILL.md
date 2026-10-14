---
name: rig-runtime-switch
description: Switch a team rig to another runtime variant (e.g. claude-code to omp/DeepSeek) to keep a parked team working through a usage limit, revive its companion rigs, and open it on herdr to watch. Use when rig seats sit on a rate-limit dialog or a runtime pool is exhausted.
---

# Rig Runtime Switch — keep a team working through a limit

Swap a rig's runtime variant instead of waiting for a reset. Whether the switch took, and whether a
pool is still capped, are measurements rather than dates: `rig ps --nodes --rig <rig> --json` reports
every node's `runtime`, `model` and `startupStatus`, and `$RIG provider signals` reports each account's
usage window with the `resetsAt` it prints (task e7c7d763).

## Which `rig`: `4genteam` or the rig CLI

`4genteam` is its own binary and does not answer `rig` verbs; its `stop` is machine-wide, not a rig teardown. Everything that works on a rig spec or a node (`down --delete`, `up <spec>.yaml`, `capture`, `launch`, `parked`, `provider signals`) needs the rig CLI itself, so call it by path:

```bash
RIG="node <path to the rig CLI dist/index.js>"
$RIG ps --nodes --rig <rig> --json     # runtime, model, sessionStatus, startupStatus per seat
```

`4genteam ps <rig> [--nodes|--compact|--grid|--json]` reads tmux and the seat transcripts, marks a seat `quota` when its transcript tail shows a quota or credit message, and answers the `rig ps --nodes --rig R --json` contract.

## When to use

- `$RIG parked --rig <rig>` shows seats **PARKED — needs-input (selection_prompt)** — the
  `/rate-limit-options` dialog (stop / wait for reset / upgrade).
- `$RIG capture <seat>` shows that dialog; quota evidence confirms the cap (e.g. transcript
  `quotaLimits.rate_limit_type = seven_day`, or a clean `env -u ANTHROPIC_BASE_URL claude -p …`
  probe returning "You've hit your weekly limit").
- The team's own brief authorizes the switch (e.g. "the omp/DeepSeek switch if Claude is capped").
- Note: a pool's cap is a usage signal, not a property of the pool — read it before switching
  (`$RIG provider signals` prints each account's used percent and its `resetsAt`), and do not switch to
  a pool whose signal says it is capped.

## Why it is safe

- **Variants are just files**: `~/.config/4genthub/seats/<rig>/rig/` holds `rig.yaml` (agy),
  `rig-claude.yaml`, and — once authored — `rig-omp.yaml`. Same `name`, same seat keys and edges.
- Stable seat keys ⇒ canonical session names (`<rig>-<seat>@<rig>`) survive the rebuild, so
  **queue obligations and `rig send` destinations keep resolving** (WORK counts carry over).
- `$RIG down` snapshots first (restorable); omp reads the launch dir's `.env` and `.mcp.json`, and
  needs `permission_policy: builtin:yolo` (the `full_bypass` warning at up-time is expected).

## Steps

1. **Record the switch in cloud**, once per seat:
   ```bash
   4genteam sync switch <rig> <seat> --runtime omp --model deepseek/deepseek-flash --apply none --reason "<why>"
   ```
   This ONLY records the occupant in 4genthub cloud; it never changes a running seat. **Do not use `4genteam seat reseat` to change a runtime**: it kills the seat's tmux session and relaunches through `rig launch`, which fails when the rig is not running in the runtime daemon, leaving the seat dead and the runtime unchanged.
2. **Author or select the variant spec**:
   In `~/.config/4genthub/seats/<rig>/rig/`:
   `rig.yaml` (agy), `rig-claude.yaml` (claude-code), `rig-omp.yaml` (omp/DeepSeek).
   Set per member `runtime: omp` + `model: deepseek/deepseek-flash`, keep `permission_policy: builtin:yolo`.
   Check the spec's seats equal the live seats: `rig-omp.yaml` can carry more seats than the live room and a member left on `claude-code`. `rig-architect-omp.yaml` is the 5-seat all-omp spec (lead, architect, go-dev, fe-dev, reviewer).
3. **DeepSeek key**:
   Ensure `~/.config/4genthub/.env` and `~/.config/deepseek/.env` contain `DEEPSEEK_API_KEY`.
4. **Dry-run**:
   `cd <rig>/rig && $RIG up rig-omp.yaml --plan` → expect `resolve_spec: ok`, `preflight: ok`.
5. **Teardown previous rig** (requires owner's explicit confirmation):
   `$RIG down <rigId> --snapshot --force --delete --json` (use the rig id from `ps`; the name form can answer `HTTP 409`).
   If the answer is `deleteBlocked` with `Could not confirm session ... is absent ... no server running`, the tmux server is already gone and the rig cannot check. Start a throwaway server (`tmux new-session -d -s zz-teardown-probe`), repeat the down, then `tmux kill-session -t zz-teardown-probe`. Then launch the spec: `$RIG up <spec>.yaml`.
6. **Re-launch with the client lifecycle (`4genteam up`)**:
   After `$RIG up <spec>.yaml` creates the seats, do not stop there. Always bring the rig back via `4genteam up` or `4genteam continue` so that `rigd`, the auto-compaction supervisor, the watchdog, and the status bridge are running:
   ```bash
   4genteam up <rig> [--allow-peak]
   ```
   (If pointing to a specific new spec file, launch the spec once and immediately ensure `4genteam up <rig>` or `4genteam compact <rig>` is running).
7. **Verify & watch**:
   `4genteam watch --rig <rig>` opens the styled 2-column live monitor.
   `$RIG ps --nodes --rig <rig> --json` must show `runtime: omp`, `model: deepseek/deepseek-flash`, `startupStatus: ready` for every seat; `4genteam ps <rig>` must show 0 needing attention.
   `4genteam status <rig>` verifies compaction supervisor and safeguards are running.

## Gotchas seen live

- Role files (`agents/<seat>/guidance/role.md`) mention MCP tools because the rendered seat carries
  them; on a seat rendered WITHOUT an mcp block those instructions are inert, and the kickoff message
  is what says so. The kickoff is still what makes the seat's *brief* runtime-correct.
- The old rig's `needs-input` dialog dies with the teardown — the user's "wait for reset" option
  is superseded by the switch (the snapshot + handover files are the continuity).
- omp seats are per-token billed and share one `DEEPSEEK_API_KEY`; concurrent sessions can make
  requests fail as if the model were unavailable — diagnose key contention before aborting.
- Prefer this playbook when the reset is far.
