# `doctor` shows the skill census, and fails on a demote or a broken record

The census was only readable through `4genteam skills evidence`: a seat that never runs the verb learned
nothing from it, and a report nobody fails on is not a check. This wires it into `doctor` as a `skills` row
beside `worktrees` and `clone`, one row per room.

The row reads the census and repairs nothing - the same rule as the worktrees row. A demote is fixed by a
`skills propose` and a gate; a broken record by the commit that names the right evidence. A doctor row that
"fixed" either would be a second writer for the skill tree.

Its two failing states are the census's own verdicts, not a new vocabulary:

- **`skills_demote`** - a `trusted` skill a declared signal contradicted inside its window. The reason names
  the skill, the signal that fired, how many times, and the rows that fired it (`refs`), so a reader opens
  the failures instead of trusting the count.
- **`skills_broken`** - a record that cannot be read, or one citing an evidence id that does not resolve. The
  reason names the skill and the id (`qitem-… does not resolve`).

A demote wins the state when a room has both, and the broken records stay in the reasons beside it.
`promotable` is a **detail**, not a failure: a skill ready for its gate is not a defect in the room. Every
other skill prints one line with its verdict and counts (`hold`, `no-signal`, `promotable`), and a `no-signal`
skill is printed rather than left out - that is the finding a reader arms it from. A stream the census could
not read rides beside the row, because a census that read nothing must not print as a census that found
nothing. A room whose tree cannot be resolved (the `AGENTHUB_CLIENT_SKILLS` the runtime installs from is
unset) reports the input it could not read instead of a verdict.

Two smaller changes. `clientskills.CensusRoom` is now the ONE composition of the census - tree, rail rows,
typed streams, evidence resolution - and `skills evidence` calls it too, so the verb and the row cannot
report different numbers. And `rowsFor`'s JSON projection gained the new row's reasons, so `doctor --json`
answers the same thing the lines do.

Verification: `go test ./...` in `agenthub_client` - every package ok, including five cases for the row
(demote names the refs, broken record names the id, promotable stays `running` and is a detail only, a missing
tree is a note, a census note rides beside the row). Live in this seat's worktree: `doctor` printed
`4genthub-min skills: running, age unknown` with 37 per-skill lines (`share/agent-browser no-signal (signals
0), since e705bd56…`); pointed at a throwaway tree whose one trusted record cited an unknown row, the same
command printed `4genthub-min skills: skills_broken`, the reason
`skills: 4genthub-min share/broken broken-record: qitem-20261021999999-deadbeef0000 does not resolve` on stdout
and stderr, `"reasons"` in `--json`, and exited 3. The throwaway tree was moved out; the worktree is clean.
