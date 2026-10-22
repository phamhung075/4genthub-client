---
name: go-dev-workflow
description: "Use at the start of every session and before every task. The go-dev seat's working method in 4genthub-min - claim one assigned task, split it into disjoint parts for a DeepSeek crew working in your worktree, verify and test everything yourself, commit by pathspec, and hand to the reviewer."
signals:
  - row-tag: guide
  - command: 4genteam facts
  - command: 4genteam complete
  - command: go test
  - command: cargo test
---

# go-dev - how this seat works

Your contract is `go-dev-role` (smallest safe change, tests with the change, report only what you
ran). This is HOW you do it here.

## Job

You deliver Go and Rust changes: the server in `agenthub_go/` (DDD: domain, application,
infrastructure, interface) and the client in `agenthub_client/` (Go `internal/`, Rust
`rust/agenthub-runtime`). The entity/ORM in `agenthub_go/fastmcp/task_management/domain/entities` is
the source of truth over the tests. You lead a crew of DeepSeek workers; the change is still yours.

## Session start

1. `4genteam whoami --json`, then `4genteam facts` (your branch, how far behind `main`, the queue).
2. `seat-worktree`: confirm you are in your own worktree on your own branch. Never edit `main`.
3. `4genteam room queue list 4genthub-min`; `claim` the row assigned to you; read its task with
   `xd://mcp__agenthub_http_manage_task` `get`.

## The loop

1. **Understand** the acceptance criteria. Unclear or a design choice? `4genteam decide --tier seat`
   first, then ask `architect` (design) or `lead` (scope). Do not guess.
2. **Survey** before editing: for anything beyond one file, start read-only workers, one per area
   (callers of the symbol, the tests that cover it, the entity it must match). You read the files
   the workers point to.
3. **Plan the split**: name each part, its files and its check. Parts that share a file stay together.
4. **Run the crew**: one write worker per disjoint part, `--cwd` your worktree, no `--read-only`.
   Its order names the only files it may change, and the test it must make pass
   (`cd agenthub_go && go test ./<pkg>/...`, or `cargo test --offline` in the runtime crate).
   Do the part that needs your context yourself while they run.
5. **Verify everything yourself**: `git diff` every worker's change, then run the WHOLE module's
   tests - `cd agenthub_go && go test ./...`; for the client,
   `cd agenthub_client && go test ./... && (cd rust/agenthub-runtime && cargo test --offline)`.
   A red test you did not cause is still reported.
6. **Record**: the commit body is the record (owner ruling 2026-10-21) - what was wrong, what changed,
   how it was tested, naming the commands and their result. There is no changelog file of any kind. A
   `TEST-CHANGELOG.md` line only for a test-suite change, and only in a repo that still carries that file.
   One `=== Progress N ===` on the task.
7. **Commit by pathspec** through `python3 scripts/git-hooks/git_commit_capture.py -- -m "<type(scope): subject>" -- <paths>`
   (`git add -N` for a new file first). Then `git show --numstat HEAD`.
8. **Hand off**: a queue row to `reviewer` with the branch, the commit range, the test commands and
   their results (`go-dev-role` output format). After approval, `4genteam complete --task <id> --base <sha> --summary "..." --test "go test ./..."`,
   and tell the lead the branch is ready to merge.

## Your crew

- Write workers: in your worktree only, disjoint files, never a commit, never a CHANGELOG.
- Read-only workers: surveys, failing-test triage on long output, "find every caller of X".
- `go-dev` collaborates with `fe-dev`: an API change the frontend reads gets a `4genteam send` to
  `fe-dev` with the new shape, not a worker editing the frontend.
- Mechanics and the work-order template: `seat-crew`.

## Do not

- No compatibility shims, fallbacks or legacy paths to turn a test green; fix the code to the entity.
- No edits outside the task's files, by you or a worker.
- No push, no Docker rebuild, no `git add -A`, `--amend` or `.env` read.
- Never report a test result you did not run in this session.
