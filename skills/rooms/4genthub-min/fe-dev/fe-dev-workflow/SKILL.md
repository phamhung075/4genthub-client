---
name: fe-dev-workflow
description: "Use at the start of every session and before every task. The fe-dev seat's working method in 4genthub-min - claim one assigned frontend task, split it into disjoint component parts for a DeepSeek crew in your worktree, type-check and test with no local build, prove it in a browser against a deployed origin, and hand to the reviewer."
signals:
  - guard: local-frontend-build
  - row-tag: guide
  - command: 4genteam facts
  - command: 4genteam complete
  - command: npx tsc
  - command: browser
  - command: mcp:manage_task
---

# fe-dev - how this seat works

Your contract is `fe-dev-role` (smallest safe change, tests with the change, report only what you
ran). This is HOW you do it here.

## Job

You deliver changes to `agenthub-frontend/` (React, TypeScript, Tailwind, shadcn/ui). Shared types
live in `agenthub-frontend/src/types`, one definition each. Backend shapes come from `go-dev`, not
from your guess. You lead a crew of DeepSeek workers; the change is still yours.

## Session start

1. `4genteam whoami --json`, then `4genteam facts`.
2. `seat-worktree`: your own worktree and branch, never `main`.
3. `4genteam room queue list 4genthub-min`; `claim` your row; read its task with
   `xd://mcp__agenthub_http_manage_task` `get`.

## The loop

1. **Understand** the acceptance criteria. A UX or design choice you cannot settle from the task:
   `4genteam decide --tier seat`, then `lead`. An API shape question: `4genteam send` to `go-dev`.
2. **Survey** with read-only workers: where the component is used, the hook and query that feed it,
   the type it renders, the existing animation and theme classes it must reuse.
3. **Plan the split** by component or hook. Two parts that touch one file stay together; a shared
   type change is one part done first, by you.
4. **Run the crew**: one write worker per disjoint part, `--cwd` your worktree. Its order names the
   only files it may change, the design rules (`taste-skill`), and the check it must pass.
5. **Verify everything yourself**, NO LOCAL BUILD (owner rule 2026-10-17: never `vite build`,
   `npm run build` or `npx vite`, by you or a worker):
   - `npx tsc --noEmit -p .` and `npx vitest run` in the frontend checkout;
   - `git diff` every worker's change;
   - behaviour in a real browser with `fe-4genthub-driven` against a deployed origin, after the
     owner's deploy; before it, say the browser check is pending.
6. **Record**: the commit body is the record (owner ruling 2026-10-21) - what was wrong, what changed,
   how it was verified and whether the browser check ran, naming the tests and their result. There is no
   changelog file of any kind. One `=== Progress N ===` on the task.
7. **Commit by pathspec** through the wrapper - `python3 scripts/git-hooks/git_commit_capture.py -- -m "<type(scope): subject>" -- <paths>`
   in the hub, and in the client the same file by absolute path
   (`/home/daihu/__projects__/4genthub/scripts/git-hooks/git_commit_capture.py`), because the client keeps no copy;
   then `git show --numstat HEAD`. A commit that touches `agenthub_client/skills/**` also carries
   `Skill-Evidence:` - a `qitem-...`, the `4genteam skills evidence ...` run with what it showed, or
   `none - <reason>`; the wrapper's skills arm is not on main yet (it lands with the parked patch) - so until then
   the trailer is your word, and a plain `git commit` meets no check at all (see `seat-crew`).
8. **Hand off**: a queue row to `reviewer` (branch, range, the checks and results, what the browser
   check still needs). After approval, `4genteam complete --task <id> --base <sha> --summary "..." --test "npx tsc --noEmit -p ."`,
   and tell the lead the branch is ready.

## Your crew

- Write workers: one component or hook each, your worktree only, never a commit or a build.
- Read-only workers: usage sweeps, reading a long Playwright or vitest log, a contrast or
  layout audit of the deployed page (`agent-browser`, `dogfood`) that returns findings.
- Mechanics and the work-order template: `seat-crew`.

## Do not

- No local build, no Docker, no push; the frontend reaches production through the lead's push.
- No second copy of a type, a toast or a fetch that the WebSocket sync already owns.
- No compatibility props or fallback rendering for an old backend shape; follow the current one.
- Never claim a browser result you did not capture in this session.
