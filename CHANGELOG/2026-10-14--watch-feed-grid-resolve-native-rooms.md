## watch, feed, grid, input resolve native rooms; seat naming is the runtime's

### Fixed

- `4genteam feed|watch|grid|input|inputs --rig <room>` answered "no such room" for a room started by `4genteam up`.
  `clientwatch.requireRoomAndSeats` looked under the cloud seat store (`<store>/<room>/<seat>`) instead of the
  room's own layout `<state root>/rooms/<room>/seats/<seat>`. It now checks the native layout and the seat store is no
  longer consulted by this package.
- Hand-built `<rig>-<seat>@<rig>` names are replaced by `clientruntime.SeatSessionName` / `ParseSeatSessionName` in
  `seatlog` (`RigSeats`, the Claude process match), `clientrestore` (`rigSessions`, `rigSeats`, the brief target) and the
  `clientlifecycle` supervisor (say, abort, interrupt, the forcecompact argument).

### Changed

- `clientruntime.SeatDir` is the native seat directory, which is also its `PI_CODING_AGENT_DIR`. `seatlog.SeatLog`,
  skill delivery (`clientseat`) and `clientpolicy.ConfigPath` read `<seat dir>/{sessions,skills,config.yml}`, not
  `<root>/<rig>-<seat>@<rig>/agent/...`.
- Not changed: `CanonicalRigSeat` and the send translation still accept the documented `<rig>-<seat>@<rig>` input form;
  `clientsync` seat-store commands (`sync`, `pull`, `bundle`, `withhold`) legitimately use the cloud seat store. The
  forcecompact helper is documented as targeting an 4genteam `pi-runner.js` seat; whether it works on a native seat was not measured.

### Tested
`TestTheExistenceCheckReadsTheNativeRoomLayout` (clientwatch) plus fixtures moved to native names in `clientrestore`,
`clientlifecycle`, `clientseat`, `seatlog`. The new test fails to compile on the previous code (the old
`requireRoomAndSeats` signature and `seatStore`). `go build ./... && go vet ./... && go test ./...` pass.
