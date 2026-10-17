# The held text names its sender, and goes back to them

Hub task `c94cecfc`. The runner's hold is where a text that arrives while the seat is mid-turn waits for the
next free turn. Its measured defect is that at its bound the text was DROPPED IN SILENCE: `deliver_held`
incremented the SAME counter the resume ladder counts its attempts in and compared the total against
`MAX_RESUME_ATTEMPTS` (3), so a text refused once - on a seat that had already spent three resume attempts on
unrelated turns - was dropped on its FIRST offer, with a `gave up` line in the runner's own log as its only
trace. Measured in the live room: 19 give-up lines counted at 23:14Z in the window that began 22:00Z on
2026-10-16, 13 of them addressed to the lead seat - and the window is OPEN-ENDED, so a later recount reads more
(33 at 23:41Z, 17 of them the lead's) rather than disagreeing; fe-dev's three lost sends are the same event seen
from the sender's end, which read `relayed` for a message that had died.

Two things were missing for a sender to be able to act on any of it. The text's own bound - a text's life is
not the seat's turn life, and sharing one counter made the other's spending the text's death. And the ORIGIN:
whose text it was and which verb carried it. The origin was not on the wire at all: `SendText` carried
`session` and `text`, so the frame the runner relayed could not say who sent it, and the delivery line could
only name the refused prompt's id.

### Changed

- `rust/agenthub-runtime/src/runner/mod.rs`:
  - `pub const MAX_HELD_ATTEMPTS: u32 = 3` - the bound belongs to the TEXT, and reaching it is a RETURN, never
    a deletion. `HeldText { refused_id, text, origin, attempts }` replaces the `(id, text)` pair, so a text
    carries who sent it and how many offers it has spent (a requeue is not a fresh start).
  - `Origin { from, verb }` with `label()` (`"<session> (send)"`, `"an unnamed sender"` when the caller said
    nothing) and `sender_session()`. `PendingPrompt` carries the origin too: a text resumed by the ladder and a
    text relayed by the mailbox are the same text and must be answered at the same place.
  - `publish_hold` now names the origin in the sentence a reader acts on - `"held: the text from <origin>
    waits for the next free turn (N of 8 held)"` - and a full queue is still `blocked`.
  - `return_to_sender(state_dir, origin, target, refused_id, text, reason)`: at the bound the runner speaks the
    `text` verb BACK to the sender's own mailbox, whole, with the reason, and reads the mailbox's answer for it
    (`relayed`), because "a local write succeeded" is not "the sender was told". A hand-back that cannot be
    made - no addressable session, or its mailbox refused - is published and the text goes back to the FRONT of
    the queue with its count reset rather than out of the process.
  - `deliver_held` names the origin in the delivery it logs and publishes
    (`"delivering the text <id> from <origin> (attempt n/3): the seat is free again"`), and
    `next_held_delivery_id` gives every held delivery a per-process id, so a refusal of one held text can never
    be read as another's.
- `rust/agenthub-runtime/src/types.rs`, `ipc/mod.rs`, `main.rs`, `supervisor/mod.rs`: `SendText` gains
  `from`/`verb` (both `#[serde(default)]`, so an older caller stays valid and is named unnamed, not guessed at),
  the supervisor stamps them onto the frame it hands the seat's runner, and the runtime's own `send` speaks
  them from `AGENTHUB_SESSION_NAME`.
- `internal/clientruntime/transport.go`, `ipc.go` (`agenthub_client`): `SendTextArgs(session, text, sender,
  verb)` is the one request builder, used by `Client.SendText` and `RuntimeSendTextOn`; a caller with no session
  of its own (a shell, a hook) sends no `from` at all instead of a blank string the runner would have to read
  as a name.
- `internal/clientruntime/sender.go`, `ipc.go`, `transport.go` and every client carrier
  (`clientlifecycle`, `clientoutbox`, `clientsync`, `clientrestore`): `SendOptions.Verb` carries the verb that
  CARRIED the frame, and `Client.SendText` / `SendTextToSeat` / `DeliverToSession` take it, so the client's
  paths are distinguished the way the runtime's already were - the rig's `say`, its `abort`, the `wake`,
  `messages`, and the restore `brief` - instead of every one of them claiming to be the `send` verb.

### Testing

- `rust/agenthub-runtime/tests/held_send.rs` (new, Linux): `a_text_sent_to_a_seat_that_is_mid_turn_is_delivered_when_the_turn_ends`
  runs the REAL binary - a real mailbox socket, a real runner, and a child on PATH (`omp`) that opens a turn,
  refuses what arrives inside it, and ends it when the probe says so - and asserts the whole path: the refusal
  is HELD AND PUBLISHED (the state file says `working` with `held: the text from <session> (send) waits for the
  next free turn`), the text reaches the agent after the turn ends under a `runner-held-<pid>-<n>` id and with
  its body unchanged, the seat log's delivery line names the sender's session and the verb, and the seat is not
  left `blocked`. Red first, measured in a detached worktree at this branch's base `131796e`: it fails on the
  publication (`the runner published the open turn` - the file reads `idle` where a sender is told nothing) and,
  with that assertion removed, on the origin (`delivering the text send-probe-2 refused (attempt 1/3)`). The
  PROBE WAITS FOR THE HANDSHAKE before it sends anything: the `get_state` answer's `idle` lands after a
  `turn_start` printed during the handshake, and only the probe's own bare-socket write can race it.
- `runner::tests::a_held_text_past_its_own_bound_goes_back_to_its_sender` (new): the text's own count, not the
  resume ladder's, is what reaches the bound, and the sender's mailbox gets the text whole with the reason.
- `runner::tests::a_hand_back_with_no_addressable_sender_keeps_the_text_held` (new): an `Origin` with no session
  publishes and re-queues at the FRONT with the count reset - the text is never the price of an unaddressable
  sender.
- `runner::tests::a_held_delivery_that_cannot_reach_the_agent_keeps_the_text` (new): a channel that is gone
  leaves the text held and the pending slot clear, where the old code let it fall out of the queue entirely.
- `runner::tests::a_refused_prompt_is_held_and_delivered_when_the_turn_ends` and
  `the_held_queue_is_bounded_and_names_the_depth_it_refused`: updated to the `HeldText`/`Origin` shape, and
  their published sentences now have to name the origin.
- `internal/clientruntime/sendseam_test.go::TestSendTextArgsNamesTheOriginAndOmitsAnUnknownSender` (new): the
  request carries `from`/`verb` when the caller has a session, and no `from` key at all when it does not.
- `internal/clientseat/send_transport_test.go::TestSendGoesThroughTheRuntimeSocket` (extended): the payload a
  seat's send puts on the socket names the origin beside the target and the body.
- `supervisor::tests::a_multi_line_message_reaches_a_runner_seat_as_one_text_frame` (extended) and
  `main::tests::the_cli_send_stamps_this_shells_session_and_the_send_verb` (new), both at the reviewer's gate:
  the daemon's `from`/`verb` STAMPING and this binary's own origin were asserted NOWHERE - deleting the two
  stamping lines in `deliver_text` left the whole suite green (the reviewer's mutation, 2026-10-17) - so the
  frame test now asserts `frame["from"]`/`frame["verb"]`, and the origin is a pure function
  (`send_origin_from`) whose two no-session cases are pinned (a shell that is not a seat; a name of spaces).
  Re-measured RED: with the two stamping lines deleted the frame test fails on
  `left: Null, right: "agenthub--demo--principal"`.
- `clientlifecycle`: the rig's two carriers pin their own verb (`say`, `abort`) beside the text they send, so a
  frame's verb cannot quietly become the generic `send` again.
- At this change, from `wt/go-dev-scope` (these two commits replayed onto main `46b8b6e`): `cargo test` in
  `rust/agenthub-runtime` = 166 passed, 0 failed (149 unit + 17 across `tests/`); `go vet ./...` rc 0;
  `gofmt -l internal/ cmd/` clean; `go test -count=1 ./...` = 32 packages ok, 0 fail.
