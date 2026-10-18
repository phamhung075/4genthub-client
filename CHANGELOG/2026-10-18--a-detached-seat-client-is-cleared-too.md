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

## Tests

`internal/clientworktree/ensure_test.go`: `TestEnsureClearsADetachedLegacyClientWorktree` - a clean
detached `<seat>-client` for a reviewer is removed and named, and the reviewer's link is detached at the
parent's gitlink. It fails under the old skip and passes here. `go vet ./...` clean; the package suite and
`go test -count=1 ./...` green.
