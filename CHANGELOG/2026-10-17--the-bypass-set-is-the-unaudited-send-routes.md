# The bypass set is the unaudited send routes

`commpolicy.BypassSuspected` answers "did this observed command reach a seat by a route that never met the
audited send path". It flagged `4genteam send` and `4genteam room queue create` as bypasses, which is the
opposite of what both are: `send` is the audited path (it builds the sender's policy, writes the decision
line, delivers, writes the outcome line), and the rail's `create` records a row the daemon owns.

### Changed

- **The set is the unaudited routes and nothing else**: `4genteam input` (raw pane input), `tmux
  send-keys`, `tmux paste-buffer`, and the foreign `rig send|queue|broadcast` spellings. The `4genteam`
  branch is one comparison now - `sub == "input"` - and the rail's `create` case is gone.
- The doc comment names the audited path's file and says why the retired `rig` spellings stay.

### Testing

- Red first: the expectations were flipped before `policy.go` was touched, and `TestBypassSuspected`,
  `TestBypassSuspectedKnownShapeIsNotALicence` and `TestBypassSuspectedNilKnown` failed on the old set.
- `go test ./...`: green; `gofmt -l` empty.
- MEASURED, a built `seatcheck audit-scan --file` over a file holding `4genteam send <session>`,
  `4genteam input --seat`, `4genteam room queue create`, `tmux send-keys` and `rig send`: it reports the
  last three only, exit 4 (`bypass-suspected: 4genteam input ...`, `... tmux send-keys ...`, `... rig send
  ...`).

### Supersedes

- `CHANGELOG/2026-10-15--the-comm-guard-binds-the-native-spellings.md`: it added `4genteam send` and the
  rail's `create` to the bypass set on the premise that they skipped the checker. That premise held while
  `seatcheck send` existed; the checker's send verb is deleted, the surviving route is `4genteam send`, and
  the rail's `create` is the audited handoff rather than a message - so both entries go, and only
  `4genteam input` (with `tmux send-keys|paste-buffer` and the foreign `rig` spellings) stays.
