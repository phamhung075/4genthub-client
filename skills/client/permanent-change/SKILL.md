---
name: permanent-change
description: Make a change to how the team works permanent, not only true in the running sessions. Use whenever the owner or a seat wants to change a team behaviour, rule, workflow or habit ("make seats do X", "stop doing Y", "from now on", "fix the team strategy"), and before telling any seat to do something differently.
---

# Make a team change permanent

**A queue row, a `guide`, a `send` or a chat reply changes a running session and nothing else.** The
seat forgets it at the next compaction, restart or reseat, and a new seat never sees it. A change has
value for the client only when it is committed in the versioned source that the runtime installs at
every seat start. Telling a seat is at most a way to get the change adopted today while the commit lands.

## Where each kind of change lives

| The change is about | Put it in | Reaches a seat |
|---|---|---|
| Behaviour every seat shares (how to read the queue, report, batch, claim chores) | `agenthub_client/skills/share/seat-crew/SKILL.md` | next seat start |
| One seat's workflow (reviewer gate, lead sequencing, go-dev commits) | `agenthub_client/skills/rooms/<room>/<seat>/<seat>-workflow/SKILL.md`; the runtime also writes it to `<seat dir>/AGENTS.md` | next seat start |
| A seat's role or tool scope | `agenthub_client/skills/rooms/<room>/<seat>/<seat>-role/SKILL.md` | next seat start |
| A rule for every agent in the repo (change record, commit form, no-push) | `AGENTS.md` and `ai_docs/agent-system/repo-agent-rules.md` | next seat start |
| A wrong tool behaviour (a verb needs an argument it could default) | client Go or Rust code, with a test | next rebuild + daemon restart |
| A recurring check | a skill under `skills/client/` with a script | the principal session |

A rule with a mechanical test goes in code or a guard (`internal/repoguards`), not prose: prose is
read, a guard is enforced.

## Steps

1. **Name the metric first.** Run `team-benchmark` and write down the number the change targets, the
   seat, and the time. Without it nothing can later prove the change worked.
2. **Pick the row of the table above** and send the owning seat an `assignment` whose definition of
   done is "committed in that file". Never a `guide` alone.
3. **Commit by pathspec with a test or a guard** where the change is checkable; the commit body is the record (what was wrong, what changed, how it was tested). No changelog file (owner ruling 2026-10-21). A commit that changes `agenthub_client/skills/**` carries a `Skill-Evidence:` trailer citing `4genteam skills evidence` or `skills propose` output (owner ruling 2026-10-22).
4. **Deliver:** push the client, bump the hub pin, push the hub (the lead pushes after the gate; client first),
   rebuild with `./scripts/build.sh --no-test` when code changed, then `4genteam room reseat <room> <seat>` for each affected seat.
5. **Prove delivery:** read the installed copy under `~/.config/4genthub/rooms/<room>/seats/<seat>/skills`
   (or its `AGENTS.md`) and confirm the new text is there. A change not found there did not ship.
6. **Prove effect:** after at least 24 hours with no other change to that seat's workflow, rerun
   `team-benchmark` and compare. Keep the change only if the metric moved; otherwise revert by a commit.

## Never

- Treat "I told the seat" as done.
- Change two behaviours of one seat between benchmark runs.
- Write a rule only in `~/.claude` memory or in a transcript: that is the principal's memory, not the team's.
- Put the change in a worktree under `wt/` that is not merged to `main`.

## Checklist

- [ ] The target metric and its before-value were recorded.
- [ ] The change is in a committed file named in the table, with a test or guard where checkable.
- [ ] Client, pin and hub were pushed by the lead, in that order.
- [ ] The installed seat copy shows the new text after reseat.
- [ ] A later benchmark shows the metric moved, or the change is reverted.
