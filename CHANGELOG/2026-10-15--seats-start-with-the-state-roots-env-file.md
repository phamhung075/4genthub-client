## 2026-10-15 - Seats start with the state root's `.env`

### Changed
- `rust/agenthub-runtime/src/supervisor/mod.rs`: `seat_env` now starts from `<state root>/.env`
  (`~/.config/4genthub/.env`), so every seat holds `AGENTHUB_MCP_URL`, `AGENTHUB_TOKEN` and the provider keys
  without a copy. Precedence: the runtime's `AGENTHUB_*` keys, then the seat spec's `env`, then the file.
  The parser (`env_file_vars`) takes `KEY=VALUE`, an `export ` prefix, one pair of quotes, skips comments, and
  never logs a value. A missing file adds nothing.

### Testing
- New `a_seats_env_carries_the_state_roots_env_file`; `cargo test --offline` 91 passed, `go vet`, `go test ./...` clean.
- A running seat never re-reads its environment: reseat to pick this up.
