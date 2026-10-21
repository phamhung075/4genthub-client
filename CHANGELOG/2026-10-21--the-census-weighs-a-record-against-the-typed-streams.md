# The census weighs a record against the streams, so a skill can be promoted or demoted

S1 gave `4genteam skills evidence` a verdict per skill from the tree alone: `hold`, `no-signal`,
`broken-record`. Those three say what a record claims, never whether the claim survived contact.

This adds the windows. Each skill's record is dated - its `since`, or the first commit that added its
`SKILL.md` when it has no record - and four typed streams are read from that date forward:

- the guard's refusal spool (`<git dir>/parked-diffs/refusals.jsonl`), keyed by rule;
- each seat's comm-policy audit, keeping only `Allowed:false`, keyed by `Reason`;
- the machine-wide `jev_decisions.log`, keyed by `kind`;
- the room's rail rows, keyed by tag (and used to count landings).

The two verdicts are `demote` (a `trusted` skill whose declared signal occurred inside its window) and
`promotable` (a `provisional` skill with `PromotionWindow` = 3 landings inside its window and no
occurrence). `broken-record` still wins over both. A stream that could not be read is named in the
report's notes, because an unread stream is not silence: counting it as silence would promote a skill
nobody watched.

**TOOL lines are counted, never judged.** A `command:` signal matches tool targets and appears as
`cmd:` in the report; an errored tool call cannot demote a skill. The acceptance names this case, and it
is the difference between a loop that learns from typed refusals and one that learns from noise.

`clientactivity` reads the same three files for the human timeline; the census keeps its own reader
because it needs the raw field a signal names (a send `Reason`, a Jev `kind`, a tool `target`), which
the timeline flattens into one rendered line.

Verification: `go test ./...` in `agenthub_client` - every package ok. The S2 fixtures drive one `demote`
(a `guard: docs-only` refusal after the record's `since`) and one `promotable` (a `row-tag: defect` row
plus three landings, with two older landings excluded by the window). Live:
`4genteam skills evidence 4genthub-min --skills-root skills` prints 37 skills, each with a dated window.
No skill carries a `signals:` block yet, so the live room is `no-signal` throughout - which is the census
reporting an unarmed loop, not a failing one.
