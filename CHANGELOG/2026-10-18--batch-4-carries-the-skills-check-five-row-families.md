## batch-4 also carries the skills check: five row families in one doctor

`main` fast-forwarded from 07c24d7 to 041e5ed while this lane was merging, so the lane merged the new tip
too and resolved `doctor.go` a second time - now for the seats row beside the clone and hooks rows.

### Changed

- `internal/clientlifecycle/doctor.go`: `doctorJSONRows`/`rowReasons` take all five wrappers (worktrees,
  clone, hooks, seats, orphan, identity). The human report prints the clone, hooks, seats and identity lines
  in row order; the stderr tail prints the clone, hooks, seats and identity reasons for a down row, and the
  seats check's unreadable inputs whether or not a row is down.
- `internal/clientservices/services.go`: `SkillsStale` stays in the identity block beside `IdentityUnset`;
  `GuardAbsent` stays in the four-states block beside `CloneDirty`, so there is still one declaration each.
- `internal/clientlifecycle/doctor_test.go`: the fixture replaces all three seams (`cloneRowFor`,
  `hooksRowFor`, `seatsRowFor`) rather than one side's.

### Verified

`go build ./...`, `go vet ./...`, `go test ./...` green. Live `doctor 4genthub-min`: five families report
(`clone: clone_dirty`, `hooks: running`, `worktrees` lines, `seats: skills_stale` - the installed copies
predate the tree, cleared by a reseat - and the identity row), and `--json` carries reasons for each
(worktrees 5, clone 13, hooks 2, seats 12, identity 6).
