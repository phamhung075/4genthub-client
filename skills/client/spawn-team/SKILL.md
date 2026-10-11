---
name: spawn-team
description: Continue or spawn an agent team — any OpenRig rig, any project — with one command, or spawn an in-session Claude Code team with the Proxy Pattern. Team lead fetches agent configs from MCP and injects them into teammate prompts (since team agents cannot access MCP tools).
---

# Spawn / Continue Team Skill

Two ways to run a team, and the two are not exclusive:

- **An OpenRig rig** — the durable form: seats in tmux, state in the daemon, survives restarts and resumes each seat's own session. **ALWAYS launch or restore using `4genteam up <rig> [--allow-peak]` or `4genteam continue <rig>`. NEVER use bare `rig up` to launch/resume a rig**, because bare `rig up` bypasses the entire client lifecycle: `rigd`, the compaction supervisor (`auto compact`), the `watchdog`, the status `bridge`, and the styled `watch` grid. See the sections below.
- **An in-session Claude Code team** — the Proxy Pattern below: teammates spawned from the current session, sharing its terminal.

**Only the in-session team below needs the lead to fetch each agent's configuration and inject it into the teammate's prompt** — a Claude Code teammate spawned by the `Task` tool does not inherit MCP connections. **A rig is the other case and needs none of that**: OpenRig renders each seat's own `agent.yaml` and guidance at launch, and a seat has its own MCP whenever its render carries mcp blocks — measured 2026-10-10 on `<RIG>`, where a seat resolves `mcp__agenthub_http__*` and `mcp__deepseek__*`. That is why a seat's own `AGENTS.md` tells it to drive `manage_task` itself instead of waiting for a lead to hand it configuration.

## Start or continue a team — always via `4genteam`

### 1. Start full stack (daemon + seats + rigd + auto-compact + bridge + watch wall):
```bash
4genteam up <rig> [--allow-peak]
```
`4genteam up` starts the entire supervisor stack in order: OpenRig daemon, seat sessions, `rigd` (which supervises the `compact` loop and `watchdog`), status `bridge`, and automatically opens `4genteam watch`.

### 2. Continue / restore an existing team with brief:
```bash
4genteam continue <rig> [brief-file] [--allow-peak]
```

- no arguments — lists the rigs and the briefs on disk
- `<rig>` — restores it from its latest snapshot (every seat resumes its own session), then briefs its seats
- `--no-brief` restore only; `--all` restores and briefs every stopped rig that has a brief, and exits non-zero when a rig it visited is not up
- seats are discovered from tmux at run time (`<rig>-<seat>@<rig>`, OpenRig's session naming) — no seat list is hardcoded, so a 1-seat rig and a 20-seat rig both work
- brief resolution: explicit path, else `~/.openrig/briefs/<rig>.txt`, else `~/.openrig/briefs/<rig>/<seat>.txt` for that seat alone, else nothing is sent
- the DeepSeek peak gate runs before the restore; `--allow-peak` skips it for this run
- log: `~/.openrig/logs/rig-continue.log`
- the standing watcher is `4genteam watchdog <rig> --watch`: when a rig the daemon expects to be running has no tmux sessions, it runs the same restore

### Writing a brief

One file per rig: `~/.openrig/briefs/<rig>.txt`. It is sent verbatim to every seat, prefixed with `Your seat: <seat>.`

- **Backticks inside a `rig send` message are executed by YOUR OWN shell before rig ever sees the text** — the effect is not mangling. Sending `rig send <seat> "text with a backtick-quoted command"` substitutes that command on the sending side; the seat policy does not catch it, because a refusal matches on the command prefix, not on a substitution buried inside an argument. (Measured 2026-10-10: a ruling reached a seat with every backtick-quoted name replaced by empty output, from a refused command inside them.) **Mitigation**: keep backticks, `$` and unbalanced apostrophes out of the message, or wrap the whole message in SINGLE quotes so your shell substitutes nothing.
- Phrase it as a re-scan request ("re-scan the board and take the unblocked items, correcting this list if stale"), not an asserted work list. Resumed seats hold fresher context and correct a stale list; on 2026-10-05 the lead corrected two already-verified items within a minute.
- Name the goal, what needs the user's approval, what is budget-capped, and the flow between seats.

### Spawning a new team (new project, new rig)

1. Write a rig spec: `pods[].members[]` with `id`, `agent_ref`, `runtime`, `model`, `cwd`, `permission_policy`, plus `edges` (`delegates_to`, `collaborates_with`, `escalates_to`). Working examples for two runtimes: `~/.openrig/agenthub-seats/<rig>/rig/rig-omp.yaml` (omp/DeepSeek) and `rig-claude.yaml` (claude-code).
2. `rig up <spec.yaml> --cwd <project-root>`; a one-seat rig needs no spec at all: `rig create <name>`.
3. Resize in place: `rig grow <rig-id> <members...>`, `rig add`, `rig shrink`, `rig remove`.
4. Write `~/.openrig/briefs/<rig>.txt` and run `4genteam continue <rig>` to brief it — the same command as continuing an existing team.

### Watching a team (the standing choice, 2026-10-07)

Always watch an omp seat team with the grid script, not with bare tiles:

```bash
4genteam watch --rig <rig>
```

- `4genteam` is this repository's Go binary (`go build -o ~/.local/bin/4genteam ./cmd/4genteam`), so it runs from anywhere once it is on `PATH`; `4genteam up <rig>` opens this wall as part of the full lifecycle. `4genteam continue` logs `watch wall skipped (herdr or 4genteam missing)` and carries on when either is absent; `RIG_CONTINUE_WATCH=0` turns the wall off for a headless run.

- One herdr workspace `<rig> grid`, one pane per seat, 2 columns, each pane a live feed of that seat's tool calls, reasoning (`~ think`), replies (`▸ say`) and incoming messages (`◂ in`).
- `4genteam continue <rig>` already runs it at the end (skip with `RIG_CONTINUE_WATCH=0`). After `rig up` or a runtime switch run it yourself. It never talks to a seat. It closes the rig's earlier grid and lead workspaces first, so a relaunch always gives the same single wall: the seat name, model and token meter in each pane, 2 columns, `--back 40 --width 200 --lines 60 --detail`.
- `rig terminal open <rig>` (one raw `tmux attach` tile per seat) is the secondary view: use it for a seat's own screen, not as the default.
- omp and claude-code seats have a session log (a claude-code seat shows its model and tokens too). A runtime with no log reader yet (Codex, agy) keeps its pane and header but shows no events.
- Full reference: `docs/WATCH.md`.

### Traps (any rig, any runtime)

- Liveness: `rig ps --nodes --rig <rig>`. `rig capture` can read blank; `tmux capture-pane -p -t <session>` does not lie.
- `[omp-runner] ERROR rpc: RPC response exceeded the transport limit` printed after `READY` is cosmetic on resume — the seat still answers. Ping it before considering a re-prime.
- Route: check the provider window before picking a runtime (`rig usage series --lane provider_window`). When Claude weekly is capped (it was 100 percent on 2026-10-05, reset 2026-10-06 12:00Z), the seats belong on omp/DeepSeek.
- Write domain: a repo worktree takes one writer team. A second team stays out of it (skill files, brief files, read-only audits).
- A stopped rig loses nothing: seats, sessions and snapshots live in the daemon database, so "down" is a lifecycle state, not data loss. Do not tear a rig down to bring it back — `4genteam continue <rig> [--allow-peak]` restores the rig, briefs the seats, and starts the full supervisor stack (never bare `rig up <rig> --existing`).

## When to Use

- A team (rig) is down and must continue its work — any rig, any project: `4genteam continue <rig>`
- A new team is needed for a new project: "Spawning a new team" above
- In-session parallel work across multiple files, or several agents for efficiency: Proxy Pattern below

## Proxy Pattern Explained

**Problem:** Team agents spawned via `Task` tool with `team_name` run in separate Claude Code processes. They do NOT inherit MCP server connections and cannot call `mcp__agenthub_http__call_seat` or `manage_seat`.

**Solution:** The team lead (which HAS MCP access) reads each seat's config BEFORE spawning, then injects it into the teammate's prompt.

## Workflow Steps

### 1. Analyze
- Read all relevant files
- Break request into independent tasks

### 2. Create Team & Tasks
```
TeamCreate(team_name="descriptive-name", description="purpose")
TaskCreate(subject="Task description", description="Details", activeForm="Doing X")
```

### 3. Fetch Agent Configs (Proxy Pattern)

For each unique seat needed, the team lead calls `mcp__agenthub_http__manage_seat` with `action="get"`, `room` and `seat` (or `mcp__agenthub_http__call_seat` with `room` and `seat`).

The result carries the seat's runtime, permission policy and rendered role files — this is the teammate's operating manual.
Cache results: if multiple teammates share the same seat type, fetch only once.

### 4. Spawn Teammates (Parallel)

Choose the right agent type per task. Inject the fetched seat config into each prompt.

**Teammate prompt template:**

```
You are a {agent_type} teammate on team "{team_name}". Your name is "{teammate_name}".

YOUR AGENT CONFIGURATION (loaded from MCP server by team lead):
{seat config from manage_seat / call_seat}

YOUR TASK (Task #{task_number}):
{task_description}

WORKFLOW:
1. Read the target file(s) to confirm current state
2. Make the required changes following the agent configuration above
3. Verify your changes by reading the file again
4. Use AskUserQuestion to ask the user to confirm your work
5. After user confirmation, mark Task #{task_number} as completed via TaskUpdate
6. Send a message to the team lead reporting completion

IMPORTANT: Do NOT skip step 4 (user confirmation) — it is mandatory.
```

**Agent type selection guide:**

| Task Type | Agent | subagent_type |
|-----------|-------|---------------|
| Write/edit code | coding-agent | `coding-agent` |
| Fix bugs | debugger-agent | `debugger-agent` |
| Write/run tests | test-orchestrator-agent | `test-orchestrator-agent` |
| Security audit | security-auditor-agent | `security-auditor-agent` |
| Code review | code-reviewer-agent | `code-reviewer-agent` |
| UI/frontend | ui-specialist-agent | `ui-specialist-agent` |
| Architecture | system-architect-agent | `system-architect-agent` |
| DevOps/infra | devops-agent | `devops-agent` |
| Documentation | documentation-agent | `documentation-agent` |
| Research | deep-research-agent | `deep-research-agent` |

The agent types available are the ones your own Claude Code installation defines; this client ships no
agent library.

### 5. Monitor
- Wait for teammate messages
- Verify results by reading files

### 6. Clean Up
```
SendMessage(type="shutdown_request", recipient="teammate-name")
TeamDelete()
```

## Key Rules

- Team lead MUST fetch the seat config via `manage_seat` or `call_seat` BEFORE spawning each teammate
- The fetched seat config MUST be injected into the teammate's Task prompt
- Each teammate MUST ask user confirmation before reporting done
- Spawn teammates in parallel when tasks are independent
- Team lead verifies results before shutting down
- The `subagent_type` in Task tool should match the seat type of the config you fetched
