---
name: changelog-updater
description: Write this repo's changelog entry - ONE new file CHANGELOG/<date>--<title>.md per merged task (not per commit), naming the proof. Use when a task merges or a change lands.
---

# Changelog updater

(This file's `CHANGELOG.md` model is retired; it describes the rule this repo runs: one dated file per merged task.)

This repo's changelog is not a file. Every merged task is **one new file**:
`CHANGELOG/<YYYY-MM-DD>--<kebab-title>.md` (740 such files at `ls CHANGELOG | wc -l` on 2026-10-18; there
is no `CHANGELOG.md`). A
change to the test suite additionally gets its line in `TEST-CHANGELOG.md`.

## Rules

- **One entry per merged task, not per commit.** A task's fix, its test and its correction share the one
  file, written with the task's first commit and finished before the merge; after the merge a change is a
  new task and gets its own file. Measured 2026-10-18: 56 of 71 commits carried an entry restating one task.
- **The entry rides a code commit.** A separate "changelog commit" is refused to a seat, because every path
  is documentation, so the entry is committed by pathspec together with the task's own change.
- **One task, one file.** Never append to another seat's entry; a landed entry is immutable, and a
  correction inside the same unmerged task edits the file, while a correction after the merge is a new
  dated file that names what it corrects.
- **Name the proof.** The shas, the counts from `git show --numstat`, the test command and its exit code.
  No adjective stands in for a number.
- **Attribution is the commit's path set**, read with `git show --numstat --format= <sha> -- <path>`, never
  the staging area. A landing merge that carries another seat's work carries that work's entry, in the same
  commit, and says so.
- **An entry records committed work.** A patch in a seat dir, a worktree edit or a note is prepared work
  with nothing to record yet; the entry is written with the task's first commit, and a task is not complete
  while its entry is missing.
- `TEST-CHANGELOG.md` is for the test suite only: a new or changed test file, a test that was deleted, a
  test that stops being meaningful. It is a line in that file, not a new dated file.

## Entry shape

```markdown
# <what changed, one line>

<One paragraph: what changed, why, and the measured number behind it.>

- Files: `path:lines`, `path:lines`
- Proof: `<test command>` exit 0; numstat 3 files +41 -7
```

## Writing it

- **Be concise**: "Optimized agent files: 1,878 -> 832 lines (55.7% reduction)", not a paragraph about the
  analysis that produced it.
- **Quantify when the number exists** - token savings, line counts, call counts, cost. When it does not,
  say nothing rather than fill the gap.
- **Active voice**: "Removed EnrichmentService (566 lines)".
- **Tables for comparisons**, prose for one change; a table of one row is a sentence.
- **Group one change's parts**, never group unrelated changes to save space.

## The retired model

The `CHANGELOG.md` + `## [Unreleased]` + `### Added/Changed/Fixed` model this skill used to teach does not
exist in this repo: there is no `CHANGELOG.md`, and an entry is never inserted at the TOP of a section.
`EXAMPLES.md`, `TEMPLATES.md` and `VALIDATION.md` beside this file still describe that model - do not follow
them until they are rewritten; this file is the rule.
