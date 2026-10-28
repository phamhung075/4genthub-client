---
name: seat-worktree
description: Every writing seat edits only in its own git worktree on its own branch, never in the shared main checkout. Main changes only by a gated merge done by the lead. Use before the first edit of a session and before every commit or merge.
signals:
  - guard: worktree-scope
  - guard: push
  - guard: git-imports-a-tree
  - command: git diff
---

# Seat Worktree

Owner decision, 2026-10-11. Every seat that writes files works in its own git worktree on its own branch. The shared main checkout (`~/__projects__/4genthub`) is read-only for seats: a second writer there changes the files and the index under every other seat, and each session then describes a different version of the code.

## Before the first edit

1. `pwd` and `git rev-parse --show-toplevel`, then `git branch --show-current`.
2. If the toplevel is the main checkout or the branch is `main`, stop. Do not edit. Ask the lead for your worktree; do not make one by hand.
3. Edit only inside your own worktree.
4. Do not open a worktree outside the room's wt/. A lane is <room>/wt/<seat>-<name>, which the guard allows by the prefix it was launched with; any other path is refused.

**Where a seat starts** (2026-10-16): the seat's launch directory is the room spec's `cwd` for that seat, else its own worktree under `<roomDir>/wt/` - `<seat>` first, then `<seat>-<repo>` - and the guard is handed that whole list. A seat the room names no `cwd` for, in a room that holds no worktree for it, does not start at all: the room's `team.json`/`spec.json` has to name one (`spec.json` for a room that is already up). The lead's own directory is the main checkout, which the lead merges into; a writing seat's is its worktree.

## Where to build and test

Every seat builds and tests where it edits, **the lead included**: in a worktree under `<roomDir>/wt/`, never in the shared main checkout. A build there (`cargo build`, `go build`, `npm run build`, a test run) writes build output and lock files into the tree the owner works in, and it contends with the owner's own builds (observed 2026-10-25, room aa2-rs: the lead ran `cargo build` in the main checkout instead of `<roomDir>/wt/lead`).

- A seat's own change: build and test in its own worktree.
- A merge result: build and test the integration tip in `<roomDir>/wt/integration` (or a detached worktree at that sha). That is the tree the gate names.
- The main checkout only receives `git merge --ff-only C`. After a fast-forward, main's tree IS `C`'s, so the re-check is `git rev-parse main` equals `C`, not another build. A room guide that says "re-run the checks on the merge result" means the integration tip, before the fast-forward.

## Branches, merges and push

**First, does your room have a director?** A room declares one by holding `<roomDir>/director/` (check: `test -d <roomDir>/director && echo director`). A director is a principal session outside the room that alone updates `main`; the founder pushes. When the directory exists, the director rule below replaces every "the lead merges to `main`" and "the lead pushes" line in this skill, in your room guide's older text and in any queue row or decision that says otherwise; the seat guard enforces it (rules `push` and `director-main`). A room without the directory follows the lead rules below unchanged.

**Director rule (room has `<roomDir>/director/`):**

- The lead still owns the seats' branches and `<room>/integration`: it creates branches and worktrees, merges ready tips into integration, and runs the reviewer gate.
- The lead does NOT update `main`: no merge, pull, cherry-pick, rebase, commit or reset on `main`, and no `update-ref`, `branch -f` or fetch refspec that moves it.
- The lead lists each gated branch in `<roomDir>/director/ready.md` as `<branch>=<sha>` with the gate's verdict (main tip, commands, results). The director merges from that list.
- `ready.md` is the one file in `<roomDir>/director/` a seat may write, and only the lead, only by adding to it: append with `>>` or `tee -a`, or use your edit tool. The seat guard (`worktree-scope`) refuses a `>` or a whole-file write over it, every other office file (`plan.md`, `loop.md`, `status.md`, `crons.md`), and the list for every other seat. A `cd` earlier in the same shell call does not move a path out of the guard's view.
- No seat pushes, the lead included. The founder or the director pushes.

**Lead rule (room without a director):**

- The lead owns every branch and every merge. The lead creates each seat's branch and worktree, merges a seat's branch to `main` when the seat has finished and the reviewer gate has passed, and removes the branch afterwards.
- Only the lead works on `main`. No other seat edits, commits or merges there.
- Only the lead pushes (`main`, `frontend`), after the reviewer gate and a green test run. No other seat pushes.
- A seat commits on its own branch with a pathspec: `git commit -m "..." -- <paths>`. Use `git add -N` for new files. A seat never rebases another seat's branch.
- Importing a tree is the same move: `git apply`, `git am`, `patch`, `git restore <path>`, `git checkout -- <path>` and `git stash pop|apply|branch` are refused when they would write a protected path, because files move between trees without a commit. `git checkout`, `git switch`, `git cherry-pick`, `git merge` and `git rebase` are allowed on a plain commit and are read only when an operand resolves into the stash reflog — a stash entry is a commit.
- A merge conflict is resolved by the author seat on its own branch, then the change goes through the gate again.
- A seat that finishes tells the lead the branch name and the commit range. The lead does the rest.

## Making a CLIENT tip reachable before naming its sha

A worktree's `agenthub_client` is a **separate clone**, not a checkout of the canonical one: inside
`<roomDir>/wt/<seat>/agenthub_client`, `.git` is a file pointing at
`~/__projects__/4genthub/.git/worktrees/<worktree>/modules/agenthub_client`, which has its own objects and
its own `origin`. A commit made there is **not an object** in `~/__projects__/4genthub/agenthub_client`, so
the lead's `git cat-file -t <sha>` and `git log --all` there say it does not exist - a correct check of the
wrong repo (measured 2026-10-18: three tips read as missing; the fetch below made all three visible).

Before you name a client sha, write the branch into the canonical repo:

```
git -C ~/__projects__/4genthub/agenthub_client fetch \
  ~/__projects__/4genthub/.git/worktrees/<worktree>/modules/agenthub_client \
  <branch>:refs/heads/<branch>
```

It writes a ref and objects only - no working tree, no index, no remote push - and it needs no force when the
branch does not exist there yet. Then prove it from the repo the lead reads:
`git -C ~/__projects__/4genthub/agenthub_client cat-file -t <sha>` and `log --all --oneline | grep <sha>`.
Name the sha in the same message you name the fetch, so the lead's first check is the true one.

## Reading other seats' work

Read it from git (`git log`, `git diff main..<branch>`, `git show <commit>`), not from their files. Another seat's uncommitted files are not a source of truth.

## Cleanup

Stale worktrees (`/tmp/wt-*`, `/tmp/gate-*`, prunable entries in `git worktree list`) are listed and pruned by the client. Do not remove a worktree with `rm`. Use `safe-file-removal` for single files only.

## Ruled (architect, 2026-10-11: `ai_docs/core-architecture/seat-worktrees.md`)

- **Your worktree and branch:** `<roomDir>/wt/<seat>` on `<room>/<seat>`, for example `4genthub-min/go-dev`. The reviewer's worktree is detached. Your client code lives in exactly one place, `<roomDir>/wt/<seat>/agenthub_client`, on the branch you commit to: `<room>/<seat>` for a seat that has none, and the lane branch a seat migrated off `<roomDir>/wt/<seat>-client` kept - a second copy at `<seat>-client` is the pre-derivation layout, and `up` removes it, clean only, so the branch survives. `.claude` is not a submodule here and is never linked. Never run `git submodule update` there.
- **The commit form (owner, pathspec fleet-wide, 2026-10-17):** commit by pathspec and do not stage first - `git commit -m "..." -- <paths>`; a new file gets `git add -N -- <new>` first, then the same pathspec commit. `git add -A`, `git add .` and `commit -a` stay forbidden.
- **Ready for a gate:** tell the lead your tip as `<room>/<seat>=<sha>`.
- **The merge routine (the lead's; in a room with a director, steps 3 and 4 are the director's, and the lead stops after step 2 by listing `C` and its verdict in `<roomDir>/director/ready.md`):**
  1. The lead merges each ready tip `--no-ff` into `<room>/integration`.
  2. The reviewer gates its tip `C`. The verdict names `C`, the main tip `M`, every `branch=sha` it contains, the commands run, and each result.
  3. The lead checks that integration = `C` and main = `M`, then runs `git merge --ff-only C` in the main checkout.
  4. The lead re-checks that main = `C`.
- **On a conflict:** the lead aborts and returns the item to you. Run `git merge <room>/integration` on your branch, resolve it there, and name your new tip.
- **After a land that contains your tip:** run `git merge --ff-only main` before your next edit.
