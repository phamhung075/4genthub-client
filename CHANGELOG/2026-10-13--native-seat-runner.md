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

### Changed (parity 1 — the defect the cutover exists for)
- A failure this runner sees is now classified before anything is done about it: `classify_failure`
  matches the signatures the finding measured (`RPC response exceeded the transport limit`,
  `rpc chunk payload exceeds the transport limit`, `The socket connection was closed unexpectedly`) plus
  the provider classes (rate limit, overloaded, timeout, reset, fetch failed) and calls those
  `Retryable`; everything else is `Terminal`. **`needs_input` is never derived from an error** — the
  defect being replaced maps omp's `Notification`/`runtime_error` to `needs_input`
  (`agent-activity-store.js:262-265`), and `needs_input` is the state that fails closed.
- A turn that ended on a retryable failure **auto-resumes with the same message**, bounded to 3 attempts
  with 1s/2s/4s backoff, then publishes `blocked` with the classification. The decision is the pure
  `decide_resume(ours, turn_active, error, failures_so_far)`, checked **in that order**: the finding
  measured that an rpc error does **not** always kill the turn, so an error seen while the turn is still
  running resumes nothing (resuming there would double the work), and a response this runner did not ask
  for is never resumed either.
- `rooms/<room>/run/<seat>.state.json` — the seat's own state, published atomically (temp + rename) with
  `state` (`idle`/`retrying`/`needs_input`/`blocked`), the classification, the resume count, and a `Z`
  timestamp: `{state, classification, resume_attempts, updated_at}`. The reference writes a
  `runner-state.json` beside its session for the same reason (`pi-runner-protocol.js:26`). This is what
  parity 4's daemon-side `last_active`/alerting will read.
- `needs_input` is now reserved for a real prompt: an `extension_ui_request` sets it, and the dialog is
  answered `{"type":"extension_ui_response","cancelled":true}` because a pane whose input is a prompt
  cannot answer a dialog — the reference cancels what it cannot present (`pi-runner.js:434-438`).
  **Named limitation:** an auto-cancelled dialog can deny something the agent asked for (in the measured
  runs, omp's advisor-cost prompt); nothing yet escalates a cancelled dialog to a human. That is
  `needs_input`'s missing consumer, not something this change papers over.
- omp's own `auto_retry_start`/`auto_retry_end` are logged and published but **not** stacked with a
  second retry: if omp's retry succeeds the prompt response is a success and nothing resumes.
- A prompt's `id` is unique per pane line and remembered, so a response is correlated with the message it
  belongs to and a resume re-sends exactly that message.

### Design errata carried here (architect seat wedged; the lead asked for these in the change record)
1. §1 says the reference's runner owns a **socketpair**; the measured reference uses plain pipes on
   fd 0/1/2 (`pi-runner.js:818-822`). Ownership is the property that matters.
2. §1's table says the force-compact path reaches the agent as a `{"type":"compact"}` frame relayed by
   the runner. No `{"type":"compact"}` exists anywhere in the OpenRig package; the frame is **ours**
   (omp does understand it — measured: `Nothing to compact (session too small)`).
3. §1's table attributes the channel theft (pidfd_getfd) to the daemon's handling of the reference
   runner. Measured: `pidfd_getfd` exists only in **this repo's** `context/mod.rs` — the theft is ours,
   not the reference's, which never touches its runner's descriptors. That code is removed here.
4. §1's table says transcripts are captured with `tmux pipe-pane`; the measured reference rotates
   `capture-pane` (`transcript-store.js:195-196`, `node-launcher.js:97-107`).
5. §2's named defect "the `--state-dir` default is not expanded" is already closed in this tree:
   `main.rs` runs the default through `shellexpand`.
6. The design's forcing mechanism for parity 1 assumes a provider failure can be induced on demand. In
   this seat's environment it cannot: `omp` exposes no general provider base-URL override and no
   Anthropic models are catalogued, so a provider stub inside a scratch room is required for that
   end-to-end leg (see the gap below). The classification itself is tested against the measured
   literals.

### Testing
- `cargo build` and `cargo test` in `rust/agenthub-runtime`: 27 passed, 0 failed. The new ones: the
  measured failure literals classify retryable and a refusal does not; the mid-turn error resumes
  nothing while the same error on an ended turn resumes with backoff; the resume schedule is bounded;
  the state file publishes atomically with a `Z` timestamp and no leftover temp file.
- Against **the real `omp`** in a scratch room (`/tmp/u2-p1`), rebuilt binary: a bogus model
  (`deepseek/definitely-not-a-model-xyz`) made the runner report `Model "…" not found` and exit 1 with
  `the child's stdout ended before the ready frame` — a startup failure is named, never a silent
  success. With a real model, a turn ran to `session_settled`; a real `extension_ui_request` arrived
  mid-turn, was cancelled with the response frame, and the turn continued to completion; the state file
  was published with `updated_at` in `Z` form. A second pane prompt sent during a running turn produced
  no rpc error at all (omp queues it), so it did not exercise the resume path — recorded rather than
  claimed.
- **Not exercised end to end:** the resume path against a *real* provider/transport failure. Its rule is
  unit-tested and its literals are the measured ones, but no mechanism available in this environment can
  make a live provider fail on demand (see erratum 6).

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

### Testing (stage 1: the runner hosts the pane)
- `cargo build` and `cargo test` in `rust/agenthub-runtime`: 27 passed, 0 failed.
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
- **Parity 1's resume path is not forced end to end against a real provider failure** — no provider stub
  is reachable in this seat's environment (erratum 6). The rule and the literals are tested; the live
  leg needs a scratch room with a stub provider.
- A cancelled dialog can deny what the agent asked for, with nothing yet escalating it to a human (see
  parity 1's named limitation).
- Only `omp` has a native rpc path; `claude-code`/`agy`/`codex` seats keep their own pane command.
- The runner publishes its state file but does not yet *report* activity/idle to the daemon (parity 4's
  `last_active`), and the reference's HTTP activity hook is deliberately not ported.
- Parities 2 (spill oversized payloads) and 4 remain to be built.
