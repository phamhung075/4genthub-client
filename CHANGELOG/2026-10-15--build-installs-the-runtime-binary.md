## 2026-10-15 - `scripts/build.sh` installs the runtime binary

### Fixed
- `scripts/build.sh` built and installed only `4genteam`, so a change under `rust/agenthub-runtime` never reached the machine: the daemon kept running the binary of 2026-10-14, and the seat env, `mcp.json` and skill install shipped earlier today were not live. It now runs `cargo build --release --offline` and installs `~/.local/bin/agenthub-runtime` the same way (built beside, renamed). The daemon must be restarted to run it (`4genteam up ROOM --team DIR` starts one).
