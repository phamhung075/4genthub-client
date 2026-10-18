## The clone and hooks rows reach doctor: what the shared checkout holds, and whether the guard is on

Plan item 1's two remaining checks, ported onto main's doctor.

### Changed

- `internal/clientlifecycle/clonerow.go` (new): the machine's row about the SHARED checkout. A seat commits in
  its own worktree, so a changed path in the shared checkout belongs to no seat - 4.1's third measured
  failure. The row names every changed path (porcelain, quoting off so a path with a space or a non-ASCII
  byte is printed as it is on disk), resolves the checkout through the same `clientworktree.MainRoot` `up` and
  the worktrees row use, and is a note rather than a failure when it cannot look.
- `internal/clientlifecycle/hooksrow.go` (new): the hooks folder GIT actually runs (`rev-parse --git-path
  hooks`, which honours `core.hooksPath`) and whether any hook there carries the guard's marker. Absent from a
  folder `core.hooksPath` NAMES is `guard_absent` - a switched-off guard, 4.4's fifth risk; never installed is
  a note, because `up` waits on these rows and no reader could act on it.
- `internal/clientlifecycle/doctor.go`: both rows are appended to the report (they carry no rig - one checkout
  holds every room's worktrees), their paths and folder print beside the rows, and their lines reach stderr
  when they are down.
- `internal/clientservices/worktrees.go`, `.../services.go`: `Clone`, `Hooks`, `GuardMarker`, `CloneDirty`,
  `GuardAbsent` - one constant each, so the installer and the reader cannot disagree.
- Tests: `clonerow_test.go` and `hooksrow_test.go` cover each row's outcomes and both doctor surfaces. The
  doctor fixture and `restoreAll` stub the two rows, because they read the checkout this binary was built in:
  a lifecycle case must not go red because the operator has a file open. (The branch's wiring cases asserted a
  `reasons` field on `--json`; main's `--json` is the rows, so they assert the row's state, and the reasons
  field belongs to batch 5's lane.)

### Smoke

Built binary, live machine: `doctor demo` reports `clone: clone_dirty` and names the checkout's changed files
(it is our own working tree), `hooks: running`; `doctor demo --json` carries both rows and no others change.

At this change: `go build ./...`, `go vet ./...`, `go test ./...` pass.
