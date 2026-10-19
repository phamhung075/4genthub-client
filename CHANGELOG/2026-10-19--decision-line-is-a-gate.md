# The decision line is a gate

Date: 2026-10-19
Task: c0c53a90 (`OWNER DECISION: seat decision-ladder enforcement`), owner ruling qitem-20261019155703-f89f341c6504

## What was wrong

`4genteam decide` had no enforcement point. The `complete` gate refuses without evidence, but nothing
read the decision log before a change landed, so seat-tier decisions were settled silently. Measured on
2026-10-19: `~/.config/4genthub/jev_decisions.log` held 148 decision lines - seat 58, room 55, lead 35 -
none on that day, and the last seat-tier line on 2026-10-18T18:18Z. Assignment rows carried criteria that
already named the mechanism, and two open choices were picked by the seat itself (the agy bridge's
credential form, the module viewer's render form). The reviewer role text never named `decide`, so the
gating seat could not flag it.

## What changed

Five seat skills, text only:

- `skills/share/queue-row-schema/SKILL.md` - the four-line schema gives an assignment's `decision:` line
  two forms, `jev=<ISO-8601 ts> <the pick>` or `none-open <why nothing was open>`, and a row that
  presents alternatives with no `jev=` is named a gate FAIL, with the measured baseline above as the
  reason.
- `skills/share/jev-decisions/SKILL.md` - the three triggers in one list: two or more defensible
  mechanisms/interfaces/shapes and the task names none; behaviour outside the acceptance criteria;
  irreversible or owner-only. A seat never puts options in front of itself without a `decide` call, and
  the checklist asks for the timestamp on the row or the report.
- `skills/rooms/4genthub-min/reviewer/reviewer-workflow/SKILL.md` - a `decision check` step before the
  verdict: the handoff's decision line and the task Progress against `jev_decisions.log` for the window,
  a missing line is a blocker `decision-check: no jev=<subject>`, an owner-only or irreversible pick
  points at its owner-decision row id.
- `skills/rooms/4genthub-min/reviewer/reviewer-role/SKILL.md` - the same rule and the output line
  `Decision check: <jev=<ts> | none-open | MISSING>`; provenance raised to seat-type-version 1.4.19.
- `skills/rooms/4genthub-min/lead/lead-workflow/SKILL.md` - every assignment row the lead writes carries
  the decision line, and the close step checks it against the log before merging, returning an assignment
  that carried alternatives with no `jev=` to the seat that picked.

No dispatch code changes: the queue daemon, `4genteam decide` and `4genteam complete` are untouched
(that would be the rejected Option B). Test change: the runtime's seat-role resolver test
(`rust/agenthub-runtime/src/skills/mod.rs`) pins the version each role's text was ported at, so its case
table now carries 1.4.19 for the reviewer and 1.4.12 for the other four.

## The hub side

`agenthub_go/fastmcp/seat_management/domain/seedlibrary/seat-types/reviewer.yaml` carries the same rule
and output line, `seedVersion` rises to 1.4.19 in `domain/seedmap/seedmap.go`, and
`domain/seedlibrary/seedcontent.lock.json` records the new content set.
