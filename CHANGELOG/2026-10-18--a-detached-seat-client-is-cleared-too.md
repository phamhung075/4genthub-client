# A detached seat's `<seat>-client` is cleared too

The migration's loop in `clearStaleClientWorktrees` skipped every derived seat with an empty branch, so
the reviewer's `<roomDir>/wt/reviewer-client` survived the live migration on 2026-10-18 (measured: clean,
detached at client main `287f7bc`). The record at hub `a75288f4` carries no writing-seat qualifier:
`<roomDir>/wt/<seat>-client` is the pre-derivation layout, and a reviewer's legacy is that layout too.
The skip bought nothing - a reviewer's client link is detached at the pin, so there is no branch a
removal would need to preserve - and it left a second client location in the room.

## Changed

- `internal/clientworktree/worktree.go` - the migration visits every derived seat. A detached legacy is
  removed clean-only like any other and printed with the existing "it was detached, nothing to lose"
  line; a refusal is still a note and the run continues. The reviewer's link stays detached at the pin,
  whether it already exists or is placed because the seat path has none.
- The architect's guard (`70aaa0fe`): a detached legacy whose `HEAD` the client repository's `main` does
  not contain is REFUSED and named, the same rule `prune` applies - removing it would delete the only
  reference to that commit, which exists only in that worktree's reflog. The seat is carried as blocked,
  and nothing is placed for it: no link at the derived path either, because a second client location is
  what the ruling forbids. It is the `headInMain` check `verbs.go` already uses, now called with the
  CLIENT repository as its root.

## Tests

`internal/clientworktree/ensure_test.go`: `TestEnsureClearsADetachedLegacyClientWorktree` - a clean
detached `<seat>-client` for a reviewer is removed and named, and the reviewer's link is detached at the
parent's gitlink. It fails under the old skip and passes here. `TestEnsureRefusesToClearADetachedLegacyOnNoBranch` -
a detached legacy carrying a commit no branch and no main contains is refused and named, the legacy stays
registered at that commit, no link is placed, and `Ensure` returns nil. It fails without the guard
(`c9dfee7`) and passes with it. `go vet ./...` clean; the package suite and
`go test -count=1 ./...` green.
