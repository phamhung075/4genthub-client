## room-decision-request skill follows the decision ladder verb

### Changed

- `skills/share/room-decision-request/SKILL.md` no longer tells seats to run the retired
  `rig queue create --destination human-operator@external` / `rig queue block`. It now uses
  `4genteam decide --tier seat|lead` (exit 0 decided, 5 escalated, 1 failed, 2 usage) and one-level escalation.
- The owner step is a `room queue create` row tagged `owner-decision`; the owner answers on the dashboard.

### Testing

Skill text only. `go test ./...` unchanged. Live delivery: `4genteam sync rig`, then `seat reseat` on a real team.
