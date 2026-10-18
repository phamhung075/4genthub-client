## Every seat's MCP file names the deepseek server from the environment, and claude gets one at all

### Changed

- `rust/agenthub-runtime/src/supervisor/mod.rs`: the seat's `mcp.json` is built by `seat_mcp_config` from
  the seat's own environment instead of written from the constant. With no `DEEPSEEK_MCP_SERVER` the file
  is `SEAT_MCP_CONFIG` verbatim, so a machine that configures nothing writes exactly today's file; with one
  named, a `deepseek` entry runs that script (`node` + the path) with `DEEPSEEK_MCP_DEFAULT_CWD` = the
  seat's own first scope directory, `DEEPSEEK_WORKSPACE_ATTACH=1` and `DEEPSEEK_MCP_PERMISSION=allow`. The
  cloud entry is unchanged and still references the token, never holding it.
- `seat_env` gained the seat's `cwd` and now carries `DEEPSEEK_MCP_DEFAULT_CWD` as a runtime-owned key (a
  value in the `.env` does not win: one daemon starts every seat of every room, so an inherited directory
  would hand one room's tree to another room's job).
- `rust/agenthub-runtime/src/runner/mod.rs`: a claude-code seat's argv gained `--mcp-config
  <seat dir>/mcp.json`. claude reads no server from the seat dir without it, so claude seats had neither
  `agenthub_http` nor a delegation server.
- agy: the runtime now owns a second entry, `deepseek`, in its global `mcp_config.json`, written in the
  same form as the cloud bridge - `sh -c 'if [ -n "$AGENTHUB_SEAT" ] && [ -n "$DEEPSEEK_MCP_SERVER" ];
  then exec node "$DEEPSEEK_MCP_SERVER"; fi'`. The path is read at connect time, so the shared file holds
  none. When the environment names no script the entry is REMOVED, which is also what clears a stale
  hardcoded path from that file (the owner's file carried one, pointing at an unrelated project). Other
  servers in the file are untouched.
- `skills/share/deepseek-delegation/SKILL.md`, new skill: every seat gets it installed by
  `install_seat_skills`, and it carries what to offload to `deepseek_agent`, what never to (a decision, a
  commit, a message, secrets), how the job's directory is decided, and that the job's writes are guarded
  and not trusted.
- `.env.sample` and `docs/AGENT-SETUP.md` name `DEEPSEEK_MCP_SERVER` as the switch: blank means no entry,
  the job's directory is always the seat's scope, and the sample holds a commented example rather than a
  path.

### Testing

- `cargo test` in `rust/agenthub-runtime` (see the run in the handoff): the two new supervisor tests pin
  that absence writes the constant byte for byte and a blank value counts as absence, that a named script
  lands with the seat's scope, and that the agy entry appears and disappears with the environment while
  other servers survive; `claude_argv_is_the_streaming_channel_with_the_seats_posture_and_model` pins the
  `--mcp-config` pair. All pre-existing tests in the crate pass.
