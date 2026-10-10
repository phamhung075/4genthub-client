---
name: rig-runtime-switch
description: Switch an OpenRig rig to another runtime variant (e.g. claude-code to omp/DeepSeek) to keep a parked team working through a usage limit, revive its companion rigs, and open it on herdr to watch. Use when rig seats sit on a rate-limit dialog or a runtime pool is exhausted.
---

# Rig Runtime Switch — keep a team working through a limit

Swap a rig's runtime variant instead of waiting for a reset. Whether the switch took, and whether a
pool is still capped, are measurements rather than dates: `rig ps --nodes --rig <rig> --json` reports
every node's `runtime`, `model` and `startupStatus`, and `rig provider signals` reports each account's
usage window with the `resetsAt` it prints (task e7c7d763).

## When to use

- `rig parked --rig <rig>` shows seats **PARKED — needs-input (selection_prompt)** — the
  `/rate-limit-options` dialog (stop / wait for reset / upgrade).
- `rig capture <seat>` shows that dialog; quota evidence confirms the cap (e.g. transcript
  `quotaLimits.rate_limit_type = seven_day`, or a clean `env -u ANTHROPIC_BASE_URL claude -p …`
  probe returning "You've hit your weekly limit").
- The team's own brief authorizes the switch (e.g. "the omp/DeepSeek switch if Claude is capped").
- Note: a pool's cap is a usage signal, not a property of the pool — read it before switching
  (`rig provider signals` prints each account's used percent and its `resetsAt`), and do not switch to
  a pool whose signal says it is capped.

## Why it is safe

- **Variants are just files**: `~/.openrig/agenthub-seats/<rig>/rig/` holds `rig.yaml` (agy),
  `rig-claude.yaml`, and — once authored — `rig-omp.yaml`. Same `name`, same seat keys and edges.
- Stable seat keys ⇒ canonical session names (`<rig>-<seat>@<rig>`) survive the rebuild, so
  **queue obligations and `rig send` destinations keep resolving** (WORK counts carry over).
- `rig down` snapshots first (restorable); omp reads the launch dir's `.env` and `.mcp.json`, and
  needs `permission_policy: builtin:yolo` (the `full_bypass` warning at up-time is expected).

## Steps

1. **MCP task first** (`manage_task` create): cap evidence, plan, acceptance.
2. **Author the variant spec** next to the others — copy `rig-claude.yaml`, set per member
   `runtime: omp` + `model: deepseek/deepseek-flash`, keep `permission_policy: builtin:yolo`.
3. **DeepSeek key**: `ln -sfn <provider-env-file> <rig>/rig/.env` and `<rig>/.env`
   (the omp launch dir is one of these; both is harmless insurance).
4. **Dry-run**: `cd <rig>/rig && rig up rig-omp.yaml --plan` → expect `resolve_spec: ok`,
   `preflight: ok` (yolo warnings only).
5. **Have the user name the teardown.** The permission guard blocks
   `rig down <rig> --delete --force` until the user explicitly asks for that rig's teardown.
6. **Switch**: `rig down <rig> --delete --force` (rc=0; snapshot name is printed) then
   `rig up rig-omp.yaml --yes`.
7. **Verify**: `rig ps --nodes --rig <rig> --json` → every node
   `runtime=omp, model=deepseek/deepseek-flash, startupStatus=ready`; `activity=running` once the
   kickoff lands; queue WORK counts unchanged.
8. **Kick off the team** — `rig send <rig>-<seat>@<rig> "<mission>"` per seat. Keep backticks out of that message: **your own shell executes a backtick substitution before rig sees the text**, and the seat policy does not catch it (a refusal matches the command prefix, not a substitution inside an argument) — wrap the message in SINGLE quotes. Every kickoff needs:
   goal; files to read (`handover/_shared.md`, `OPENRIG-TASK.md`, `NEXT_GEN.md`,
   `state-<seat>.md`); **state at the cutoff** (what was approved/in-flight, pending push
   requests, uncommitted paths not to lose); the rules (commit locally only, stage by explicit
   path, never `git add -A`, no compatibility code, the user approves every push, don't touch other
   rigs); the flow (lead → go-dev → reviewer → lead status file).
   **Runtime note is mandatory, and it has INVERTED since this playbook was written**: an
   omp seat DOES get MCP and skills when its render carries mcp blocks — measured on this rig: the
   launch dir's `.mcp.json` declares `deepseek` and `agenthub_http`, and a seat resolves
   `mcp__agenthub_http__*` and `mcp__deepseek__*` (the renderer, `seatrenderer.RenderSeat`, emits the
   per-seat file to `$PI_CODING_AGENT_DIR/.mcp.json`; a seat with NO mcp block gets no
   file and no servers, so check before asserting either way). So do NOT tell a seat to ignore
   `mcp__*` or `deepseek_agent` instructions: they are live, and the seat's own AGENTS.md depends on
   them. Headless workers: `omp --model deepseek/deepseek-flash -p '<job>'`; `--no-tools` disables
   ALL built-in tools (`omp --help`), so add it only for a job that reads and writes nothing.
9. **Revive companion rigs**: supervisor loop `rig up <RIG> --existing --yes`
   (restores from the latest snapshot and resumes the session).
10. **Open the watch wall**: `4genteam watch --rig <rig>` (the client's binary; `4genteam up <rig>`
    opens the same wall as part of the lifecycle; replaces the earlier wall, same style; the user's
    standing choice; see `spawn-team`), one per companion rig. `rig terminal open <rig>` gives raw
    `tmux attach` tiles as a secondary view. Closing a pane only detaches; it never touches the
    daemon-owned session.
11. **Switch back after the reset**: `rig down <rig> --delete --force` → `rig up rig-claude.yaml`
    (variants are drop-in; the parked Claude sessions' state lives in the handover files + queue).

## Gotchas seen live

- Role files (`agents/<seat>/guidance/role.md`) mention MCP tools because the rendered seat carries
  them; on a seat rendered WITHOUT an mcp block those instructions are inert, and the kickoff message
  is what says so. The kickoff is still what makes the seat's *brief* runtime-correct.
- The old rig's `needs-input` dialog dies with the teardown — the user's "wait for reset" option
  is superseded by the switch (the snapshot + handover files are the continuity).
- omp seats are per-token billed and share one `DEEPSEEK_API_KEY`; concurrent sessions can make
  requests fail as if the model were unavailable — diagnose key contention before aborting.
- Prefer this playbook when the reset is far; the wait-for-reset path is covered by
  `~/.openrig/agenthub-seats/bin/limit-waiter.py` and `min-team-after-reset.sh`.
