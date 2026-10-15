## 2026-10-15 - Seats get the agenthub MCP config

### Added
- `rust/agenthub-runtime/src/supervisor/mod.rs`: starting a seat writes `<seat dir>/mcp.json` (omp's agent dir)
  with the `agenthub_http` server. It names `${AGENTHUB_URL}/mcp` and `Bearer ${AGENTHUB_TOKEN}` only; both
  variables come from the state root's `.env` that every seat now starts with, so the file holds no secret.

### Testing
- New `the_seat_mcp_config_names_variables_and_no_secret`; `cargo test --offline` 92 passed.
- Verified live: after a reseat `go-dev` listed 9 `mcp__agenthub_http_*` tools and `manage_project list` returned 3 projects.
