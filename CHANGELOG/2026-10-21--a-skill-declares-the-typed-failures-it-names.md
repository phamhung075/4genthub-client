# A skill declares the typed failures it names, and the census judges it on its own seat

`RULING-2026-10-21-skill-signals.md` (architect). The evidence loop was unarmed rather than broken: 38 skills,
38 `no-signal`, nothing declared. This lands the declarations and the three rules that make them honest.

## The declarations

28 skills now carry a `signals:` block in their front matter, inside the front matter and before the closing
`---` so `metadata:` is untouched: the guard's own rule names (folded `_`→`-`), the store's own row tags, or a
literal substring of a tool target. Each rule has one owner in its scope - `worktree-scope`, `push` and
`git-imports-a-tree` on `seat-worktree` and on no seat skill, `docs-only` on `changelog-updater`,
`queue-coalesce` on `queue-row-schema`, each seat's `env-secret` on exactly one of its skills - so one refusal
demotes one skill. The 9 skills that name no typed failure stay `no-signal` and are listed with the rule or verb
that would arm each.

`skills/share/deepseek-offload` is **not** declared: it is a symlink into the `third_party/deepseek-offload`
submodule, so its front matter is that repository's file and not this one's.

## R1 - a seat-tier skill is judged on its own seat's facts

A skill delivered at `seat:<seat>` states a rule for that seat; another seat's refusal is not a failure of it.
`Fact` gains `Seat`, filled from the spool line's own `seat` field, the blocked tool line's `seat`, and the
tool log's directory; `RailRow` gains `Destination` and `SeatOf()`, which `railRows` fills from
`DestinationSession`; `seatFromSession` reads both spellings the store holds (`agenthub--<room>--<seat>` and
`<seat>@<room>`). `Occurrences`, `StreamSize`, `Commands` and `rowOccurrences` take the row's level and skip
another seat's facts - numerator and denominator both, so `tooBroad` stays a ratio of one scope. A fact whose
seat cannot be named (the machine-wide Jev log, a row handed to a session naming no seat) counts for every
scope: unknown attribution is not another seat's.

Measured on the room's own facts: `go-dev-role` 39, `fe-dev-role` 6, `lead-role` 24, `architect-workflow` 16 -
its own seat's refusals, not the room's.

## R2 - only an attributed signal can make a skill `no-signal`-free

`command:` counts use, never failure. A skill whose every declared signal is a command has no failure stream,
so it reports `no-signal` - the verdict that already means nobody can hold it to account - with its command
counts printed. `hold` is now reserved for a skill that has a stream and no occurrence inside the window.

## R3 - the refusal spool is read from every worktree of the room

A refused commit happens in the seat's own worktree, so the spool lives in that worktree's git dir.
`roomWorktreeDirs` lists `<state root>/rooms/<room>/wt/*` and one directory below each (the client is a
submodule, so a seat's worktree answers a git dir second), and `LoadEvidence` unions them with the repo it is
handed. The room's 10 `docs_only` refusals all live in derived worktrees; the caller's own checkout has none,
so before this the census read one line of ten. Now `changelog-updater` holds on all ten, each cited.

## A hold cites its occurrences

`applyWindow` filled `Refs` only for `demote`, so a skill holding on 302 occurrences showed a number with no id
to follow. A row with occurrences cites them whether it demotes or holds, deduped and capped at 20 - the count
stays in `occurrences`, so a truncated list is still a truthful one, and a citation is never synthetic.

## The four text edits

`lead-workflow` §6 Close reads the census before the merge and names what a `demote` and a `broken-record` do;
`system-improvement-analysis` takes the census line for the skills a cluster touches as an input;
`friction-triage` names the shipped feedback channel and rows tagged `finding` instead of a `friction` tag the
daemon's closed tag list does not contain; `seat-worktree` says importing a tree is the same move as rebasing.

## Verification

`go test ./...` in the client: green. `4genteam skills evidence 4genthub-min --json` on this tree: 37 skills,
**11 `hold`, 26 `no-signal`, 0 `broken-record`, 0 `demote`**, every hold carrying refs; `changelog-updater`
10 occurrences, `seat-worktree` 302, `go-dev-role` 39, `queue-row-schema` 54.
