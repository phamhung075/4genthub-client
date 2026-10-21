# `RoomDir` takes the state root the verb resolved

## Changed

- `internal/clientservices/worktrees.go`: `RoomDir(root, room)` now JOINS - `<root>/rooms/<room>` - and
  never resolves a root itself. An empty root or room is refused by name. The old version called
  `clientenv.DefaultStateRoot()` internally, so `up --state-root DIR` resolved the store for the daemon
  and the gate and then derived the seat worktrees under the machine's default: a silent wrong store.
- `internal/clientlifecycle/lifecycle.go`: `up` passes the root it resolved (`stateDir`) into
  `seatCwdsByDerivation` and `ensureWorktrees`, whose impls hand it to `RoomDir`. `worktreeRoomDir` (the
  seam over `clientworktree.RoomDir`) carries the pair.
- `internal/clientlifecycle/worktreesrow.go`: doctor's worktrees row calls the one resolution with the
  row's root instead of joining `root/rooms/<room>` itself, so the duplicate spelling is gone.
- `internal/clientworktree/verbs.go`: `roomOf(root, room)` takes the root; `worktree list` and
  `worktree prune` gain `--state-root DIR` through `clientenv.StateRootFromFlags` (the retired
  `--state-dir` spelling is refused by the same extractor, by name, exit 2). The `filepath.Dir(Dir())`
  round trip that recovered the root from the room's path is deleted - the root is passed in.
- `internal/clientlifecycle/overlap.go`: the scan's `roomDir` closure is bound to the root the verb
  resolved, instead of the package's resolution.
- `internal/clientlifecycle/skillscheck.go`: the room directory is read from the already-resolved root
  through the one resolution, rather than joining it here.

## Tested

- `go test ./...` in `agenthub_client` -> ok (whole module).
- `TestRoomDirJoinsTheGivenRootAndRefusesAnEmptyOne` pins the join and both refusals;
  `TestTheWorktreeStepsAreHandedTheResolvedRoot` pins that both `up` worktree steps are handed the root
  the verb resolved, not a re-derivation; `TestTheOverlapScanReadsTheRoomUnderTheResolvedRoot` pins the
  scan's binding.
- Live: `4genteam worktree list --state-root /tmp/sr-smoke2 --main <repo> demo` prints
  `/tmp/sr-smoke2/rooms/demo/wt/{lead,go-dev,integration}` as its derived paths; the same verb with
  `--state-dir /tmp/sr-smoke2` exits 2 naming `--state-root DIR`; without the flag it reads the machine
  store and says so (`/home/daihu/.config/4genthub/rooms/demo/spec.json is unreadable`), which is the
  derivation following whichever root was resolved.
