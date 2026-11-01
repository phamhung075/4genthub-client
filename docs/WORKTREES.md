# Worktree and branch management

Owner, 2026-11-01: `~/.config/4genthub/rooms` had grown to 37 GB and nobody could tell which work had merged. Keep the project at the minimum needed and clean right after a merge. These two verbs are how.

## `4genteam worktree status`

```
4genteam worktree status (ROOM | --all | --main DIR) [--base BRANCH] [--no-size] [--json] [--state-root DIR]
```

Read only, with no git lock taken, so it is safe beside a live room. It walks `rooms/<room>/wt/` on disk, so it finds what no single `git worktree list` names: worktrees nested in worktrees, worktrees of several repositories in one room, and directories git does not know.

| Reports | Fields |
| --- | --- |
| Each thing under `wt/` | kind (`worktree`, `submodule`, `clone`, `broken`, `plain`), repo, branch or detached, merged or commits ahead, uncommitted and untracked counts, size, last commit date, owning seat, whether it is live, processes working in it |
| Each repository | base, stash count, prunable entries, worktrees registered outside every room (`foreign`) |
| Each local branch | merged or commits ahead, date, subject, the worktree holding it |

"Merged" means the tip is reachable from the base: `--base`, else origin's HEAD, else `main`, else `master`. Of a local base and its origin copy the one further along counts. A squash merge reads as unmerged; check by hand before deciding it.

## `4genteam worktree clean`

```
4genteam worktree clean (ROOM | --all | --main DIR) [--apply] [--branch BRANCH] [--keep GLOB]... [--drop-ignored]
                        [--base BRANCH] [--no-size] [--json] [--state-root DIR]
```

A dry run unless `--apply`.

**It removes** a linked worktree under a room's `wt/` that is clean and merged, nested ones first; a merged local branch, with `git branch -d`; a rebuildable cache, from a worktree that stays too (`target` with a valid `CACHEDIR.TAG`, or `node_modules`, with nothing tracked inside and git-ignored); and a plain directory holding no file at all.

**It lists as a decision, with every reason, and changes nothing for:**

- uncommitted or untracked files
- commits the base lacks
- a live seat (the runtime's status, or a seat's state file with a running pid; lanes `<seat>-<name>` count as the seat's, `integration` as the lead's)
- any process whose working directory is inside it
- a git lock
- a `.obsolete` anywhere in its path, whatever state it is in
- a plain directory with files, a broken `.git`, a clone of its own, or a populated submodule clone (git removes that only with `--force`)
- a nested worktree that stays
- ignored files that would not come back: `git worktree remove` deletes those silently, so a `target` without the tag holds its worktree. `--drop-ignored` accepts this for the whole run.

**It never** passes `--force`, moves a remote ref, drops a stash or a tag, or removes a worktree outside every room's `wt/` (the same rule `worktree prune` keeps; they are listed as `foreign`). A branch `-d` refuses is reported, not forced.

**A detached commit no ref reaches** gets the branch `preserved/<room>/<worktree>-<sha>` before the worktree goes. It then reads as an unmerged branch in every later status, so it stays somebody's decision.

**What a scope reaches:** `ROOM` is its worktrees, its `<room>/` branches and what its removals free. `--all` and `--main` reach every merged branch of the repositories they use, so pass `--keep` for a long-lived merged line. `--main` also narrows the room walk to that one repository.

**Exit codes:** 0 when every action happened; a decision is not a failure. 3 when an action failed. 5 when a seat other than a room's own lead asks for `--apply`.

## After a land

The client has no merge or land verb: a lead runs the `seat-worktree` merge routine by hand, and a director runs its own script. So the cleanup is a call that routine makes, right after the merge:

```
4genteam worktree clean --main <repo> --branch <branch> --apply
```

It takes only that branch and the worktrees holding it. A branch that has to stay exits 3 and names why, so a script need not read output; that is a follow-up, never a reason to undo the merge. A worktree outside the rooms keeps its branch, which is what a reused job worktree wants.

Add `ROOM` in place of `--main` to clean the same branch name in every repository the room uses (the seat's hub and client links share it).
