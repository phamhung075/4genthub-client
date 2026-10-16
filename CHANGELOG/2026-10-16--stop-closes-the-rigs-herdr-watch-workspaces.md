# `stop` and `room stop` close the rig's herdr watch workspaces

### Fixed

- **`4genteam stop RIG` and `4genteam room stop ROOM` left the `feed --room R --seat S` and `input --room R --seat S`
  helpers running (7-8 orphans each time).** Those helpers are started by `grid` and `watch` in the panes of the
  herdr workspaces labelled `<rig> grid` and `<rig> lead`, and they poll forever; neither stop touched herdr, so
  the panes and their helpers outlived the room. Both verbs now call `clientwatch.CloseRigWorkspaces`, which closes
  those two workspaces (only that rig's; nothing when herdr is not installed). A herdr error is reported on stderr
  and does not fail the stop.
- Files: `internal/clientwatch/grid.go`, `internal/clientlifecycle/lifecycle.go`, `internal/clientroom/room.go`.
- Tests: `close_test.go` in each of the three packages (fake herdr: only that rig's grid and lead are closed; both
  stop verbs call the close). Not proven against the live room, which cannot be stopped for it.
