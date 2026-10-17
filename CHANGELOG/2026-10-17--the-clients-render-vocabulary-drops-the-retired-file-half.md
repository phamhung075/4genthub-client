# The client's render vocabulary drops the retired file half

The hub's render is a resolution record now, not a delivery: it stops rendering `agent.yaml`,
`startup.files`, a skills list and `guidance/role.md`, and a seat's skills come from the client skill tree
(share, room, seat tier) while `<seat dir>/mcp.json` is written by the runtime at start. The client tree
kept teaching the old shape in three places, and a reader that believes them goes looking for a file that
does not exist.

### Changed

- **`skills/client/rig-runtime-switch/SKILL.md`** - the "Gotchas seen live" bullet that said role files
  (`agents/<seat>/guidance/role.md`) mention MCP tools because the rendered seat carries them now says what
  actually carries them: the seat's role and instructions arrive as skills (the client tree's share, room
  and seat tiers) and its tool surface as `<seat dir>/mcp.json`, written by the runtime at every start; a
  seat type that declares no mcp block gets no server there and the kickoff message is what says so.
- **`skills/client/spawn-team/SKILL.md`** - "4genteam renders each seat's own `agent.yaml` and guidance at
  launch" is replaced by the measured mechanism: the runtime installs the seat's skills from the client tree
  and writes the seat's `mcp.json` at every start, so a seat has its own MCP whenever its seat type declares
  mcp blocks. The 2026-10-10 measurement it was drawn from stands and is kept.
- **`internal/clientsync/seatpolicy.go`** - the package header now states what the file half is and, with
  `file:line`, what writes a seat's files: `install_seat_skills` copies the resolved skill tree into
  `<seat dir>/skills/` (`rust/agenthub-runtime/src/supervisor/mod.rs:400`) and `start_seat` writes
  `<seat dir>/mcp.json` (same file, `:704`). A reader asking "who produced this file in the seat's launch
  directory" is answered in the one file that still reads the render.

### Measured

- **No client CODE reads a retired name.** `grep -rn 'guidance/role.md|agent.yaml|startup.files' --include='*.go'
  --include='*.rs' --include='*.ts' --include='*.tsx'` over the tree returns only comments and this entry's
  own prose; the one test fixture that carries `agent.yaml` (`internal/clientsync/bundleverb_test.go`) uses
  it as an archived file NAME inside a bundle, which is a different subject.
- **`grep -rn 'guidance/role.md'` over the tree after this change returns**: the five new seat-role skills'
  provenance line (a mention that says the path is retired), the two new comments above, the captured
  production baseline (`skills/rooms/4genthub-min/fe-dev/fe-4genthub-driven/baseline/production.json`, page
  text recorded on 2026-10-11 - editing it would falsify a measurement), and `CHANGELOG/` history. Nothing
  left states that the render delivers one. The same census for `agent.yaml` / `startup.files` adds
  `docs/AGENT-SETUP.md`, which already says nothing writes a seat's `agent.yaml` any more.
- `docs/AGENT-SETUP.md` needed no edit: it names `install_seat_skills` and `AGENTHUB_CLIENT_SKILLS` and
  already carries the correction.

### Testing

- `go test -count=1 ./...` all packages ok, 0 FAIL; `go vet ./...` rc 0; `gofmt -l internal/ cmd/` clean.
- `cargo test --offline` in `rust/agenthub-runtime`: 167 passed, 0 failed.
- `internal/retiredverbs` stays green: the two skills this entry edits keep their allowlist entries for the
  predecessor's commands they name in order to forbid (the pin's own coverage test fails an entry whose line
  was reworded, and both lines are untouched).

### What this does not do

- It does not rename `rig` to `room` anywhere: that is the board's C8 unit and it waits on the owner's four
  answers. The client's own delivered vocabulary still says "rig" where it always did.
- It does not edit the captured production baseline under `fe-dev/fe-4genthub-driven/baseline/`, which is
  recorded page text.
