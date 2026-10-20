---
name: spawn-team
description: Continue or spawn an agent team with 4genteam, or spawn an in-session Claude Code team with the Proxy Pattern. Team lead fetches agent configs from MCP and injects them into teammate prompts (since team agents cannot access MCP tools).
---

# Spawn / Continue Team Skill

Two ways to run a team, and the two are not exclusive:

- **A team room** — the durable form: seats in tmux supervised by 4genteam, survives restarts and resumes each seat's own session. **ALWAYS launch or restore using `4genteam up <rig> --team <DIR> [--allow-peak]` or `4genteam continue <rig>`** (`--team DIR` is required: the room's seats come from the local team definition that directory holds, and there is no default). **NEVER launch or resume with the predecessor orchestrator's `rig up`**: it bypasses this client's whole lifecycle — `agent`, the compaction supervisor (`auto compact`), the `watchdog`, the status `bridge`, and the styled `watch` grid — and its daemon is not running (measured 2026-10-14: `rig whoami` answers `daemon unreachable`). See the sections below.
- **An in-session Claude Code team** — the Proxy Pattern below: teammates spawned from the current session, sharing its terminal.

**Only the in-session team below needs the lead to fetch each agent's configuration and inject it into the teammate's prompt** — a Claude Code teammate spawned by the `Task` tool does not inherit MCP connections. **A room is the other case and needs none of that**: the runtime installs each seat's skills from the client tree (share, then the room, then the seat's own tier) and writes the seat's `<seat dir>/mcp.json` at every start, so a seat has its own MCP whenever its seat type declares mcp blocks — measured 2026-10-10 on `<ROOM>`, where a seat resolves `mcp__agenthub_http__*` and `mcp__deepseek__*`. That is why a seat's own `AGENTS.md` tells it to drive `manage_task` itself instead of waiting for a lead to hand it configuration.

## Start or continue a team — always via `4genteam`

### 1. Start full stack (seats + agent + auto-compact + watchdog + bridge + watch wall):
```bash
4genteam up <room> --team <DIR> [--allow-peak]
```
`4genteam up` starts the entire supervisor stack in order: seat sessions, `agent` (which supervises the `compact` loop and `watchdog`), status `bridge`, and automatically opens `4genteam watch`.

### 2. Continue / restore an existing team with brief:
```bash
4genteam continue <room> [brief-file] [--allow-peak]
```

- no arguments — lists the rooms and the briefs on disk
- `<room>` — restores it from its latest snapshot (every seat resumes its own session), then briefs its seats
- `--no-brief` restore only; `--all` restores and briefs every stopped room that has a brief, and exits non-zero when a room it visited is not up
- seats are discovered from tmux at run time (`<room>-<seat>@<room>`) — no seat list is hardcoded, so a 1-seat room and a 20-seat room both work
- brief resolution: explicit path, else `~/.config/4genthub/briefs/<room>.txt`, else `~/.config/4genthub/briefs/<room>/<seat>.txt` for that seat alone, else nothing is sent
- the DeepSeek peak gate runs before the restore; `--allow-peak` skips it for this run
- log: `~/.config/4genthub/logs/room-continue.log`
- the standing watcher is `4genteam watchdog <room> --watch`: when a room the daemon expects to be running has no tmux sessions, it runs the same restore

### Writing a brief

One file per room: `~/.config/4genthub/briefs/<room>.txt`. It is sent verbatim to every seat, prefixed with `Your seat: <seat>.`

- **Backticks inside a `4genteam send` message are executed by YOUR OWN shell before `4genteam` ever sees the text** — the effect is not mangling. Sending `4genteam send <seat> "text with a backtick-quoted command"` substitutes that command on the sending side; the seat policy does not catch it, because a refusal matches on the command prefix, not on a substitution buried inside an argument. (Measured 2026-10-10: a ruling reached a seat with every backtick-quoted name replaced by empty output, from a refused command inside them.) **Mitigation**: keep backticks, `$` and unbalanced apostrophes out of the message, or wrap the whole message in SINGLE quotes so your shell substitutes nothing.
- Phrase it as a re-scan request ("re-scan the board and take the unblocked items, correcting this list if stale"), not an asserted work list. Resumed seats hold fresher context and correct a stale list; on 2026-10-05 the lead corrected two already-verified items within a minute.
- Name the goal, what needs the user's approval, what is budget-capped, and the flow between seats.

### Spawning a new team (new project, new room)

1. Write the room's team definition: a `team.json` in a directory of your choice, holding `room{slug,name}`, `modules[]`, `seats[]` (`seat_key`, `seat_type`, `runtime`, `model`, `permission_policy`, `cwd`, `env`), `links[]` and `overlays{}`. **`links[]` are required: a room of more than one seat with none is refused at load** (`a room of N seats needs its links; with none, no seat can message another and the topology has no edges`), by `4genteam up` and by `team apply`, before anything is sent. Kinds: `delegates_to`, `collaborates_with`, `escalates_to`, `can_observe`, `spawned_by`; the minimum is the lead `delegates_to` every other seat, each worker `delegates_to` the reviewer, every non-lead seat `escalates_to` the lead. Check them after the room is up: `4genteam whoami --json` returns `edges`, and the cloud lists them under `/rooms/<room>/seats/<seat>/links`. Working examples in this repository: `scripts/team/<room>/team.json` and `~/.config/4genthub/rooms/<room>/team/team.json`.
2. `4genteam up <room> --team <that directory>` — the room and its seats come from the definition it holds, and the daemon starts with them. This client has no `rig create` and no `rig up <spec>.yaml`: a room is created from its local definition and started the same way.
3. **Resizing a live room has no verb on this client.** The predecessor CLI's `grow`/`add`/`shrink`/`remove` are gone: a room's seats are its definition, so a change means editing `team.json`, starting it with `4genteam up <room> --team <dir>` (which writes the definition), and `4genteam seat reseat <rig> --seat <name>` for the seats that must reload it.
4. **Every seat starts with `~/.config/4genthub/.env` in its environment** (the runtime reads it when it starts the seat), so `AGENTHUB_URL`, `AGENTHUB_TOKEN` and the provider keys need no per-seat copy. A key a seat's `env` names wins over the file, and the runtime's own `AGENTHUB_*` keys win over both. The runtime also writes `<seat dir>/mcp.json` with the `agenthub_http` server (`${AGENTHUB_URL}/mcp`, `Bearer ${AGENTHUB_TOKEN}`; variable names only, no secret), so a started seat mounts the nine MCP tools. A running seat never re-reads its environment: after changing the file, `4genteam room reseat <room> <seat>`. Never print or read the token to check it; test for presence, or have the seat call `manage_project` with `action: list`.
5. Write `~/.config/4genthub/briefs/<room>.txt` and run `4genteam continue <room>` to brief it — the same command as continuing an existing team.

### Watching a team (the standing choice, 2026-10-07)

Always watch an omp seat team with the grid script, not with bare tiles:

```bash
4genteam watch --room <room>
```

- `4genteam` is this repository's Go binary (`go build -o ~/.local/bin/4genteam ./cmd/4genteam`), so it runs from anywhere once it is on `PATH`; `4genteam up <room>` opens this wall as part of the full lifecycle. `4genteam continue` logs `watch wall skipped (herdr or 4genteam missing)` and carries on when either is absent; `ROOM_CONTINUE_WATCH=0` turns the wall off for a headless run.

- One herdr workspace `<room> grid`, one pane per seat, 2 columns, each pane a live feed of that seat's tool calls, reasoning (`~ think`), replies (`▸ say`) and incoming messages (`◂ in`).
- `4genteam continue <room>` already runs it at the end (skip with `ROOM_CONTINUE_WATCH=0`). After `4genteam up <room> --team <DIR>` or a runtime switch run it yourself. It never talks to a seat. It closes the room's earlier grid and lead workspaces first, so a relaunch always gives the same single wall: the seat name, model and token meter in each pane, 2 columns, `--back 40 --width 0 --lines 60 --detail`.
- The secondary view is a seat's own screen, and this client has no verb that opens one raw `tmux attach` tile per seat: read that pane with `4genteam capture <session>`, or attach by hand. Use it for a seat's own screen, not as the default.
- omp and claude-code seats have a session log (a claude-code seat shows its model and tokens too). A runtime with no log reader yet (Codex, agy) keeps its pane and header but shows no events.
- Full reference: `docs/WATCH.md`.

### Traps (any room, any runtime)

- Liveness: `4genteam ps <room> --nodes`. `4genteam capture <session>` can read blank; `tmux capture-pane -p -t <session>` does not lie.
- `[omp-runner] ERROR rpc: RPC response exceeded the transport limit` printed after `READY` is cosmetic on resume — the seat still answers. Ping it before considering a re-prime.
- Route: check the provider window before picking a runtime. The predecessor CLI's `rig usage series --lane provider_window` has no `4genteam` counterpart — for DeepSeek billing the native read is `4genteam offpeak`, and for the other providers the vendor's own dashboard is the source. When Claude weekly is capped (it was 100 percent on 2026-10-05, reset 2026-10-06 12:00Z), the seats belong on omp/DeepSeek.
- Write domain: a repo worktree takes one writer team. A second team stays out of it (skill files, brief files, read-only audits).
- A stopped room loses nothing: seats, sessions and snapshots live under `~/.config/4genthub/rooms/<room>/`, so "down" is a lifecycle state, not data loss. Do not tear a room down to bring it back — `4genteam continue <rig> [--allow-peak]` restores it, briefs the seats, and starts the full supervisor stack (never the predecessor's `rig up <rig> --existing`).

## When to Use

- A team (room) is down and must continue its work — any room, any project: `4genteam continue <room>`
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
