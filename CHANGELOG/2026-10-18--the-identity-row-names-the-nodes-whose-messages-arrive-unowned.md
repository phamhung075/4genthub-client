## The `identity` row names the nodes whose messages arrive unowned

Batch 5 of the supervisor-port table, part 2: `9a530e9` onto `01a2837`.

### Added

- `doctor` reports **`identity`**: one row per room, naming every node the daemon can attribute
  (`canonicalSessionName`, or a `logicalId` it resolves by, with the runtime whose messages carry it), and
  failing on each node it cannot. A node with neither field sends as `<unknown sender>` forever — the ruling's
  measured failure — and nothing in the message text tells a reader which node that was.
- The row carries the standing line that an identity is a **claim**, never a proof: it says what a message
  asserts, not who sent it. That line is unconditional, because a row read as authorship is worse than no row.
- The node list is the SAME reading the peak gate and the restore make (`rigNodes` → `clientruntime.RoomNodes`),
  so the three cannot disagree about which nodes exist. A read that fails is a note, not a refusal, like every
  other check row.

### Changed

- `doctor --json` carries the row's lines as `reasons` (the mechanism added in part 1), so a monitor sees the
  unnamed node rather than just `identity_unset`.
- `TestDoctorIsReadOnly` now separates a **read** from an **action**: it still fails if `doctor` spawns,
  restarts or signals anything, and it now also fails if the identity row's node read is missing.

### Tests

- `internal/clientlifecycle/identityrow_test.go`: every attributed node named with its runtime; a node with no
  identity fails the row with the `<unknown sender>` line; a logical id alone is an identity; an unreadable node
  list is a note and the row stays running; and the wiring on all three surfaces (stdout, stderr, `--json`).
- `gofmt -l internal/ cmd/` empty, `go vet ./...` rc 0, `go test -count=1 ./...` 34 packages ok.
- Smoke, the built binary: `4genteam doctor --json` on this box prints the `identity` row with the four real
  nodes (`agenthub--4genthub-min--lead`, `--go-dev`, `--fe-dev`, …, each `(runtime omp)`) as `reasons`.

### Still not carried from batch 5

The clone row (`04e7140`) and the hooks row (`73d0be5`) both resolve the shared checkout through
`clientworktree.MainRoot`, which lives only on the gated `4genthub-min/batch-2-worktree` branch. The access and
guide checks, the launch/reseat wiring and the seats row they feed come after them. None is claimed here.
