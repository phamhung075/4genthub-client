# An agy seat mounts the agenthub MCP server

`start_seat` writes the seat's `mcp.json` for EVERY runtime (`rust/agenthub-runtime/src/supervisor/mod.rs`,
the document `SEAT_MCP_CONFIG`), and that file is omp's own convention for the agent dir it is handed. agy reads
its servers from its own `~/.gemini/config/mcp_config.json`, and its argv is only `agy --model '<X>'` - nothing
in it names that file - so the runtime installed nothing there: measured 2026-10-15 on the agy run (hub
`d143f23b`), the live pane child was `agy --input-format stream-json --output-format stream-json
--dangerously-skip-permissions --model gemini-3.8-flash-low` and that session had NO agenthub_http tools. An
agy seat could not call `manage_task`, `manage_context`, `call_seat` or the queue from inside its own runtime,
which is the point of seating one.

### Fixed

- **The agy arm installs the seat's MCP server in agy's OWN config**, beside the guard hook it already installed
  there: `install_agy_start_files` is the hook plus `install_agy_mcp_server`, called from the same `Agy` block
  of `start_seat`. It is a command, not a `url` entry, because agy's HTTP transport (`serverUrl` + `headers`)
  would need the seat's token written INTO a file agy applies to every session on the machine.
- **The entry reads the seat's environment at connect time and starts nothing outside a seat.** agy spawns its
  own environment with every server it starts, so the line is
  `if [ -n "$AGENTHUB_SEAT" ]; then exec mcp-proxy --transport streamablehttp --headers Accept '…'
  --headers Authorization "Bearer $AGENTHUB_TOKEN" "$AGENTHUB_URL/mcp"; fi` - the token and the URL are
  variables the seat already has, never a copy in the file, and a session that is not a seat starts no bridge.
- **The file is merged, never rewritten, and a file it cannot merge into is refused by name.** Another tool's
  servers survive (measured against the owner's own `deepseek` stdio entry), a second install does not rewrite
  the file, and a file that is not a JSON object - or an `mcpServers` that is not an object - is an error rather
  than a file to overwrite, the rule the guard hook's install already follows.

### Testing

- `cargo test --offline` in `rust/agenthub-runtime`: 145 unit + 15 `seat_guard` + 1 `umask` = **161 passed, 0
  failed**. New: `supervisor::tests::the_agy_mcp_server_joins_the_global_file_and_leaves_the_rest` (the entry's
  shape, the other servers kept, idempotence, and both refusals) and
  `an_agy_start_installs_the_guard_hook_and_the_mcp_server` (the pairing).
- **RED FIRST, measured on the base implementation (`f5f102d`) with ONLY the tests added: 6 compile errors, all
  of them missing new symbols** (4 × `cannot find function install_agy_mcp_server`, 1 ×
  `install_agy_start_files`, 1 × `cannot find value AGY_MCP_SERVER`).
- **MUTATIONS on the fixed tree, each caught by a named assertion**: dropping `disabled: false` → the
  `Null`/`false` assertion; `--transport sse` → the transport assertion; `if true` for the seat check → the
  seat-check assertion; and removing `install_agy_mcp_server` from `install_agy_start_files` → the pairing test.
- **SMOKE, both halves, with the string the code itself builds** (extracted from the source): `HOME=<scratch>
  agy mcp list` reads the entry back as `agenthub_http  stdio  enabled  sh -c …`, and the line itself, run with
  this seat's own environment, answered an MCP `initialize` with `agenthub - AI Orchestration Platform 1.22.0`
  and `tools/list` with the NINE agenthub tools (`manage_task` … `manage_connection`) - the bridge reaches the
  cloud as the seat that started it.
- `go vet ./...` rc 0; `gofmt -l internal/ cmd/` clean; `go test -count=1 ./...` = 32 packages ok, 0 FAIL.

### What this does not do

- It does not prove a LIVE agy session calling a tool: agy must hand its environment to the process it spawns
  and speak stdio to it, which only a live agy seat can confirm (the task's AC1, waiting on the owner's agy seat).
- It changes no omp or claude-code seat: their `mcp.json`, their argv and their start-time files are untouched,
  and the install sits inside the `Agy` arm.
- The `Agy` arm's one-line call is not covered by a test of its own - covering it means starting a real agy pane
  inside the suite. What is pinned is the pair `install_agy_start_files` installs, and that arm is its only caller.
