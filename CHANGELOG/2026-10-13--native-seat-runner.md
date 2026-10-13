## `agenthub-runtime runner`: the seat's pane is a native process that owns its agent's channel

### Added
- `rust/agenthub-runtime/src/runner/mod.rs` — `agenthub-runtime runner <room>/<seat>`, the tmux pane's
  foreground process (`Commands::Runner` in `src/main.rs`). It resolves the seat from
  `rooms/<room>/spec.json`, spawns its agent in RPC mode, and owns the only channel to it: newline-JSON
  over the child's stdin/stdout, exactly the reference's transport (`pi-runner.js:818-831`, `:872`).
  Child argv is the reference's (`pi-runner-protocol.js:220-242`): `--mode rpc --session-dir
  <seat dir>/sessions --approval-mode yolo|always-ask [--model M]`; the child inherits the seat's
  identity because the supervisor already exported `AGENTHUB_*` into the pane, so there is no second env
  copy and no `OPENRIG_*` family. A runtime with no native rpc path is refused by name.
- `rooms/<room>/run/<seat>.sock` — the seat's mailbox, where the daemon hands the runner a frame it must
  relay (today: `{"type":"compact"}`). `runner::request_compact` is what the IPC `ForceCompact` verb and
  `agenthub-runtime compact <seat>` now call.
- Pane behaviour from the reference: the child's stdout is split into responses (correlated by `id`) and
  events; events are mirrored, and assistant prose (`message_update` → `assistantMessageEvent.delta`) is
  written as text, because that is the one payload an operator reads rather than a name
  (`pi-runner.js:204-208, 832-833`). rpc errors mirror in the reference's voice (`[omp-runner] ERROR
  rpc: …`, `:362-375`). Pane input becomes a `prompt` frame, an empty line or Ctrl-C becomes `abort`,
  and input over 1 MiB is refused with the reference's own wording (`:34-36`).
- `RoomSupervisor::state_dir()` for the IPC layer's relay, and room startup wires the runner:
  `start_seat` launches `<this binary> --state-dir <abs> runner <room>/<seat>` in the pane for an omp
  seat instead of `omp` directly (design §1's table).

### Removed
- The `pidfd_getfd` compaction path (`src/context/mod.rs`, 94 lines: the `/proc` + `ss` fd hunt, the raw
  `SYS_PIDFD_OPEN`/`SYS_PIDFD_GETFD` syscalls, `CAP_SYS_PTRACE`). The runner owns the channel, so the
  daemon asks the runner instead of stealing a descriptor; a seat with no runner is a refusal naming the
  seat, not a silent success.

### Changed
- The design's §1 describes the reference as owning a "socketpair"; the measured reference uses plain
  pipes on fd 0/1/2 (`pi-runner.js:818-822`). This module uses pipes and records the deviation: ownership
  of the channel is the property that matters, and inventing a socketpair would have matched the
  sentence, not the code.

### Testing
- `cargo build` and `cargo test` in `rust/agenthub-runtime`: 23 passed, 0 failed (4 new: target parsing,
  the omp argv, a non-rpc runtime refused by name, the mailbox path).
- Against **the real `omp`** in a scratch room (`/tmp/u2-runner`): the runner spawned
  `omp --mode rpc --session-dir /tmp/u2-runner/state/rooms/roomA/seats/dev/sessions --approval-mode yolo
  --model deepseek/deepseek-flash:high`; the transport `ready` frame was consumed and the `get_state`
  handshake succeeded; a line typed into the pane produced a full turn
  (`agent_start → turn_start → message_* → turn_end → agent_end → prompt_result → session_settled`); a
  frame written into the mailbox was relayed and `omp` answered it as a domain error —
  `[omp-runner] ERROR rpc: Nothing to compact (session too small)` — which settles that the native
  `{"type":"compact"}` frame is one `omp` understands.
- The `up`-shaped run over IPC (`/tmp/u2-up`): `StartRoom` returned the seat `running`; the tmux pane's
  process was `<binary> --state-dir /tmp/u2-up/state runner roomA/dev` and **its** child was
  `omp --mode rpc …` (ppid = the runner), so no `pi-runner.js` is in the room's tree;
  `GetRoomStatus` reported `dev running`; `ForceCompact` answered
  `roomA/dev: compact relayed to its runner (id forcecompact-8985)` and the pane showed the relay and
  omp's answer; `ForceCompact` for a seat with no runner was refused naming the seat and its mailbox;
  `StopRoom` left no tmux session and no runner process.

### Known gaps (the next unit, not silently skipped)
- Parity 1's classification is not wired yet: an omp `runtime_error`/`auto_retry_*` event is mirrored to
  the pane but does not yet set the agent's own retryable state, which is the defect the cutover exists
  to fix (`agent-activity-store.js:262-265` maps it to `needs_input` — not inherited here, but not yet
  replaced by the retryable state either).
- An `extension_ui_request` that a human cannot answer is mirrored, not answered; the reference cancels
  the unanswerable ones automatically (`pi-runner.js:434-438`).
- Only `omp` has a native rpc path; `claude-code`/`agy`/`codex` seats keep their own pane command.
- The runner does not yet report activity/idle to the daemon (parity 4's `last_active`), and the
  reference's HTTP activity hook is deliberately not ported.
