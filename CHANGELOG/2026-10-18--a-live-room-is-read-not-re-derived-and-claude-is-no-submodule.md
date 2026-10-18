## A live room is read, not re-derived, and `.claude` is not a submodule

### Fixed

- `internal/clientworktree/worktree.go` (`ensureOne`): a derived path that is already a registered
  worktree is **kept** - no branch check, no re-pointing, no submodule work - and only a path that is
  not registered is derived. The old step refused any registered worktree whose branch was not the
  derived one, so `4genteam up` refused **6 of 6** on the live `4genthub-min` room: lead and integration
  on `.claude`, go-dev's branch bare, fe-dev and architect on their own lanes, the reviewer on its own
  branch instead of detached. A room that is up must be readable, or the heal after a reboot refuses a
  room that is fine.
- `internal/clientworktree/worktree.go` (`.claude`): the submodule list is the client submodule alone.
  Measured on this hub (2026-10-18): `.gitmodules` records `claude-automation` and `agenthub_client`,
  `git rev-parse HEAD:.claude` answers `path '.claude' exists on disk, but not in 'HEAD'`, and no derived
  worktree carries a `.claude` at all. The link was therefore an unconditional failure for every seat.
- `internal/clientworktree/worktree.go` (`createWorktree`): a branch that exists with **no holder** is
  the seat's own branch and is attached to (`git worktree add <path> <branch>`, re-pointing nothing);
  `rejectBareBranch` is retired. The live go-dev was exactly that state. A branch another worktree
  holds is still refused by name.
- `internal/clientworktree/check.go`: `Registered` is a registered worktree with the client link
  present, whatever branch it holds; `Wrong` is a path that is not a worktree or a registered worktree
  with no client link. The branch comparison moved out of the reading with the rule it enforced.

### Testing

- `go vet ./...` clean; `go test -count=1 ./...` 35 packages ok.
- `internal/clientworktree`: the another-branch case became `TestEnsureKeepsADerivedPathOnAnotherBranch`,
  the bare-branch case became `TestEnsureCreatesTheSeatWorktreeOnItsOwnExistingBranch` (asserting the
  branch sha does not move), the pin case asserts the client link only and that no `.claude` path is
  created, and Check's bucket case moves the another-branch worktree into `Registered` and makes `Wrong`
  a missing client link.
- Live: `4genteam up 4genthub-min --room-only --allow-peak` at this lane - the same command that
  refused 6 of 6 - answers `lead: kept`, `go-dev: created`, `fe-dev: kept`, `reviewer: kept`,
  `architect: kept`, `integration: kept`, reads all five seats `running`, and exits 0 with no seat
  restarted. A second run is six `kept` lines. `worktree list` no longer reports a missing path.
