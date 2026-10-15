## 2026-10-15 - `4genteam heal`, and skills installed into every seat

### Added
- `internal/clientheal` and the verb `4genteam heal ROOM [--check] [--json]`: checks one room, whatever its project, for the faults that stop a team working, fixes each one that is safe and re-checks it. Checks: the state root `.env` holds `AGENTHUB_URL` and `AGENTHUB_TOKEN` (presence only, never a value; left for the owner), a seat is running (reseat), a running seat holds both keys (reseat), a seat has its `mcp.json` (reseat), every safeguard runs (`up`, and the last refusal from `alerts.log` is named when it stays down). Exit 0 when clean or fully fixed, 3 when something is left.
- `rust/agenthub-runtime/src/supervisor/mod.rs`: starting a seat installs its skills into `<seat dir>/skills/<name>` from the tree named by `AGENTHUB_CLIENT_SKILLS` (state root `.env`, else the daemon's environment): `share/`, `rooms/<room>/` and the nested `rooms/<room>/<seat>/`, the more specific replacing a shared skill, every file of a skill copied. The directory is rebuilt each start. Before this the runtime's skill resolver was never called, so no live seat held any skill.

### Testing
- `internal/clientheal/heal_test.go` (6 tests), `a_seat_gets_its_nested_skills_and_not_another_seats`; `go vet ./...`, `go test ./...`, `cargo test --offline` (93 passed).
