---
name: queue-row-schema
description: Write a queue row body the daemon accepts - four lines, facts / rule / decision / next, inside the per-field caps. Read before `4genteam room queue create`.
signals:
  - guard: queue-coalesce
  - command: 4genteam room queue
---

# Queue row schema

## The verb form: ROOM first, and it defaults

Every sub-verb takes the room as its FIRST positional argument and the qitem id second - and ROOM is the
ONE positional that defaults: leave it out and the verb takes `$AGENTHUB_ROOM`, the room the runtime launched
this seat into. `room queue show <qitem>` therefore reads this seat's own rail, and naming a room still wins
over the variable.

```
4genteam room queue list   [ROOM] [--destination S] [--source S] [--state S[,S]] [--limit N]
4genteam room queue show   [ROOM] QITEM_ID
4genteam room queue create [ROOM] --to SESSION --summary TEXT --body TEXT --tag T [--priority low|normal|high] [--reply-to QITEM_ID]
4genteam room queue update [ROOM] QITEM_ID [--state S] [--note TEXT] [--summary TEXT]
4genteam room queue claim  [ROOM] QITEM_ID
4genteam room queue close  [ROOM] QITEM_ID --reason R [--target SESSION] [--note TEXT]
```

- **The shortfall rule**: a sub-verb exactly ONE positional short of its own list means ROOM was left out,
  and it is filled from `$AGENTHUB_ROOM` (`4genthub-min` on this machine). The COUNT decides, never the
  spelling of a word: `queue show <qitem>` and `queue show <room> <qitem>` are told apart by how many
  positionals arrived, so an id typed where the room belonged is never read as a room.
- **The two forms**: `queue show <qitem>` is this seat's room; `queue show <room> <qitem>` is a named room.
  A count short by more than one is a missing TAIL, not a missing room, and is refused naming the id:
  `queue close <room>` answers `a QITEM_ID is required`.
- **With `$AGENTHUB_ROOM` unset**, the short form is refused BEFORE anything is asked of the daemon, naming
  the variable: `ROOM was left out and $AGENTHUB_ROOM is unset, so there is no room to default to; name the
  room, or run this from a seat the runtime launched` (exit 3). Asking the daemon about a room named after
  the id would be a wrong answer, so the verb does not try.
- Measured on this room (24h to 2026-10-18): 49 `the ROOM comes first` refusals - lead 19, go-dev 13,
  architect 13, fe-dev 2, reviewer 2; the commit that added the default counts 147 failed calls of that
  shape. The order is now stated in a skill; the shortfall is fixed by the default.
- A create's `--summary`, `--body` and `--tag` are required in practice even though `--help` brackets
  them: without a tag the daemon answers `a create carries at least one allowed tag`, and a `--body` that
  is not the four lines below is refused field by field.
- `close --reason` is an ENUM, never prose: `handed_off_to` (requires `--target SESSION`), `blocked_on`,
  `denied`, `canceled`, `no-follow-on`, `escalation`, `superseded`, `gate` (the reviewer's verdict close:
  `--note` must carry PASS/FAIL and the commands run, and the client refuses one without it). Any other
  close's sentence goes in `--note`.
- There is no global flag and none before the sub-verb: `--state-root DIR` and `--socket PATH` follow the
  positionals of the verb you called, and each verb has its own flag set.

Every `4genteam room queue create` body is exactly four lines, in this order:

```
facts: <measured state, key=value pairs joined by '; '>
rule: <the policy, owner order or gate check that governs this row>
decision: <jev=<ISO-8601 ts> <the pick> | none-open <why nothing was open> | what was decided>
next: <the one action the destination takes, and who takes it>
```

The `decision:` line of an ASSIGNMENT names the gate that settled the choice:

- `decision: jev=<ISO-8601 ts> <the pick>` when a choice was open - the timestamp `4genteam decide`
  printed, with the pick it settled (`jev-decisions`);
- `decision: none-open <why nothing was open>` when it was not - the task named the mechanism itself or
  nothing was left to choose.

A row of any other tag keeps the one-sentence `decision: <what was decided>` form.

## A row that presents alternatives needs its `jev=` line

An assignment that presents alternatives and carries no `jev=<ts>` is a gate FAIL: the reviewer's
`decision check` answers `decision-check: no jev=<subject>` and does not pass. Measured on this room
(2026-10-19): `~/.config/4genthub/jev_decisions.log` held 148 decision lines - seat-tier 58, room 55,
lead 35 - and none on 2026-10-19, while the assignments carried criteria that already named the
mechanism; two open choices were then settled silently, by the seat that picked (the agy bridge's
credential form, the module viewer's render form). The line ties each pick to one timestamped verdict,
so a reader can audit it instead of trusting the row.

## Caps

- Summary <= 120 chars; body <= 600 chars (both already enforced).
- Values: facts <= 280, rule <= 110, decision <= 100, next <= 78. Prefixes and newlines cost 32 chars, so
  `32 + 568 = 600`: a body at every cap is exactly the body cap.
- `room queue create --help` states only the 600-char body cap; the per-field caps above are the ones the
  daemon enforces and names when it refuses, so a row is written against these four numbers.
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

## A cited file is one the reader can open

A row that names a file hands the destination a path it can open. `local://<name>` does not: the namespace
resolves inside the WRITING session's own directory (`rooms/<room>/seats/<seat>/sessions/<ts>/local/`), so
the citation is a link only its author holds. Measured on this room: a row cited
`local://phone-recheck-2026-10-18.md`, the reading seat's answer was `Local file not found`, and locating the
file by absolute path took a find over the home tree, 51s (feedback 6f3032e9, 2026-10-18; the same shape on
2026-10-11, b37002ca). Cite a room-shared path or an absolute path under `seats/<seat>/`.

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
