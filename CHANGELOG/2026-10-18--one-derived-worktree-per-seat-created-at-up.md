## One derived worktree per seat, created at `up` and reported by `doctor`

### Added

- `internal/clientworktree`: the ruling's Start, Check and its two verbs. `Ensure` creates or reuses each
  seat's own worktree, refuses - and names - a derived path that exists on another branch or is not a
  worktree, and refuses a branch no worktree holds rather than re-pointing it; `worktree list RIG` prints
  the room's derived worktrees and every foreign entry of `git worktree list`; `worktree prune RIG`
  removes only `/tmp/wt-*`, `/tmp/gate-*` and prunable entries, without `--force`, naming everything it
  kept. The two submodules are linked as worktrees of the main checkout's own submodule repositories
  (`git worktree add`), never `git submodule update`: a new linked worktree's update clones from a URL
  that cannot serve a never-pushed pin. Each writing seat's worktree gets `.env` linked to the client's
  one env file, because omp reads the key from its launch directory.
- `clientservices`: `SeatType`, `Worktree`, `DerivedWorktrees` (the seats in the room's own order, then
  `<room>/wt/integration` when the room has a lead), `RoomDir`, `TeamDir`, `TeamSeats`, `LiveSeats`, the
  `worktrees` row and its one failing state `worktrees_wrong`, and `SkillsRootEnv`. One derivation, so
  `up`, `doctor` and the verbs cannot disagree about a path.
- `up` runs the worktrees step between the gate and the seats: a seat's own worktree exists before it
  launches, and a derivation refusal refuses the start rather than launching a seat in the room.
- `doctor` carries a `worktrees` row per room: `running`, or `worktrees_wrong` when a derived path exists
  and is not the derived worktree. A path not created yet and a foreign entry are details, never
  failures - `up` creates the first and `worktree prune` owns the second.

### Changed from the branch this was ported from

- The seats the step derives come from the spec `up` already resolved (`spec.json` for a room that
  exists, the `--team` definition for a first start), not from a second read of `team.json`: a room must
  not be started with one team and have its worktrees derived from another. `doctor`'s row and the two
  verbs read the same `LiveSeats` over the room's own record.
- `RoomDir` is `<state root>/rooms/<rig>`, the room directory the runtime writes, and `doctor
  --state-root DIR` reads the row from the tree it was pointed at.
- The key link is `clientworktree.LinkProviderKey` over `clientenv.DefaultPath()`, so the package needs
  no `sync rig` verb (main has none).
- The room's rig list in the row is `agent.json`'s, the list the safeguard rows already read.

### Tests

- `internal/clientworktree`: the fixture builds real repositories and worktrees; the cases cover create,
  reuse, the two refusals, the chained submodule links, the legacy `-client` cleanup, the reviewer's
  detached worktree, prune's narrow scope (including a dirty worktree it must keep) and the verb surface.
- `internal/clientlifecycle`: the row's one failure, its details, its not-observed note, the doctor
  wiring (stdout/stderr/`--json`), and the `up` step - the seats it derives and that a refusal refuses
  the start; the existing order case now names the step between the daemon and the seats.
- Run: `go build ./...`, `go vet ./...`, `gofmt -l cmd internal` (nothing) and `go test -count=1 ./...`
  at this HEAD, all green. Smoked on the live room, read-only: `worktree list 4genthub-min` prints the
  six derived paths (three missing) and the foreign entries, and `doctor 4genthub-min` carries the
  `worktrees_wrong` row naming the three paths that exist on another branch.
