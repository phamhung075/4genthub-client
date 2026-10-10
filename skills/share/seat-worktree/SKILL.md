---
name: seat-worktree
description: Every writing seat edits only in its own git worktree on its own branch, never in the shared main checkout. Main changes only by a gated merge done by the lead. Use before the first edit of a session and before every commit or merge.
---

# Seat Worktree

Owner decision, 2026-10-11. Every seat that writes files works in its own git worktree on its own branch. The shared main checkout (`~/__projects__/4genthub`) is read-only for seats: a second writer there changes the files and the index under every other seat, and each session then describes a different version of the code.

## Before the first edit

1. `pwd` and `git rev-parse --show-toplevel`, then `git branch --show-current`.
2. If the toplevel is the main checkout or the branch is `main`, stop. Do not edit. Ask the lead for your worktree. The client creates it (`4genteam up <rig>`); do not make one by hand.
3. Edit only inside your own worktree.

## Committing and merging

- Commit on your branch with a pathspec: `git commit -m "..." -- <paths>`. Use `git add -N` for new files.
- `main` changes only by a merge or fast-forward after the reviewer gate, done by the lead. A seat never commits to `main`, never pushes, never rebases someone else's branch.
- A merge conflict is resolved by the author seat on its own branch, then the change goes through the gate again.

## Reading other seats' work

Read it from git (`git log`, `git diff main..<branch>`, `git show <commit>`), not from their files. Another seat's uncommitted files are not a source of truth.

## Cleanup

Stale worktrees (`/tmp/wt-*`, `/tmp/gate-*`, prunable entries in `git worktree list`) are listed and pruned by the client. Do not remove a worktree with `rm`. Use `safe-file-removal` for single files only.

## Open items

Branch naming, the exact merge routine and the pathspec change are ruled by the architect. Follow the committed ruling in `ai_docs/` when it exists; this skill carries only the rule that does not depend on it.
