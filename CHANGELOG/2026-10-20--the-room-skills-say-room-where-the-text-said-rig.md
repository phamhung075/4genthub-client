## The room skills say room, where the text said rig

### Changed

- `skills/rooms/*/*/*-role/SKILL.md` (the six role skills of this tree), `skills/share/seat-worktree/SKILL.md`
  and `skills/share/safe-file-removal/` (`SKILL.md`, `EXAMPLES.md`, `TEMPLATES.md`, `VALIDATION.md`):
  **room** wherever the text said rig - "the seats your room defines", "the seat your room names for
  review", "in the room", `<room>/<seat>`, `<room>/integration`, "this room's seat policy".
  Text only; no verb, flag or placeholder name changed meaning, and the placeholders still stand for
  the same string a branch or a session is named after. Owner ruling 2026-10-20: the unit is the room.
- The server's seed library carries the same text (`agenthub_go/.../seedlibrary/seat-types/*.yaml`,
  `shared-modules/comm-guard-skill.md`, `guide-common.md`, `mcp-usage.md`) with `seedVersion` 1.4.20;
  that half commits in the hub, this half is documentation-only and rides the lead's client commit.

### Testing

- `go test ./... -count=1`: **exit 0**, 35 packages ok. The change is text, so the suite proves it
  broke nothing; the text itself is checked by `git diff` and by the hub's seed library tests,
  which assert the audited send route now reads `4genteam send <room> <seat>`.
