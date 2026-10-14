## A `claude-code` seat runs on the native runner

### Added

- `rust/agenthub-runtime/src/runner/claude.rs`: translates the runner's agent protocol (`prompt`, `abort`, `compact`,
  `get_state` in; `ready`, `response`, `turn_start`, `message_update`, `turn_end` out) to and from
  `claude -p --input-format stream-json --output-format stream-json --verbose --include-partial-messages`.
  The handshake, retry ladder, state file and mailbox stay the one implementation for both runtimes.
- `compact` is the `/compact` user message; `abort` is the stream-json `interrupt` request, and the turn it ends
  answers success so a cancel never starts the retry ladder.
- `AgentRuntime::has_runner()`: omp and claude-code seats run the runner in their pane and publish the state file.

### Changed

- The supervisor no longer launches `claude --dangerously-skip-permissions` bare in the pane; a claude-code seat
  runs the runner like an omp seat. Permission policy maps `yolo` to `--permission-mode bypassPermissions` and the
  restrictive words to `default`.
- A claude-code seat's readiness is the runner's state file, but `claude` prints nothing before its first turn, so
  `ready` is synthesized at spawn. The status evidence says so: it proves the pipe, not that the model answered.

### Tested
`cargo test` (85 pass, 9 new); live on an isolated daemon and socket: `up` -> running, a raw prompt answered
`PONG`, `room compact` relayed and ended its turn, `room stop`. The lab was removed afterwards.
