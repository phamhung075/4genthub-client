# A proposal is written into the caller's worktree, and nowhere else

`4genteam skills evidence` says what a skill's record claims and whether it survived. This is the other
half of the loop: `4genteam skills propose ROOM --skill S --change fix|derived|captured --from
QITEM[,QITEM] [--draft FILE]` turns cited evidence into a change a gate can read - the skill's
`record.json` naming the change and its evidence, and `--draft`'s bytes as the new `SKILL.md`, verbatim.

**It never commits, and it never writes outside the caller's own worktree.** The caller is
`$AGENTHUB_SEAT`, or the seat `$AGENTHUB_SESSION_NAME` names (`agenthub--<room>--<seat>`, and the room in
that name is the guard); the directory is the seat's derived worktree, resolved through
`clientservices.DerivedWorktrees` - the same one rule `up` and the worktree verbs read. The resolved skill
must lie inside it, after symlinks are resolved, so a proposal aimed at the shared checkout through a
link is refused with both paths. That is the design note's rule that landing stays a commit somebody is
accountable for: the proposal is uncommitted by construction.

Three refusals, each naming what it refused:

- **an evidence id that does not resolve** - the rail answers per id, so a row the daemon does not have and
  a rail that cannot be reached read differently; one bad id refuses the whole proposal, and nothing is
  written;
- **a skill that declares no signal the cited rows carry** - a queue row's typed facts are its tags, so the
  only kind that can match is `row-tag:`; a skill declaring stream kinds alone has nothing a row carries,
  and the refusal lists the declared signals;
- **a proposal that changes no line** - the record is rendered before anything is written and compared with
  what is on disk, so running the same proposal twice refuses instead of rewriting a file with its own
  bytes.

Two smaller decisions. The record a proposal writes **carries the state it already had** (absent means
provisional): a proposal says what changed and what cites it, while resetting a trusted skill is a decision
for the gate that lands it. And `since` becomes the tree's `HEAD`, because a proposal windows from where it
starts; a tree outside git keeps the `since` it had and the report says so.

A name the tree delivers at two levels is refused with both candidates listed, unless `--skill` carries the
level as `LEVEL/NAME` - two levels are two files, and a proposal must say which one it edits.

Verification: `go test ./...` in `agenthub_client` - every package ok. Live, in this seat's worktree over a
throwaway tree (since no skill carries a `signals:` block yet): the happy path wrote `record.json` (`fix`,
`since` = HEAD, the cited row) and the draft into `SKILL.md`, `git log` unchanged, nothing staged; the same
proposal again refused `changes no line`; an unknown `qitem-…` refused naming the daemon's answer; a
`--skills-root` in the shared checkout refused `outside your own worktree`; no seat named refused. The
throwaway tree was moved out and the worktree is clean.
