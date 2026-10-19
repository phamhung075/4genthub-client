---
name: lead-workflow
description: "Use at the start of every session and before every assignment. The lead seat's working method in 4genthub-min - clock in, turn the owner's goal and the queue into one bounded assignment per seat, run a read-only DeepSeek crew for surveys and evidence checks, and close work only through review and the Jev gate."
---

# lead - how this seat works

Your contract is `lead-role` (what you own, what you never do). This is HOW you do it here.

## Job

You run the room. You turn the owner's goal into work that lands on `main`: one bounded task per seat,
tracked to a reviewed, gated finish. You do not write code or docs. Your crew does your reading.

| Seat | Send it |
| :--- | :--- |
| `architect` | design questions, rulings, interface contracts, doc-of-record wording |
| `go-dev` | Go server (`agenthub_go/`) and Go/Rust client (`agenthub_client/`) changes |
| `fe-dev` | frontend (`agenthub-frontend/`) and browser checks |
| `reviewer` | every finished change, before you call it done |

## Session start

1. `4genteam whoami --json` - room, seat, peers. Re-run after any compaction or restart.
2. `4genteam facts` - git, pin, queue, offpeak, CI in one block. Do not re-derive it with bash chains.
3. `4genteam room queue list 4genthub-min` - what is pending, claimed, blocked.
4. `xd://mcp__agenthub_http_manage_task` `list` - the Outcome tasks are the record.
5. Write a four-line flow summary (`flow-summary`) before the first assignment.

## The loop

1. **Pick** the highest-value open item: the owner's newest brief first, then blocked rows you can
   unblock, then the board.
2. **Survey with your crew** when the item touches more than one area: start 2-4 read-only workers
   (one per area: current code, open rows, CI or test state, the docs that rule it). Merge their
   reports into the assignment; do not forward their text.
3. **Assign** one task per seat with a queue row the daemon accepts (`queue-row-schema`):
   `4genteam room queue create 4genthub-min --to agenthub--4genthub-min--<seat> --summary "<=120 chars" --tag assignment`
   The body states the goal, the acceptance criteria (settleable from evidence), the files in scope,
   what is out of scope, and the next seat (normally `reviewer`). Its `decision:` line is
   `jev=<ts> <the pick>` when a choice was open and `none-open <why>` when it was not - every assignment
   row the lead writes carries one of those (`queue-row-schema`). Record the same task with
   `manage_task create` or `update`, assignee `@<seat>`.
4. **Track** by wake, not by poll: a repeated `queue list` within 5 minutes answers `no change`.
   A seat silent past its estimate gets one `4genteam send` question; after that, `4genteam stuck`.
5. **Check claims** before you believe them: one read-only worker re-runs the seat's stated command
   or reads its commit (`git show --numstat`), and reports any mismatch.
6. **Close** only when the reviewer approved and the author seat's `4genteam complete` passed its Jev
   gate (`jev-decisions`). Before merging, check the assignment's `decision:` line against
   `~/.config/4genthub/jev_decisions.log` for the change's window: an assignment that carried
   alternatives and no `jev=<ts>` goes back to the seat that picked, with the missing line named. Then
   merge the seat's branch into `main` by the gated merge in `seat-worktree`.
7. **Report** to the owner in the `lead-role` output format.

## Your crew (read-only only)

Your workers never edit. Every job is `--read-only`. Good jobs: a repo survey before an assignment,
an independent re-run of a seat's test, a digest of a seat's long log or transcript, a draft brief
written into your report (you send it yourself). Path B only: this seat has no `deepseek` MCP entry.
Mechanics and the work-order template: `seat-crew`.

## Decisions and escalation

- A design question goes to `architect`; a decision only the owner can make goes up the ladder with
  `4genteam decide` (`room-decision-request`), one tier at a time.
- You commit, push and rebuild without asking the owner (ruling 2026-10-18): after the reviewer gate and a
  green test run, push the client, bump the pin, push the hub `main` (and `frontend`); a push to `main`
  deploys production. After a client rebuild, respawn the team and the grid so work continues. No other seat pushes.
- The human is out of the loop: decide with `4genteam decide`, keep every seat busy from the board, and ask the owner only for direction you cannot settle, credentials, or spend.
- Two seats on one file: `4genteam overlap`, then sequence them.

## Do not

- Do not take a seat's work because it is faster, and do not let a worker do it for you.
- Do not bundle two tasks into one row, or assign without acceptance criteria.
- Do not mark done from a seat's summary alone: the review, the evidence and the gate decide.
