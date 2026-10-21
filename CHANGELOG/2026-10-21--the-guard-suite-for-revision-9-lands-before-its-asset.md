# The seat guard's revision-9 suite lands before its asset

## What lands

`rust/agenthub-runtime/tests/seat_guard.rs` arrives whole from `4genthub-min/guard-imports-a-tree` (`7f4512c`), on
a branch cut from client main `ec09aa0f`. Thirty rows: the twenty-seven already on main, the two index rows of
revision 8, and `the_pathspec_is_read_as_the_set_it_covers`.

That last row is revision 9's: an operand that is a directory or a glob names the guard inside it without naming it,
so a predicate `covers(here, operand)` reads the operand itself, every line of `git ls-files -- <operand>`, and a
bounded on-disk walk when the operand resolves to a directory. `rm -r rust/agenthub-runtime/assets` followed by
`git add` of the same directory was ALLOWED at the revision-8 asset as one call and as three, and HEAD's asset was
gone; the whole-index forms `git add -u`, `git add -a`, `git add -p` and `git commit -a`/`-am` were allowed beside
the already-refused `git add -A` because they name no path for any rule to read. The row pins the refusals and the
controls that must stay allowed: `git add -- <file>`, `git add -N -- <new>`, `git add src/`, `git commit -m x --
<file>`, a pathless `git commit` over a clean index, `git rm -r <unprotected dir>`, `git mv <dir>`, `git checkout
HEAD -- <dir>` and `rm -r <unprotected dir>`.

## Why the landing is split

The asset is the owner's: `assets/seat-guard.ts` is refused to every seat by `protectedPath`, the lead included, and
the guard branch is not mergeable — a merge of it reverts `supervisor.go` and `room.go`. So the test half lands here,
on main's own tree, and the asset travels as
`wt/lead/owner-actions/seat-guard-rev9-from-main.patch` (sha256 `806536475a20`), which dry-runs clean against
`ec09aa0f` (`patch --dry-run -p1 --fuzz=0`) and takes the asset from `3e7c033aa2e1` (revision 6) to `44be3f195328`
(revision 9). The owner applies that one file and commits it on this branch; the reviewer then gates the applied
tree, where the suite is green.

## Verification, measured on this branch

`cargo test --offline --test seat_guard` in `rust/agenthub-runtime` at `ec09aa0f` + this file, with main's
revision-6 asset still in place: **27 passed, 3 failed** —
`the_index_is_written_by_path_so_the_path_is_read` (`tests/seat_guard.rs:1381`),
`the_index_is_read_where_the_verb_names_no_path` (`:1480`) and `the_pathspec_is_read_as_the_set_it_covers`
(`:1668`). The three red rows are the point of the split: they pin the asset's rule, so the landing cannot pass by
accident and the asset commit is what turns them green. With `44be3f195328` applied, the same suite is 30 passed,
0 failed (measured on the same file at `4genthub-min/guard-imports-a-tree`).

**The three failures are expected at this tip and are not regressions**: main's asset is revision 6, and these rows
assert revision 8 and 9 behaviour it does not have.
