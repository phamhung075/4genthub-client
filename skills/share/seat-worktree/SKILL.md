---
name: seat-worktree
description: Every writing seat edits only in its own git worktree on its own branch, never in the shared main checkout. Main changes only by a gated merge done by the lead. Use before the first edit of a session and before every commit or merge.
---

# Seat Worktree

Owner decision, 2026-10-11. Every seat that writes files works in its own git worktree on its own branch. The shared main checkout (`~/__projects__/4genthub`) is read-only for seats: a second writer there changes the files and the index under every other seat, and each session then describes a different version of the code.

## Before the first edit

1. `pwd` and `git rev-parse --show-toplevel`, then `git branch --show-current`.
2. If the toplevel is the main checkout or the branch is `main`, stop. Do not edit. Ask the lead for your worktree; do not make one by hand.
3. Edit only inside your own worktree.

**Where a seat starts** (2026-10-16): the seat's launch directory is the room spec's `cwd` for that seat, else its own worktree under `<roomDir>/wt/` - `<seat>` first, then `<seat>-<repo>` - and the guard is handed that whole list. A seat the room names no `cwd` for, in a room that holds no worktree for it, does not start at all: the room's `team.json`/`spec.json` has to name one (`spec.json` for a room that is already up). The lead's own directory is the main checkout, which the lead merges into; a writing seat's is its worktree.

## Branches, merges and push

- The lead owns every branch and every merge. The lead creates each seat's branch and worktree, merges a seat's branch to `main` when the seat has finished and the reviewer gate has passed, and removes the branch afterwards.
- Only the lead works on `main`. No other seat edits, commits or merges there.
- Nobody pushes, the lead included. The owner pushes to the remote when the owner decides.
- A seat commits on its own branch with a pathspec: `git commit -m "..." -- <paths>`. Use `git add -N` for new files. A seat never rebases another seat's branch.
- A merge conflict is resolved by the author seat on its own branch, then the change goes through the gate again.
- A seat that finishes tells the lead the branch name and the commit range. The lead does the rest.

## Reading other seats' work

Read it from git (`git log`, `git diff main..<branch>`, `git show <commit>`), not from their files. Another seat's uncommitted files are not a source of truth.

## Cleanup

Stale worktrees (`/tmp/wt-*`, `/tmp/gate-*`, prunable entries in `git worktree list`) are listed and pruned by the client. Do not remove a worktree with `rm`. Use `safe-file-removal` for single files only.

## Ruled (architect, 2026-10-11: `ai_docs/core-architecture/seat-worktrees.md`)

- **Your worktree and branch:** `<roomDir>/wt/<seat>` on `<rig>/<seat>`, for example `4genthub-min/go-dev`. The reviewer's worktree is detached. Inside it, `agenthub_client` is on the same branch name, and `.claude` is detached. Never run `git submodule update` there.
- **The commit form (owner, pathspec fleet-wide, 2026-10-17):** commit by pathspec and do not stage first - `git commit -m "..." -- <paths>`; a new file gets `git add -N -- <new>` first, then the same pathspec commit. `git add -A`, `git add .` and `commit -a` stay forbidden.
- **Ready for a gate:** tell the lead your tip as `<rig>/<seat>=<sha>`.
- **The merge routine (the lead's):**
  1. The lead merges each ready tip `--no-ff` into `<rig>/integration`.
  2. The reviewer gates its tip `C`. The verdict names `C`, the main tip `M`, every `branch=sha` it contains, the commands run, and each result.
  3. The lead checks that integration = `C` and main = `M`, then runs `git merge --ff-only C` in the main checkout.
  4. The lead re-checks that main = `C`.
- **On a conflict:** the lead aborts and returns the item to you. Run `git merge <rig>/integration` on your branch, resolve it there, and name your new tip.
- **After a land that contains your tip:** run `git merge --ff-only main` before your next edit.
