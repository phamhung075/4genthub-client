# The legacy client worktree is identified by its path, and a kept seat gets its missing link

Ruling: hub `a75288f4`, `ai_docs/core-architecture/seat-worktrees.md` § "Where a seat's client code
lives" (`qitem-20261018121114-9209ad06b981`): client code lives in exactly ONE place,
`<roomDir>/wt/<seat>/agenthub_client`. Measured 2026-10-18 on the live room: the architect's and
fe-dev's client work sat in `architect-client` (`4genthub-min/architect-client`) and `fe-dev-client`
(`4genthub-min/fe-dev-edit-yield`), while their derived paths carried no link — so `Check` called two
working seats wrong and the row could never go green.

## Changed

- `internal/clientworktree/worktree.go` — `clearStaleClientWorktrees` is scoped by PATH:
  `<roomDir>/wt/<seat>-client` for a derived writing seat is the subject whatever branch it holds (the
  old test for the derived branch name never matched a lane branch, which is what the two live seats
  held). It answers the branch each cleared legacy held, per seat, and a removal git refuses - the
  worktree holds uncommitted or untracked work - is a printed note naming the path and the seat, not an
  error; the run continues. `ensureOne` takes that branch and, for a KEPT path whose
  `<seat>/agenthub_client/.git` is absent, places the link through `placeClientLink`: on the legacy
  branch when the migration freed one, else on the branch the derivation names, created at the parent's
  gitlink when it does not exist. An unplaceable link (the branch is held at a path this run does not
  own) is a note too - `Check` keeps naming the path, which is the truthful state.
  `linkSubmodule` takes the branch as an argument instead of reading it from the worktree.
- `skills/share/seat-worktree/SKILL.md` — names `<seat>/agenthub_client` as the one client location (the
  lane branch for a seat migrated off `<seat>-client`) and drops the sentence that `.claude` is detached
  in a seat worktree: it is not a submodule here and is never linked.
- `Check` and its client-link test are unchanged: every derived path is registered, whatever branch it
  holds, with the `agenthub_client` link present.

## Tests

`internal/clientworktree/ensure_test.go`: the legacy path is cleared whatever branch it holds (branch
and its commit survive); a dirty legacy is named and kept with no link placed while the rest of the room
is still created; a kept path's link attaches to the legacy's lane branch and its commit; a kept path
with no legacy and no branch gets the link created at the parent's gitlink; an unplaceable link is a
note, and the run succeeds. The pre-existing clearance case still passes: the branch survives and the
link attaches to it.

`go vet ./...` clean; `go test -count=1 ./...` green in `agenthub_client`.

## Not done here

The live room is not migrated by this change: `up` performs the migration, and running it on the live
room removes `fe-dev-client` and `architect-client` and attaches their branches - the lead's step after
the gate.
