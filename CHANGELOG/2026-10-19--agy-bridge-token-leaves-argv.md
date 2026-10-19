# The agy bridge carries the seat token in its environment, never in argv

The agy arm's MCP entry (board fd12b946) ran
`exec mcp-proxy --transport streamablehttp --headers Authorization "Bearer $AGENTHUB_TOKEN" "$AGENTHUB_URL/mcp"`
inside `sh -c`. The shell expands the token into `mcp-proxy`'s ARGV, and `/proc/<pid>/cmdline` is
world-readable while `/proc/<pid>/environ` is owner-only: the seat credential was published to every local
user's `ps`. Measured on the live room 2026-10-19 04:55Z, the reviewer seat's bridge (pid 19859, spawned by
agy pid 19835) carried the full bearer JWT in its command line. The omp/claude paths were never affected -
their `mcp.json` names url+headers, so the token stays in the seat's environment.

## Fixed

- **`AGY_MCP_COMMAND` hands the token over in the bridge's ENVIRONMENT.** `mcp-proxy` 0.10.0 reads
  `API_ACCESS_TOKEN` from its own environment and sets `Authorization: Bearer <it>` itself
  (`mcp_proxy/__main__.py`, the `streamablehttp` client path), so the line is now
  `if [ -n "$AGENTHUB_SEAT" ]; then API_ACCESS_TOKEN="$AGENTHUB_TOKEN" exec mcp-proxy --transport
  streamablehttp --headers Accept 'application/json, text/event-stream' "$AGENTHUB_URL/mcp"; fi`. The
  assignment prefixes `exec`, so it lands in the child's environment and never in its argv; the bridge's
  command line is the transport, the Accept header and the URL. The seat guard (nothing starts outside a
  seat) and the stdio shape agy parses are unchanged, and the omp/claude install sites are untouched.
- `install_agy_mcp_server` compares the entry by value, so the next seat start rewrites the old line.

## Testing

- `cargo test --offline` in `rust/agenthub-runtime`: **208 passed, 0 failed** (187 unit + 1 + 19 + 1).
- New `supervisor::tests::the_agy_bridge_argv_carries_no_credential` runs the installed `sh -c` line with a
  stand-in `mcp-proxy` on PATH that records its argv and whether the token arrived in its environment: the
  argv holds `--transport streamablehttp`, the Accept header and the URL, no `Bearer`, and the token string
  appears nowhere in it, while the environment carries it.
- **RED FIRST, measured with only the constant reverted to the old line and the new tests in place:** both
  `the_agy_bridge_argv_carries_no_credential` and `the_agy_mcp_server_joins_the_global_file_and_leaves_the_rest`
  FAILED (`the credential is in the bridge's argv: --transport / Authorization`, and the shape assertion),
  and both pass on the fixed line.
- **SMOKE with the real `mcp-proxy` 0.10.0 and the string the code builds**, against a local recorder:
  the request arrived with `Authorization: Bearer <smoke value>` and the proxy's `/proc/<pid>/cmdline` held
  `--transport streamablehttp --headers Accept application/json, text/event-stream http://.../mcp` with the
  token count 0; its environment held `API_ACCESS_TOKEN`. The only bypassed half is agy's own spawn: a real
  agy seat listing the nine tools after a reseat is the lead's post-pin measurement.
- **NINE TOOLS through the installed shape against the live hub**, with the seat's token inherited in the
  environment (never printed): `initialize` answered `agenthub - AI Orchestration Platform 1.22.0`, and
  `tools/list` returned **9** - `call_seat`, `manage_connection`, `manage_context`, `manage_git_branch`,
  `manage_project`, `manage_seat`, `manage_subtask`, `manage_task`, `submit_feedback`.
- No token value is recorded here, in the commit, or in the test: the assertions are presence/absence.
