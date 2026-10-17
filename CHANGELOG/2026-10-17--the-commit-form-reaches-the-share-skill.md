# The commit form the shared seat skill teaches is the pathspec one

The owner ruled the commit form pathspec fleet-wide (2026-10-17 02:32Z, row `qitem-20261017023257`): commit
with an explicit pathspec and do not stage first. The hub's rendered guide learned it in `b76a85a2`, but what
seats READ from the client still taught staging: the architect's finding
`qitem-20261017023642-bcc031d63db5` names the two spots. This change fixes the one a seat may edit.

- `skills/share/seat-worktree/SKILL.md`: the ruled section's "Staging" bullet becomes "The commit form" -
  `git commit -m "..." -- <paths>`, a new file's `git add -N -- <new>` first, then the same pathspec commit;
  `git add -A`, `git add .` and `commit -a` stay forbidden. The bullet names its own authority (the owner's
  ruling) because the section header cites `ai_docs/core-architecture/seat-worktrees.md`, whose staging
  section this ruling supersedes. The document's own earlier bullet already stated the pathspec form, so the
  skill no longer contradicts itself.
- The guard asset keeps its stale advice: `rust/agenthub-runtime/assets/seat-guard.ts:135` answers a blocked
  `git add -A` with `stage paths: git add -- <paths>`. The seat guard refuses a seat's write to that file
  (`protected-paths`: the guard is the owner's to change), so this row carries the one-line replacement to
  the lead rather than changing it: `stage paths: git add -- <paths>` -> `commit with a pathspec: git commit -- <paths>`.

### Testing
- Delivery measured, not inferred: `seats/go-dev/skills/seat-worktree/SKILL.md` (installed at this seat's
  start) was byte-identical to `skills/share/seat-worktree/SKILL.md` at `main` and now differs from the edited
  source, so the runtime's `install_seat_skills` - which rebuilds the seat's skills directory every start
  (`rust/agenthub-runtime/src/supervisor/mod.rs:406`) - installs the new text on the next seat start.
- No test pins the skill's prose, and none is added: the text is the product, not a copy of it. The guard
  test (`rust/agenthub-runtime/tests/seat_guard.rs`) keeps passing unchanged because the guard asset did not move.
