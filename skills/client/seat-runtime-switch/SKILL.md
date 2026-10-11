---
name: seat-runtime-switch
description: Switch ONE seat of an OpenRig rig to another runtime/LLM (e.g. architect claude-code/opus to omp/deepseek-flash) when its provider is capped, then verify, brief the seat and reopen the watch grid. Use when the owner says "switch <seat> to deepseek", "claude is limited", or one seat's runtime must change. For switching a whole rig, use rig-runtime-switch.
---

# Switch one seat's runtime / LLM

`rig launch` and `rig remove` act on a node but cannot change a running node's runtime, and editing `rig.yaml` does not touch a live seat. The working path is a **variant spec** plus a rig teardown and rebuild. Example: an architect seat moved from `claude-code`/`opus` to `omp`/`deepseek/deepseek-flash`.

## Gate: the teardown needs the owner

`rig down <rig> --delete --force` kills **every** seat in the rig, not just the one switching, and the permission guard blocks it until the owner explicitly approves that rig's teardown. Before asking:

1. `rig ps --nodes --rig <rig> --json`: every seat's `runtime`, `model`, `startupStatus`, `activity`.
2. `rig capture <rig>-<seat>@<rig> --lines 25` for each working seat. **Do not tear down over a seat that is mid-task** (a rebase, a commit, a deploy) without telling the owner what will be interrupted.
3. Ask the owner (ask_question): switch now / wait for the busy seat / start the seat as a separate rig. Then proceed only on an explicit yes.

## Steps

`<L>` is the rig's launch directory (where the seats start and omp reads `.env`) and `<L>/rig/` is its spec directory.

1. **Back up and author the variant** (never edit the original spec in place):
   ```bash
   cd <L>/rig && cp rig.yaml rig.yaml.pre-<seat>-<runtime>.bak
   python3 - <<'E'
   s=open('rig.yaml').read()
   a="          id: <seat>\n          model: <old model>\n          permission_policy: builtin:yolo\n          profile: default\n          runtime: <old runtime>\n"
   b="          id: <seat>\n          model: deepseek/deepseek-flash\n          permission_policy: builtin:yolo\n          profile: default\n          runtime: omp\n"
   assert a in s
   open('rig-<seat>-<runtime>.yaml','w').write(s.replace(a,b))
   E
   ```
   Keep seat ids and edges identical: the canonical session names `<rig>-<seat>@<rig>` then survive, so queue WORK counts and `rig send` destinations still resolve. The block must match the spec's real indentation; check with `sed -n 1,50p rig.yaml` first.
2. **DeepSeek key, in the LAUNCH dir.** omp reads `<L>/.env`. Check it **before** the rebuild:
   ```bash
   grep -c DEEPSEEK_API_KEY <L>/.env   # must be 1; the link must not point at a file without it
   ln -sfn ~/.config/deepseek/env <L>/.env   # if it does; note the old target, to switch back
   ```
   **Pitfall seen live:** `<L>/.env` pointed at `~/.config/4genthub/.env` (no DeepSeek key), so the first rebuild left every omp seat on `runtime_error: No API key found for deepseek`, and `rig send` refused with "interactive prompt (runtime_error)". The 4genthub client reads `~/.config/4genthub/.env` from the home dir itself, so repointing `<L>/.env` does not break it. A key in your own shell env does not reach seats: tmux inherits the daemon's environment.
3. **Dry-run**: `rig up rig-<seat>-<runtime>.yaml --plan` expects `resolve_spec: ok`, `preflight: ok` (yolo `full_bypass` warnings are expected).
4. **Switch** (after the owner's yes): `rig down <rig> --delete --force` (prints a snapshot id), then `rig up rig-<seat>-<runtime>.yaml --yes`. Run both in one command; it can take about 30 s, so use a background task and wait for it.
5. **Verify with a measurement**, not silence:
   ```bash
   rig ps --nodes --rig <rig> --json    # every node runtime/model/startupStatus=ready
   rig ps --nodes --rig <rig>           # REASON must not read runtime_error
   rig capture <rig>-<seat>@<rig> --lines 10
   ```
   If REASON is `runtime_error`, read the capture: a missing key is step 2; fix it and repeat step 4.
6. **Brief the seats.** The rebuilt seats are fresh sessions with no memory. Send each busy or relevant seat a kickoff:
   ```bash
   4genteam send --from owner <rig-short> <seat> '<message>'
   ```
   Use **single quotes** and no backticks or `$`: your shell runs a backtick substitution before rig sees it. Say: the runtime changed and why; which files hold its state (`handover/_shared.md`, `state-<seat>.md`, `NEXT_GEN.md`); anything left half-done (check `git status` before continuing a rebase); the rules (commit locally only, stage by explicit path, owner approves every push).
7. **Reopen the watch grid**: `4genteam watch watch --rig <rig>` (idempotent; it rebuilds both workspaces).

## Switching back after the reset

Repeat with the original runtime and model, repoint `<L>/.env` to its recorded old target, or use the `.bak` spec. The seat's state lives in the handover files and the queue, not in the session.

## Relaying to a seat that refuses (`interactive prompt`)

`rig send` refuses when a pane sits at a prompt. Read `rig capture` first: if it shows only error text and no menu, `--dangerously-interact --reason "<why>"` stages the text without submitting it, and `rig send` will not accept a bare Enter (`message is empty`). Submit it with `tmux send-keys -t '<session>' Enter`. Never do this over a real selection or approval dialog.
