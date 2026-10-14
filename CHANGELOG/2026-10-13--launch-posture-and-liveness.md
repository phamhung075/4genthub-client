## The launch posture, and liveness as something proven rather than claimed

Fixes both defects the reviewer's gate raised on `3546694` (`qitem-20261013202633-9000ae7fbb75b0fe`,
routed to go-dev as `qitem-20261013203245-0c2808c5fc72f82e`).

### Fixed — the permission vocabulary was mapped backwards
`permission_policy` was compared against a word this module had invented (`"approve"`), so the posture the
repo intends arrived inverted: a seat carrying `yolo`/`builtin:yolo` was launched `--approval-mode
always-ask`, which is the one posture that parks a headless seat. The mapping now comes from the repo's own
authority rather than a guess:

- the vocabulary is the operator's (`docs/AGENT-SETUP.md:389`: `locked|standard|open|yolo|none`, where
  "`yolo` means the seat runs unattended with no approval prompts");
- the value the client actually materialises is `builtin:yolo` (`internal/clientsync/rigverb.go:62`),
  whose comment gives the reason — without a full-bypass launch "omp's always-ask floor cancels approvals
  headlessly and the seat parks";
- `internal/clientpolicy/policy.go` states the same shape from the runner's side: "OpenRig launches an omp
  seat in exactly one of two postures - yolo, or always-ask which the managed runner auto-cancels". The
  auto-cancel half is this runner's own dialog handling and was measured in the run below.

`runner::approval_mode` is the whole table, and it is the only spelling of it:

| `permission_policy` | `--approval-mode` |
|---|---|
| `yolo` | `yolo` |
| `builtin:yolo` (what the client materialises) | `yolo` |
| *unspecified* (what the live rig's seats carry) | `yolo` |
| `locked` | `always-ask` |
| `standard` | `always-ask` |
| `open` | `always-ask` |
| `none` | `always-ask` |
| anything else | **refused by name** |

An undefined word is refused rather than guessed: guessing is what inverted this in the first place.

### Fixed — `state=running` was a launch-time claim, not a liveness one
`start_seat` returned `Running` the moment tmux accepted the session, and `start_room` **swallowed** a
per-seat failure entirely — a seat that never came up produced no row at all, in a list that then read as
success. Reproduced before the fix: a seat whose runtime command does not exist, a seat whose agent dies at
startup, and a seat whose policy word is undefined each came back `ok` from `StartRoom`, with
`GetRoomStatus` showing `dev=stopped`. Design §6.2 point 5 says a partial start never exits 0, and that was
not buildable on this status.

- A seat must now **prove** it is up inside a named window (`SEAT_READY_WINDOW_SECS` = 20s, for an omp
  seat): the signal is the seat's own state file (`runner::state_file_path`), which the runner publishes
  only after the agent answered the transport handshake. The runtimes without a runner have no handshake to
  wait for, so their signal is the session still existing after `NON_OMP_READY_GRACE_MS` (1500ms) — stated
  as the weaker thing it is, not passed off as handshake proof.
- A seat that does not prove itself is an error naming it, and `start_room` returns a status for **every**
  seat: a failed one carries `SeatLifecycleState::Dead`, `detail` with the reason, and no pid. A room where
  no seat came up is an `Err`, not an `Ok` with an empty list, and it is not made an active room — a room
  with nothing running has no rail to answer queue verbs from.
- `SeatStatus.pid` is now documented for what it is: the pid tmux returned when it created the session, a
  **record**, not proof of liveness. Liveness is the state file (or, for other runtimes, the session).

### Testing
- `cargo build` and `cargo test` in `rust/agenthub-runtime`: 28 passed, 0 failed (new:
  `every_documented_policy_maps_and_an_unknown_one_is_refused`, the whole table above plus the refusal).
- **Forcing harness** `/tmp/u2-live/drive.py` against a scratch daemon (`/tmp/u2-live`, its own state dir,
  its own tmux server via `TMUX_TMPDIR`; the live rig's tmux server was never addressed):
  - `StartRoom dead` (runtime `/nonexistent/agent-binary`) -> `REFUSED | room dead started no seat (1 of 1):
    dev: seat dead/dev died before it reported readiness (its pane is gone within 1500ms; pid 0 was the
    session record tmux returned)`; `GetRoomStatus dead` -> `dev=stopped`.
  - `StartRoom badmodel` (omp, model that does not exist) -> `REFUSED | ... died before it reported
    readiness ... within 20s`; `GetRoomStatus badmodel` -> `dev=stopped`.
  - `StartRoom badpolicy` (`permission_policy: "approve"`) -> `REFUSED`; `GetRoomStatus badpolicy` ->
    `dev=stopped`.
  - Positive control (`/tmp/u2-live/live_only.py`): `StartRoom live` (omp, real model,
    `permission_policy: builtin:yolo`) -> `dev=running`; a whole turn completed in the pane —
    `agent_start → turn_start → message_* → READY → turn_end → agent_end → prompt_result →
    session_settled` — and the pane's own log line shows the posture:
    `spawned omp ["--mode", "rpc", "--session-dir", ..., "--approval-mode", "yolo", "--model", ...]`. No
    approval prompt appeared; the one dialog that did arrive was the advisor-cost request, answered
    cancelled by the runner, and the turn continued.
  - `StopRoom` for all four rooms left nothing behind.

### Known gaps
- The scratch harness cannot authenticate `omp` unless the seat's environment carries the provider
  credential (the client's job, not this module's): with the default tmux server the pane inherited a
  server environment without it and the turn ended on `No API key found for deepseek`. Run without
  `TMUX_TMPDIR`, the posture is still provable from the argv line but the turn is not.
- That credential failure also surfaced a correlation gap: omp's error came back without an id this runner
  recognised, so the runner classified it (`Terminal`, correct) but did not own it ("a response this runner
  did not ask for") and the seat's state stayed `idle` instead of `blocked`. Both outcomes are right for a
  missing credential; a turn that dies this way on a *retryable* failure would not resume. Parity 2/4 eat
  this, and the measured evidence is here rather than assumed.
