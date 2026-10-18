## Four doctor row families in one report: worktrees, clone, hooks, identity

`main` (07c24d7) and the batch-4 lane both appended rows to `runDoctor`; the merge keeps all four families
and gives the two machine rows the `--json` reasons `main` had added for the others.

### Changed

- `internal/clientlifecycle/doctor.go` (merge resolution): the human report prints the clone row's lines and
  the hooks row's, then the identity row's; the stderr tail prints the clone and hooks reasons for a down row
  and then the identity reasons. `doctorJSONRows`/`rowReasons` take the clone and hooks wrappers beside the
  worktree, orphan and identity ones, so `clone_dirty` and `guard_absent` carry their paths on `--json` too.
- `internal/clientservices/services.go`: `GuardAbsent` was declared by both sides (`identity`'s state block
  and the machine rows' block). One declaration stays, beside `CloneDirty`; the identity block keeps only
  `IdentityUnset`.

### Verified

`go build ./...`, `go vet ./...`, `go test ./...` green. Live: `doctor 4genthub-min` prints all four
families (`clone: clone_dirty` naming this checkout's changed files, `hooks: running`, the worktrees lines,
the identity lines) and `--json` carries their reasons (worktrees 5, clone 33, hooks 2, identity 6).
