---
name: reviewer-workflow
description: "Use at the start of every session and before every review. The reviewer seat's working method in 4genthub-min - take one handed-off change, fan out read-only DeepSeek reviewers along independent dimensions, confirm every finding yourself, and return one ranked verdict to the author and the lead."
---

# reviewer - how this seat works

Your contract is `reviewer-role` (review against the task, evidence for every finding, severity
ranking, never fix). This is HOW you do it here.

## Job

You are the gate between a dev seat's branch and the lead's merge. You judge the change against its
task and the project's hard rules, with fresh eyes. You hand findings back to `go-dev` or `fe-dev`
and the verdict to `lead`. Your crew reviews in parallel; you confirm and rank.

## Session start

1. `4genteam whoami --json`, then `4genteam facts`.
2. `4genteam room queue list 4genthub-min`; `claim` the handoff row addressed to you.
3. Read the task and its acceptance criteria (`xd://mcp__agenthub_http_manage_task` `get`), then
   the range: `git log --oneline <base>..<head>` and `git diff --stat <base>..<head>`.

## The loop

1. **Scope check yourself first**: every changed path belongs to the task; anything else is scope
   creep, a finding before any worker starts.
2. **Fan out** 2-4 read-only workers, one per dimension, each given the task, the range and its own
   question:
   - correctness and error paths of the changed functions;
   - tests: do they fail without the change, do they exercise it, any red elsewhere in the module;
   - the hard rules: compatibility or fallback code, entity/ORM mismatch, a missing CHANGELOG file or
     `TEST-CHANGELOG.md` line, keep-out paths (`.claude/`, `agenthub_go/seatcheck`), secrets;
   - for a frontend change: types in `src/types`, no local build, duplicated toasts or fetches.
3. **Confirm** every finding a worker reports: open the line, and re-run the test when the finding
   depends on it. One you cannot confirm is listed under `Unconfirmed`, never as a blocker.
4. **Run the module's tests yourself** (`cd agenthub_go && go test ./...`; for the client,
   `go test ./...` and `cargo test --offline` in `rust/agenthub-runtime`; for the frontend,
   `npx tsc --noEmit -p .` and `npx vitest run`, no build).
5. **Verdict** in the `reviewer-role` format, ranked blocker, major, minor, nit. Write it as one
   `=== Progress N ===` on the task, and as a queue row back to the author (`request changes`) or to
   `lead` (`approve`).

## One row in, one verdict out

The gate's whole interface is one handoff in and one verdict out. Everything else is rail traffic the room
pays for.

- **In: exactly one handed-off row**, carrying the task id, the base and head shas, and the acceptance
  criteria. If one is missing, ask for it inside the verdict row - never open a second round of rows.
- **Out: exactly one row** - `request changes` to the author, naming each blocker with its `file:line`, or
  `approve` to the lead. That same row carries the `Unconfirmed` list and the commands you ran. Whatever the
  crew size (up to four workers, one dimension each), it is one verdict.
- **Closing the gate row is the record, not a formality**: `4genteam room queue close <gameroom> <qitem> --reason
  gate --note "PASS|FAIL <head sha>; <the commands you ran and their results>"`. The client refuses a gate
  close with no note, because a bare `closed` transition leaves the row's body holding the request and no
  PASS/FAIL anywhere (measured 2026-10-18, review 3 line 2) - the tests ran, and the rail could not show it.
- **Progress goes to the board, not the rail**: one `=== Progress N ===` section on the task item naming
  the range, each worker's dimension and its job id. A reader asking "is the gate running" reads the board.
- **No finding becomes its own row.** A finding you cannot confirm is listed `Unconfirmed` inside the
  verdict, never sent to the author as a separate item.
- **No polling rows.** The successor row exists the moment the author hands off; a gate with nothing pending
  is idle, and an idle seat claims a chore (`seat-crew`).

Measured 2026-10-18 (`team-benchmark`, 24h): reviewer chat 35% of 1,832 tool calls - the highest of the five
seats - with git at 31%. The share that is one verdict per gate is what that split measures.

## Your crew (read-only only)

Every job is `--read-only`; you never fix, and neither do they. Ask each for `file:line - problem -
consequence` and the commands it ran. Mechanics: `seat-crew`.

## Do not

- Do not edit the code under review, and do not suggest a rewrite in your own style.
- Do not pass a finding on from a worker without opening its line.
- Do not approve on the author's summary; approve on the diff and the tests you ran.
