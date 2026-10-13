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

### Changed (the lead's follow-up to the gate: ownership follows the classification)
An error the runner classified but did not own used to leave the seat published as `idle` — a seat that looks
healthy and is not, which is the same lie this cutover exists to remove, and the measurement that made it
concrete was a **retryable** death that would never have resumed. `decide_resume(ours_by_id, turn_active,
has_pending_prompt, error, failures)` is now the whole rule:

1. the turn is still running -> nothing is resumed and the healthy seat is untouched;
2. otherwise, **any** error means the seat is not healthy: a terminal one publishes `blocked` with its
   classification whatever the response's id said;
3. a retryable error with a pending prompt is resumed even when the id did not line up — the mismatch is
   logged (`rpc error without a matching id; treating the pending prompt as its turn`) instead of swallowed;
4. a retryable error with no turn to resume publishes `blocked` ("a retryable failure with no turn to
   resume") rather than leaving an `idle` seat behind.

Forced on the exact measured case (a scratch room whose `omp` has no provider credential, so the response is
the id-less `No API key found for deepseek`): the pane now reads
`gave up: the agent refused the turn :: No API key found for deepseek.` and the seat's state file reads
`{"state": "blocked", "classification": "the agent refused the turn", "resume_attempts": 1}` — where before
the fix the same run left `idle` and said `nothing to resume: a response this runner did not ask for`. The
id-mismatch branch itself is unit-tested; in this run the id matched, which is recorded rather than implied.

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
- Parities 2 and 4 remain to be built.

## The relay frame and the spill frame (parity 2, per `SPEC-runner-relay-protocol-2026-10-13.md`)

### Added
- `rust/agenthub-runtime/src/frame.rs`: the spec's contract as code. `MAX_FRAME_BYTES = 131 072` bounds the
  **encoded line** — JSON escaping inflates a payload up to 6×, so a sender measures what it writes, never
  the payload. `frame_or_spill` sends the frame as it is below the limit and, at or above it, writes the
  payload to `<state dir>/rooms/<room>/[seats/<seat>/]spill/<name>.bin` and sends a `payload` frame whose
  handle carries `bytes`, `sha256`, `path`, `head`/`tail` (8 192 bytes each, for a log line and a glance)
  and `encoding`. `read_spill` verifies the digest *and* the length before a byte is trusted, and reports
  `truncated_stream` on a mismatch. `read_line_bounded` refuses to buffer a line past the limit and aborts
  with `framing_error`. `gc_spills` keeps the newest 256 MiB per room, oldest first by fixed-width
  lexicographic name, with no mtime trusted. `remove_spill_trees` is wired into
  `RoomSupervisor::stop_room`: a spill outlives no room.
- The mailbox answers now. `serve_mailbox_stream` reads bounded frames, relays each, and answers every
  frame that carried a `replyTo` on the socket the **caller** created — exactly once, `{"id","ok":true}` or
  `{"id","ok":false,"error":…}`. That is the difference between "the local write succeeded" and "the frame
  was relayed", which a mailbox write could not previously tell apart.
- The relay's verb vocabulary (`relay_frame`): `compact` (landed), `agent` (the daemon's frame written to
  the child verbatim, one line, no field added), `text` (that verb's sugar, refused in
  `PANE_INPUT_REJECTED`'s own words above 1 MiB), and `status` (the read side of the state file, so no
  caller parses a file it does not own). An unknown `type` is answered `unsupported_type` *and* still
  mirrored to the pane, because an invisible refusal is the same lie as a stub that always succeeds.
- `send_to_seat`: one sender for the daemon's frames into a seat — the spill above the limit, an optional
  reply, and `read_spill` on a `payload` answer. `request_compact` is now a call to it, so the compact path
  and every later seat-directed frame cannot drift into two conventions.

### Testing
- `cargo test` in `rust/agenthub-runtime`: **34 passed, 0 failed**. The new tests force the spec's own
  threshold: payloads whose encoded line is exactly `N-1`, `N` and `N+1` bytes — the first stays a data
  frame, the last two yield a `payload` frame whose `sha256` verifies against the file and whose
  `head`/`tail` are exactly 8 192 bytes; a spill with one byte changed is refused `truncated_stream`; a
  line past the limit with no line end is refused `framing_error` while a whole line under it is read back
  whole; GC keeps the newest within budget and removes the oldest first; a room stop removes both spill
  trees; a reply carries `ok`/`error` on the caller's socket.
- **Not exercised end to end:** no daemon→agent frame above the limit has gone through a live mailbox yet.
  `send_to_seat`'s only caller so far is `request_compact`, whose frame is small, so the spill leg is
  forced at the unit level and not yet by a live relay; `SendText` still reaches every seat through a PTY,
  and routing a native seat's text through the mailbox is the next step rather than a quiet part of this.

### Corrected
- The design's §4.2 read as if a spill could rescue a payload **omp refuses to emit**. Measured by the
  architect (`SPEC` §0): the cap lives inside the omp binary (`maxFrameBytes`, the elision machinery) and
  `pi-runner.js` contains none of its strings — so the runner cannot spill bytes omp never emits. The spill
  covers the directions where we own the bytes (daemon→agent, runner→daemon) and parity 1's retry covers
  what it cannot.

## Publishing the socket-close death (`agent_socket_closed` / `agent_stdio_eof` / `agent_exited`)

### Added
- The runner now publishes each of the three facts it alone can tell apart (`SPEC` §3): a sealed pipe from
  the writer thread (`agent_socket_closed`), the child's stdout ending (`agent_stdio_eof`), and the reaped
  child on the main thread, last writer winning (`agent_exited(<code>)`). The mailbox's answer for a frame
  that fails to relay reads the same state file, so the answer a caller gets and the file a reader finds
  agree instead of each guessing from an error string.
- `RoomSupervisor::get_room_status` reports the reason in `detail` and, when a seat's pane is gone while its
  state file still claims `idle`/`running`, **replaces that stale claim** with `agent_socket_closed` — the
  backstop that makes "not running with a reason" true even when nothing else got the chance to write.

### Testing
- **Shown failing before the fix**: `SIGKILL` on a live seat's `omp` child, then 4s later the state file still
  read `{"state":"idle"}` and the mailbox was gone — the death was published by nothing.
- **After the fix, same forcing run**: the daemon answers
  `{"seat":"dev","state":"stopped","detail":"agent_socket_closed: the pane ended without publishing a death"}`
  and the seat's file reads `{"state":"agent_socket_closed","classification":"the pane ended without
  publishing a death"}` — the two agree, and no path leaves the seat `idle` or `running` after a death.
- Measured and worth recording: in this environment the **pane's teardown kills the runner** before it can
  write its own death line (the mailbox is refused and no runner-written state appears), which is why the
  daemon-side backstop is the path that makes the record true. The death is therefore published **when the
  status is asked for**, not spontaneously — `GetRoomStatus` is what U3 calls.
- `cargo test` in `rust/agenthub-runtime`: 34 passed, 0 failed.

## Two guards on the resume path (found by the reviewer reading the code, not forced)

### Fixed
- **A terminal outcome now clears the pending prompt.** Only the success path cleared it, so after a
  `blocked` publication a later retryable error with a foreign id was treated as that prompt's turn and
  scheduled a re-send — the durable record flipping `blocked` -> `retrying` with nothing actually in flight.
- **A duplicate failure frame for the same prompt is counted once.** Two reports of one failure used to
  count as two: `resume_attempts` reached 8 while the send bound held at 4, and two independent retry
  threads were scheduled, so two copies of the same message could be in flight. The key is the pending id
  plus the error text, so a retry that fails again is a new failure and still counts — the ladder is not
  weakened, only the double count removed.

### Testing
- `cargo test` in `rust/agenthub-runtime`: 34 passed, 0 failed. **Not forced, and said so**: both guards were
  found by the reviewer reading this code, and reachability of the first needs a terminal error followed by
  a retryable one, which neither of us could force with the frames available. The reviewer's B1s harness
  frames are the place to try; nothing here is claimed as verified beyond compilation and the suite.

### Known gaps (updated)
- **Respawn is not built** (deliberately out of scope, per the row): after a transport death the pinned
  prompt is re-sent down a dead channel, so parity 1's bounded ladder covers the **error-frame class**, not
  channel deaths.
- The lab found a separate defect in the room rail: `handed_off_to` is **terminal** on the runtime's queue, so
  the row's successor can neither claim nor close it and `destination_session` keeps pointing at the seat
  that handed it off. The lead→worker→reviewer flow therefore completes hops 1-3 (`pending` → `in-progress`
  → `handed-off`, transitions recorded) and cannot complete hop 4.
- Parity 2's remaining half: routing a native seat's `SendText` through the mailbox, and the chunk/stream
  reader (`seq`/`eof`, `truncated_stream` on a gap) are not built.
