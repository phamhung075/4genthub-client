---
name: queue-row-schema
description: Write a queue row body the daemon accepts - four lines, facts / rule / decision / next, inside the per-field caps. Read before `4genteam room queue create`.
---

# Queue row schema

Every `4genteam room queue create` body is exactly four lines, in this order:

```
facts: <measured state, key=value pairs joined by '; '>
rule: <the policy, owner order or gate check that governs this row>
decision: <what was decided, one sentence>
next: <the one action the destination takes, and who takes it>
```

## Caps

- Summary <= 120 chars; body <= 600 chars (both already enforced).
- Values: facts <= 280, rule <= 110, decision <= 100, next <= 78. Prefixes and newlines cost 32 chars, so
  `32 + 568 = 600`: a body at every cap is exactly the body cap.
- One occurrence of each key, in that order; a value is one line, never empty.
- A create carries at least one allowed tag (prod-break, gate, ruling, owner-decision, finding, guide, docs,
  defect, skill-check, landing, assignment, handoff, board, reseat); `high` requires `prod-break`.
- The body is create-only: `queue update` takes `--note`, `--summary`, `--state`, never `--body`, so the
  rule governs what may be ADDED - rows written earlier keep their bodies verbatim.

## A row is a handoff, not a transcript

A row exists to hand ONE action to ONE seat, or to record ONE decision for the seat that reads it. Progress,
status and the evidence behind a claim go into the board item's Progress (`manage_task` update with
`details`, appending one `=== Progress N ===` per call), which is the surface the item shows and the reader
walks; the rail is not a log.

A row that repeats a status costs the room twice - it is written by one seat and polled by the next. Measured
on this room (2026-10-18): queue traffic is $6.44 of $34.35, 17% of spend - writes $3.75 over 3,351 turns,
polls $2.69 over 2,618 - and one seat wrote 177, 155 and 93 rows on three consecutive days. The fix is not a
smaller row; it is one row per handoff.

## What goes in `facts`

`4genteam facts` field paths verbatim, e.g. `worktree.head=8e706e18; queue.pending=1`. When a needed fact
has no verb, name its source (`commit=`, `ci.run=`, `file=`). Do not re-derive a fact by a bash chain: the
seat that reasons to a fact pays for the reasoning twice.

## Refused at create (enforced in `queue/mod.rs` and `clientruntime/queue.go`)

Missing field, out-of-order or unknown line, duplicate key, empty value, per-field cap, empty tag list - each
refusal names the rule and the field.

## Example

```
tag: gate
summary: GATE PASS task b6a8d202: /docs tests 37 green, tsc 0
facts: sha=8e706e18; base=d902b7e0; tests=37 passed; tsc=0; numstat=12 files +1014 -9
rule: gate D3 test run, D4 keep-out paths, D5 diff size; all green
decision: pass; the commit stands as landed
next: go-dev carries 4/8 on this base; no edit to the gated paths
```

The full ruling and one worked example per tag ship in RULING.md beside this file.
