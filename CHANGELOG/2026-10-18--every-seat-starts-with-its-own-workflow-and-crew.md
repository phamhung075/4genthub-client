## Every seat starts with its own workflow and its DeepSeek crew

The seats had role skills but never used them for method: omp loads a skill only when its description
matches the turn, and the `<seat>-role` skills describe a contract, not when to read them. Each seat in
`4genthub-min` now has a workflow, and the runtime puts it in the seat's context at every start.

### Added

- `skills/share/seat-crew`: the shared crew protocol. Each seat leads its own team of DeepSeek workers
  through `deepseek-offload`: when to start one, path A (`deepseek_agent`) or path B (`dsh-offload.mjs`,
  background), the six-part work order, at most four at once, off-peak, the job store in the seat dir
  (`DSH_OFFLOAD_JOB_DIR`), and that the seat re-verifies every result.
- `skills/rooms/4genthub-min/<seat>/<seat>-workflow` for lead, architect, go-dev, fe-dev and reviewer:
  the job, the session start (`4genteam whoami`/`facts`, the queue, `manage_task`), the loop, the crew's
  limits (read-only for lead, architect and reviewer; write workers on disjoint files in their own worktree
  for go-dev and fe-dev), and what not to do.
- `rust/agenthub-runtime/src/supervisor/mod.rs` `install_seat_skills`: writes the `<seat>-workflow` body to
  `<seat dir>/AGENTS.md`, which omp loads from its agent dir; removes a stale one when the seat has no
  workflow.
- `rust/agenthub-runtime/src/runner/mod.rs` `child_argv`: a claude seat gets
  `--append-system-prompt-file <seat dir>/AGENTS.md` when the file exists.
- `docs/AGENT-SETUP.md`: the paragraph on the always-loaded workflow.

### Not covered

- `agy` seats (the reviewer): how agy loads a context file is unverified, so the reviewer reads its
  workflow as an on-demand skill only.
- The lead has no `deepseek` MCP entry, so its crew is path B only (the workflow says so).

### Tests

`go test ./...` and `cargo test --offline` green (181 runtime tests). New:
`a_seats_workflow_skill_becomes_its_always_loaded_agents_file`,
`claude_argv_appends_the_seats_workflow_when_it_exists`. The role-skill landmark test is unchanged and
green: the role skills were not edited. Live seats take the change on `4genteam room reseat 4genthub-min <seat>`.
