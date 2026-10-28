---
name: flow-summary
description: State a turn's flow as four lines - facts, rule, decision, next - before acting, with facts read from `4genteam facts` instead of re-derived by bash chains.
signals:
  - command: 4genteam facts
---

# Flow summary

Before a turn that changes something - a commit, a row, a reseat, a fix - state the flow in four lines:

```
facts: <what you measured, one block of 4genteam facts keys and values>
rule: <the policy, owner order or gate check that governs the turn>
decision: <what you decided, one sentence>
next: <the one action you take, and who acts after you>
```

Write it where the work is read: a queue row body (see `queue-row-schema` for its caps), a handoff message,
or the turn's own plan. The four lines a seat states are the four lines a row carries - one shape, two
surfaces.

## Facts come from the verb

Run `4genteam facts` once and read the block; do not spend a bash chain on a computation that has one right
answer. Measured on this room's transcripts (2026-10-17, 20,536 bash calls): the worktree's git state was
rebuilt in 6,600+ commands and the rail in 5,316 - each a turn spent on a fact the verb answers once.

- The block names its own version: check `schema` is `4genteam.facts.v1` before reading keys.
- Keys used most: `repo` (head_short, branch, dirty, ahead_of_base, behind_base), `pins` (state
  same/ahead/behind/diverged), `queue` (mine_pending, mine_in_progress, rows, newest_mine_summary),
  `offpeak` (peak, next), `ci` (head, status, conclusion), `task` (status, criteria, criteria_count).
- A source that could not be read prints `"state":"unavailable"` with a one-line `reason`. That is a missing
  measurement, never a zero: carry the reason into `facts:`, do not reason around it.
- `--task ID` adds the task's acceptance record; `--base REF` moves the compared base (default `main`).
- A secret's presence is a fact with one command: `test -n "$AGENTHUB_TOKEN"` and echo the result, never
  `cat ~/.config/4genthub/.env` - the seat guard refuses that read as `env-secret`, so the turn is spent on a
  refusal and a retry. Measured on this room (2026-10-18): 73 such refusals, 30 of them go-dev's.
- Every figure names its source: the verb key, the row, or the board Progress section that already carries
  it. A number a row or a Progress line states is cited, never measured again - and measuring it again is
  what bash-first turns cost: $8.40 over 8,193 turns on 2026-10-18, 24% of the room's spend.

## The other three lines

- `rule`: name the rule, do not paraphrase it away - an owner order, a gate's checks, or a repo rule, so the
  next reader can check it.
- `decision`: one sentence; a finding that needs no decision says `none - <why>`.
- `next`: the single action, with its actor. Two actions mean two rows.

## Example

```
facts: repo.head_short=8e706e18; repo.behind_base=0; queue.mine_pending=1; pins.state=same; offpeak.peak=false
rule: a row addressed to this seat is the work; only the lead pushes (the director and founder, in a room with `<roomDir>/director/`)
decision: claim the rail row and write the enforcement tests before the parse
next: go-dev writes the red tests, then the parse; the lead closes when green
```
