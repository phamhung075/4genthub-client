---
name: seat-crew
description: "Use whenever a task has two or more independent parts, a repo-wide read, a long log or a bulk mechanical change. Every seat leads its own crew of DeepSeek workers through deepseek-offload: split the work, start one worker per part in parallel, verify each report, and own the result."
---

# Seat crew - you lead your own workers

Every seat is the leader of its own small team. The room's seats are the departments; the workers
under you are DeepSeek Harness sessions started through the `deepseek-offload` skill. You keep the
judgment, the edits that need your context, the verification and the record; workers do the reading,
scanning, drafting and parallel legwork. Your seat's `<seat>-workflow` says what YOUR workers may do.

## When to start workers

| Start workers | Keep it yourself |
| :--- | :--- |
| Two or more parts that do not touch the same files | One small edit you already understand |
| "Find every place X happens", a sweep of many files | Anything needing this conversation or the owner's words |
| A long log, test output or transcript to digest | A decision your role owns (ruling, verdict, assignment) |
| A mechanical change checkable by a build or a test | Commits, merges, pushes, queue rows, task records |
| Checking a peer's claim independently | Secrets, `.env`, credentials, production |

Rule of thumb: if the work makes more intermediate text than final text, a worker does it.

## How to start them

**The tool paths.** Path A, `xd://mcp__deepseek_agent` with `{"prompt": "...", "cwd": "<worktree>"}`,
blocks until the worker finishes: use it for one short answer. Path B runs workers in the background
and is the one for a crew. A seat with no `deepseek` MCP entry (the lead today) has path B only.

```sh
OFF="$(dirname "$AGENTHUB_CLIENT_SKILLS")/third_party/deepseek-offload/.agents/skills/deepseek-offload/scripts/dsh-offload.mjs"
export DSH_OFFLOAD_JOB_DIR="$PI_CODING_AGENT_DIR/offload"   # your seat dir, never a repo's scratch/
WT="${AGENTHUB_WORKTREES%%:*}"                               # your worktree: the first entry is where your pane started

node "$OFF" start --prompt-file "$PI_CODING_AGENT_DIR/offload/orders/<label>.md" --cwd "$WT" --label <label> --read-only --detach
node "$OFF" list                      # your jobs
node "$OFF" result <jobId>            # exit 0 done, 1 error, 2 still running
node "$OFF" update <jobId> "<redirect>"  # steer a running worker
node "$OFF" cancel <jobId>
```

Each shell call is a fresh shell: set `OFF` and `DSH_OFFLOAD_JOB_DIR` in the same call that uses them.
Write every work order to a file and pass `--prompt-file`; a quoted prompt with backticks is executed
by bash before the worker sees it. `--read-only` for every job whose deliverable is a finding; omit it
only for a write job in your own worktree. Never pass `--allow-git-write`: you commit, workers do not.

**Waiting.** Do not poll in a loop. Start the crew, do your own part, then collect each `result`.
When nothing else is left, run one background `wait` for all of them:

```sh
for j in <id1> <id2> <id3>; do node "$OFF" wait $j --timeout-ms 1800000 2>&1 | sed -n '/--- result ---/,$p' | cut -c1-2000 | head -40; done
```

## The work order (every worker, every time)

A worker is a fresh agent that knows nothing of this room. The file holds:

1. **Objective** - one imperative sentence.
2. **Scope** - the exact paths it may read and, for a write job, the only paths it may change.
   Two write workers never share a file.
3. **Rules it inherits** - clean code only (no compatibility, fallback or legacy code), the entity/ORM
   is the source of truth over tests, no `.env` reads, no commits or pushes, no CHANGELOG edits.
4. **Method** - the commands you expect it to run (tests, greps, builds).
5. **Output contract** - at most 300 words: findings as `file:line - fact`, the commands it ran with
   their exit codes, and an `Unverified:` line.
6. Last line: "Report only what you actually ran and observed."

## Crew size and cost

- At most **4 workers at once** per seat; one worker per independent part, never one per question.
- Run `4genteam offpeak` first. At peak, start only work that cannot wait, and add
  `--defer-to-off-peak` to batch work.
- A failed worker is retried once with a sharper order; after that, do the part yourself or report it.

## Owning the result

- **A worker's report is a claim.** Re-run the check that matters (the test, the grep, the build)
  before you repeat a finding or hand a change on. Read every write worker's `git diff` yourself.
- **Merge, do not paste.** Fold the reports into your seat's own output format; never forward raw
  worker text to a peer or the owner.
- **Record it.** One `=== Progress N ===` section on the task through `manage_task update` names the
  labels, the job ids and what each settled; say which conclusions came from workers.
- Your role's limits apply to your workers. A seat that may not edit code starts read-only workers only.
