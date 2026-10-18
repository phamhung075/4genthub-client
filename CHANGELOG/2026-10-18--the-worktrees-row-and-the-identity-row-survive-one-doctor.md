## The worktrees row and the identity row survive one `doctor`

`main`'s worktrees row and this lane's identity row both rewrote `runDoctor`'s print tail; the merge
keeps both, and the JSON reasons are derived from one wrapper list instead of two.

### Changed

- `internal/clientlifecycle/doctor.go` (merge resolution of `9584d1f` into `7ceb690`): the human report
  prints the worktrees row's lines and then the identity row's, and the stderr tail does the same for
  both; `doctorJSONRows`/`rowReasons` take the worktree wrapper beside the orphan and identity wrappers,
  so a `worktrees_wrong` row carries its wrong paths on `--json` too. One rule for both surfaces.

### Tests

- `go build ./...`, `go vet ./...`, `go test ./...` pass on the merge. Live: `doctor 4genthub-min`
  prints five `worktrees:` lines and five `identity:` lines, and `--json` carries both rows with reasons.
