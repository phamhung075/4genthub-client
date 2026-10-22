---
name: self-check-while-working
description: Use at the start and the close of a work item - a short checklist that points at the checks already in force, and feeds friction-report. It names them; it does not restate them.
---

# Self-check while working

Two moments, one screen. This skill is a pointer: each line belongs to another skill or to the launch check
named below. Read the owner; do not copy it here.

## At item start

| Check | Owner |
|---|---|
| I am in my own worktree, on my own branch | `seat-worktree` |
| I have the skills and tools this item needs | the `F1`-`F5` launch check - defined by the seat-skills ruling, `ai_docs/core-architecture/seat-skills.md` §6 (4genthub repo); landed on client `main` (`617c283`, `992d184`) in `internal/clientlifecycle/skillscheck.go`, which `up`, `doctor` and `reseat` run |
| I know the acceptance criterion and where the evidence goes | the item's own brief |

## At item close

| Check | Owner |
|---|---|
| Fresh verification evidence before any completion claim | `verification-before-completion` |
| A test for the behavior I changed | `test-driven-development` |
| The branch name and commit range reported to the lead | `seat-worktree` |

## The one new duty

A check that fails, or a tool that is missing, is friction: file it with `friction-report` and continue. The
checklist's only job is to catch it before it costs more.

## Boundaries

- No gate, no ceremony. If it does not fit one screen, cut it back.
- It does not restate any rule it names.
