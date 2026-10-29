---
name: seat-crew
description: "Use whenever a task has two or more independent parts, a repo-wide read, a long log or a bulk mechanical change. Every seat leads its own crew of DeepSeek workers through deepseek-offload: split the work, start one worker per part in parallel, verify each report, and own the result."
signals:
  - command: deepseek_agent
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

## The rule the rail enforces (2026-10-18)

- **The queue verbs default the room.** Every `4genteam room queue` sub-verb takes `[ROOM]` as its first
  positional and fills it from `$AGENTHUB_ROOM` when it is left out, so a seat's own rail needs no room:

  ```sh
  4genteam room queue show <qitem>
  4genteam room queue claim <qitem>
  4genteam room queue close <qitem> --reason handed_off_to --target reviewer --note "..."
  ```

  Name a room only when it is not your own. Measured 2026-10-18: 147 calls failed by typing the id where
  the room belonged, and the verb then answered `the ROOM comes first`.

## The Skill-Evidence trailer (2026-10-22)

A commit whose path set carries a skill file - the client commit spells it `skills/**`; the hub's own spelling
is `agenthub_client/skills/**` - carries a `Skill-Evidence:` trailer, and its value is one of three shapes:

```
Skill-Evidence: qitem-<14 digits>-<12 hex>                                # the row that ordered the edit
Skill-Evidence: ... 4genteam skills evidence <args> ... <what it said>    # the census run you made
Skill-Evidence: none - <reason>                                           # an edit no stream ordered
```

One key is enough; repeating it with the SAME value is accepted, two DIFFERENT values are refused. The
`none - <reason>` reason must be 8 characters or more (`none - typo` is refused), and the census command must be
followed by 8 or more further characters, so the command alone does not pass. A message that starts `Merge ` and
a commit made on a host that is not a seat are exempt; the lead is NOT exempt.

The check is the commit wrapper `scripts/git-hooks/git_commit_capture.py` - the one place that sees the PATH SET
and the MESSAGE together - and it runs in whichever repo you commit IN: in the hub by that path, in the client by
the same file's absolute path (`$HOME/__projects__/4genthub/scripts/git-hooks/git_commit_capture.py`),
because clients keep no copy. A hook cannot host this check: a `commit-msg` hook is blind to a pathspec commit's
path set (the ruling measured it), so a plain `git commit -- <paths>` meets no check at all - commit through the
wrapper. The skills arm is NOT on main yet: it lands with the parked patch
(`seats/go-dev/PATCH-2026-10-22-skill-evidence-trailer.{diff,md}`, at the owner's apply), so today the wrapper
refuses a docs-only commit and does not yet refuse a missing `Skill-Evidence:` - until it does, the trailer is
the seat's word, not a refusal. The guard judges SHAPE ONLY: no daemon, no census re-run, no verdict. A skill path is not a document - a `SKILL.md`
is executable instruction - so a skills-only commit is judged by this trailer instead of being refused as
docs-only. The value names what the edit came from, never a coverage claim:

- the **row** that ordered it, when a row did;
- the **census run** and what it printed -
  `4genteam skills evidence <room> --skills-root agenthub_client/skills` (add `--json` for a machine read),
  with the row it moved;
- `none - <reason>` for an owner ruling, a new skill, a rename or a typo.

A ref you cannot produce is `none - <reason>`, never a guessed id: `4genteam skills propose --from <ref>`
refuses a ref it cannot resolve, and the reviewer reads the value.

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
   is the source of truth over tests, no `.env` reads, no commits or pushes.
4. **Method** - the commands you expect it to run (tests, greps, builds).
5. **Output contract** - at most 300 words: findings as `file:line - fact`, the commands it ran with
   their exit codes, and an `Unverified:` line.
6. Last line: "Report only what you actually ran and observed."

## Crew size and cost

- At most **4 workers at once** per seat; one worker per independent part, never one per question.
- Run `4genteam offpeak` first. At peak, start only work that cannot wait, and add
  `--defer-to-off-peak` to batch work.
- A failed worker is retried once with a sharper order; after that, do the part yourself or report it.

## Blocked: take a backup item, then declare the wait

When your item cannot move (a reviewer verdict, a director merge, an answer to a QUESTION, a build
slot, the peak window), first take a backup item: another rail row for you, then a chore (below).
Only when there is none, declare what you wait on and end your turn:

```sh
4genteam wait --on merge:<branch>@<sha> [--max 60m] [--note "<why>"]
```

Kinds: `review:<qitem|branch@sha>`, `merge:<branch@sha>`, `answer:<qitem|QUESTION ref>`,
`slot:cargo` (add `--slot-dir <dir of slot* lock files>`, e.g. `/tmp/aa2-build-slots`, so a free
slot is seen), `peak:offpeak`, `seat:<seat>`. Room and seat come from `AGENTHUB_ROOM`/`AGENTHUB_SEAT`.
The verb returns at once; never sleep or poll after it. While it holds you are not nudged as idle.
It ends at the first of: a message to you, a new rail row for you, the event itself (row closed,
commit in main, slot free, off-peak), or `--max` (default 60m, cap 4h); the last two wake you with
one line - then take a backup item or ask. A wait with no target, or while a pending row other than
its target is yours, is refused. `4genteam wait --show` / `--clear` read or end it.

## No work for your seat: claim one chore

Idle is a fact you read, not a feeling: no pending or in-progress rail row addressed to you, no board
item assigned to you in progress, and `4genteam room status` showing your seat at `quiet_seconds >= 900`.
An idle seat takes ONE chore - a board item labelled `chore` - and works it for at most 30 minutes. The
owner approved this standing list; no seat asks permission first.

- **Claim** with `manage_task` `update`: `assignee=@<seat>`, `status=in_progress`, `details` =
  `claim <4genteam clock --short>`. The assignee IS the lock - re-read the item before starting, and if it already names
  another seat, take a different chore.
- **Never create a chore and never start code under one.** The lead seeds and replenishes, at most six
  live. A chore that needs a fix or a decision returns to the lead as a `finding` rail row.
- **Release**: `assignee` cleared, `status=todo`, and a Details line naming why (blocked, needs a
  decision, past 30 minutes).
- **Close** only with the output pasted into Details - the list and its counts; a chore with no diff
  closes by the board route, and the close says so.
- The rail is not the claim surface: a row is ONE action to ONE destination, and a chore has no
  destination until it is claimed.

The catalogue the lead seeds, and what "done" produces:

| Chore | The output |
| :--- | :--- |
| skills install drift | per seat, the dirs `seats/<seat>/skills` lacks against the client tree |
| rail hygiene | `handed-off` rows whose successor is missing or itself handed off |
| board hygiene | items `in_progress` with no Progress update in 2h, and items with no acceptance criteria |
| report triage prep | today's feedback rows that no board item names, grouped by `layer` |
| doc citation rot | `ai_docs/` and skill citations whose path no longer exists |
| obsolete markers | each `*.obsolete`: is a live doc referencing it, is its content superseded in git |

Live state is the board, not this file: the catalogue is permanent, the instances change every day.

## Owning the result

- **A worker's report is a claim.** Re-run the check that matters (the test, the grep, the build)
  before you repeat a finding or hand a change on. Read every write worker's `git diff` yourself.
- **Merge, do not paste.** Fold the reports into your seat's own output format; never forward raw
  worker text to a peer or the owner.
- **Record it.** One `=== Progress N ===` section on the task through `manage_task update` names the
  labels, the job ids and what each settled; say which conclusions came from workers.
- Your role's limits apply to your workers. A seat that may not edit code starts read-only workers only.
