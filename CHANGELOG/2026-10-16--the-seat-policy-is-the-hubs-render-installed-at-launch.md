# The seat policy is the hub's render, installed at launch

The architect ruled it (`RULING-seat-policy-source-and-install.md`, hub task
`d70eff82-9747-4038-8405-dd0eca48d96d`): a seat's permission policy has ONE source, the hub's rendered
`runtime/omp-config.yml`, and the client installs that document as `<seat dir>/config.yml` at launch. This
is that change, in one act.

### Changed

- **The client's role table is deleted, and the divergence it carried is why.** `internal/clientpolicy`
  held `commonBashDeny`, `nonLeadBashDeny`, `nonLeadToolDeny` and `reviewerToolDeny`, with `edit`/`ast_edit`
  denied for the role `reviewer` alone. Measured against the live hub, seat by seat (`policy render`, five
  seats of room `4genthub-min`): `architect` and `reviewer` deny `edit`, `ast_edit` and the three
  `manage_*` tools; `go-dev` and `fe-dev` deny only the three `manage_*` and keep `edit`/`ast_edit`; `lead`
  carries no `tools:` block at all. The deleted table would have left the architect its `edit` tool - the
  one seat the owner's item is about - and it denied `edit` to a developer seat that needs it.
- **`4genteam policy show|apply --team` is gone; the verb is read-only.** `policy show` needed a team file
  (`room.slug` + `seats[]`), the room has no `team/` dir, and the room's own `spec.json` is refused
  (`cannot unmarshal string into Team.room`) - so the path that would have installed the file could not
  run at all. `4genteam policy render --room RIG --seat SEAT [--state-root DIR] [--check]` prints what the
  hub would install (the client's own non-policy keys included) and, with `--check`, compares it against
  the installed file: `DRIFT` and a non-zero exit when the file is missing or differs. `PolicyKeysMatch`
  is kept, because the drift report is the comparison it makes.
- **The document is fetched, never re-derived.** New `internal/clientsync/seatpolicy.go`: `SeatRender`
  reads the hub's `runtime/omp-config.yml` out of the resolved seat's file list, `InstallDocument` adds
  this client's own non-policy key (`compaction.keepRecentTokens`, and only when the render does not
  already state a top-level `compaction:`, which it does today), and `SeatDocumentFor` is the one place a
  start or a restart asks for it. A render that cannot be READ does not stop a start: the line names the
  room, the seat and the hub's reason, and the seat comes back on the room's own record - a scratch room's
  seats are not on the hub, and `up` has never required the hub.
- **The document travels on the frame, and the runtime writes it.** `clientruntime.SeatSpec` gains
  `policy_document` (nil omits the key, so a seat with no document gets NO file - the absence rule the seat
  directory already follows for `mcp.json`), and `RestartSeat` takes it as a third argument. It is carried
  by every route that STARTS a seat: `up` per omp seat of the spec, `room reseat` and `seat restart|switch`
  each fetch one for the seat being restarted, while `heal` and `restore` deliberately pass nil - they
  start from the room's own spec, which already holds the document the seat was last started on. The
  runtime writes it as `<seat dir>/config.yml`, beside `mcp.json` and `hooks/pre/seat-guard.ts`, BEFORE the
  pane is launched, for `runtime: omp` seats only, and a restart's document is written into the room's
  `spec.json` before the seat starts from it - so every later heal, restore or restart installs the same
  file instead of a stale one.
- Docs: `README.md` and `docs/AGENT-SETUP.md` named `policy show|apply` and the team file. They now name
  `policy render` and say that the printed document is what the client installs. The `retiredverbs`
  package's read-set note named the parked deny patterns in `internal/clientpolicy/policy.go` as the one
  thing a repo-wide search for the retired-orchestrator class still left; that is now recorded as gone
  with the table.

### Testing

- `rust/agenthub-runtime/src/supervisor/mod.rs`, three tests on a real launch (a real tmux pane, a real
  seat directory): `the_seats_policy_document_is_written_before_the_runner_starts` (a seat whose runtime
  does not speak the runner's CLI, so the pane is gone at once and the start is refused - and the file is
  on disk anyway, byte for byte what the spec carried), `a_seat_of_another_runtime_is_given_no_config_document`
  and `a_restart_installs_the_document_it_was_given_and_records_it`.
- `rust/agenthub-runtime/src/types.rs::the_policy_document_travels_under_the_clients_own_key`: the spec and
  the `RestartSeat` frame are parsed from the JSON the client actually writes, because the key is a
  contract between two implementations and a rename on one side alone would leave every seat ungoverned
  with both suites green; the older spellings (no document, no `fresh`) still parse.
- The client's frame is pinned end to end in `internal/clientroom/room_test.go`:
  `TestRoomReseatCarriesTheHubsPolicyDocument` stands up a cloud that renders one document, points the env
  at it, and asserts the exact `StopSeat` then `RestartSeat` lines - the restart carrying
  `policy_document`, the fetch having really happened. The older
  `TestRoomReseatWritesStopSeatThenRestartSeat` now names an unreachable hub on purpose, so it pins the
  fallback (no document, the seat still comes back) rather than whatever the environment happens to serve.
- `internal/clientsync/seatpolicy_test.go` and the rewritten `internal/clientpolicy/policy_test.go`:
  the document is the hub's render plus the client's own key, a seat with no render entry gets no
  document, an unknown seat and an unreadable hub are named rather than swallowed, `--check` accepts a
  matching file and reports a drifted and a missing one, and `--team` is now an unknown flag.
- `go test ./...` in the client: green. `cargo test --offline` in `rust/agenthub-runtime`: 137 + 15 + 1
  passed, 0 failed.
- The launch smoke the ruling asks for, against the BUILT runtime binary (not the tests'): a private state
  directory, one `StartRoom` frame over its own socket carrying a document for an omp seat, with the
  `omp` binary kept off the daemon's `PATH` so no agent could start. Result: the seat never reported
  ready and its pane was gone, while `rooms/smoke/seats/dev/config.yml` held the frame's document byte for
  byte and `spec.json` carried it - the write is the supervisor's, and it cannot have come from any agent.
  The seat directory held exactly `config.yml`, `hooks` and `mcp.json`.
- Live, on the hub, for room `4genthub-min`: `policy render` prints a document for all five seats (the
  tool denies above), and `policy render --check` reports `DRIFT` for all five, naming the missing
  `config.yml` - the honest pre-install state, since nothing is installed until the owner rebuilds.

### What this does not do yet

- No seat is bound by the file. `config.yml` exists in none of the five seat directories, and it will
  exist in them only after the owner rebuilds and installs both binaries and reseats - the same owner
  action the pending guard patches are queued for. Until then every seat runs on the permissions it was
  launched with, and `--check` says `DRIFT` for each of them.
- The hub's render is taken as it is rendered: this client does not validate it. A render the hub should
  not have produced is written to the seat's own directory verbatim, and the hub's renderer is where that
  is judged.
