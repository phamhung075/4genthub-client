# The held text is not a refusal

Hub task `83a39aa5`. `send` reported `the runtime at <socket> refused it: …; nothing was sent` for every
failure the transport could produce - and the U3 gate caught it printed against a delivery the runtime had
HELD in the runner's mailbox, where the text was the next turn's prompt and not lost at all. The sentence was
false in the one case a sender most needs the truth, and the transport row's whole job after this change is
to say which of the three things happened: nothing left the client, the frame reached the runner (the text
may be held), or the answer cannot be read at all.

Two defects, one per side.

**The daemon could not answer two overlapping deliveries to one seat.** Its reply socket was one path per
seat per daemon, `<seat>.reply-<pid>`, unlinked and rebound on every call, so the second call's bind unlinked
the first listener and both replies landed on the survivor while the first call's accept waited forever
(measured: 1 run in 3 failed on exactly this leg). And the wait was unbounded: an accept that never fired and
a read that went quiet were both indefinite, so the client's own 10 s socket deadline was the only bound and
it reported a bare `i/o timeout`.

**The client could not tell a hold from a refusal.** Its error branch was one sentence for every error, and
the runner's silence reached it as an i/o timeout with no words.

### Changed

- `rust/agenthub-runtime/src/runner/mod.rs`:
  - `REPLY_SEQ` and `{seat}.reply-{pid}-{seq}.sock`: the reply socket is UNIQUE PER CALL, so two overlapping
    deliveries each get their own listener and their own answer. The unlink stays - cheap insurance against
    a stale file from a crashed process that had the same pid and sequence.
  - `REPLY_TIMEOUT = 8 s` and `read_reply(listener, room, seat, deadline)`: the accept and the read both end
    at the deadline, and the elapsed wait is the runner's own named error - `no answer from the runner for
    <room>/<seat> within 8s: the frame reached its mailbox, so the text may be held for its next turn`. It
    MUST stay under the client's `CallDeadline` or the client's socket deadline would bury it.
  - The reply socket file is removed on EVERY path, a timeout and a refusal alike: a unique name that leaked
    would leave one stale inode per failed call.
- `internal/clientruntime/ipc.go` (`agenthub_client`):
  - `CallDeadline` is exported: a caller that reports a timeout has to name it.
  - `IsNoAnswer(reason)` recognizes the daemon's reply-bound sentence by its words (`no answer from the runner
    for`), the way `IsFramingFailure` reads an older daemon's complaint - the runtime that must be read may be
    older than this client, so the words are the only channel both builds share.
- `internal/commpolicy/policy.go`: `OutcomeDeliveryUnconfirmed = "delivery_unconfirmed"` is the third fact an
  allowed send's outcome line can carry - the request reached the runtime and the runtime did not prove the
  delivery either way. It is not `delivery_failed`: that claims a loss nobody measured.
- `internal/clientseat/reseat.go`:
  - `sendFailureSentence(socket, err)` and `nothingWasSent(err)` replace the single sentence. A daemon
    refusal, an older daemon that could not frame the request, or no daemon at the socket means nothing was
    sent. The daemon reporting the runner's silence carries the runner's own sentence whole and adds only the
    verdict (`; the delivery is unconfirmed`), so the hold is stated once; a socket timeout and an answer this
    client cannot read name the hold themselves and never claim nothing was sent.
  - The outcome line says the same thing: `delivery_unconfirmed` when `!nothingWasSent(err)` and
    `delivery_failed` otherwise, so a held send is no longer audited as lost.
- `internal/clientactivity/activity.go`: `delivery_unconfirmed` joins `problemOutcomes`, so
  `activity --problems` shows an unconfirmed send beside a failed one.
- `internal/clientruntime/deadline_test.go`: the runtime's 8 s bound is named beside the 60 s readiness window
  and pinned - `CallDeadline` must outlast it, so a raised number has to face that test.

### Testing

- `runner::tests::two_overlapping_deliveries_to_one_seat_both_receive_their_replies` (new): two threads send
  to one seat through `send_to_seat` against a fake runner that answers each connection, and both calls get
  their own id back, then a third call still succeeds. RED at the old shared path: one of the two waits
  forever, and the answers cross.
- `runner::tests::an_unanswered_reply_ends_in_a_named_error_not_an_untimed_wait` (new): a fake runner that
  accepts and never answers, a `REPLY_TIMEOUT` shortened for the test - the call ends in the named error, in
  bounded time, not in an untimed accept.
- `runner::tests::a_silent_reply_connection_ends_in_the_same_named_error_not_an_untimed_read` (new): the
  accept happens, the read goes quiet - the same named error, and bounded.
- `internal/clientseat/sendfailure_test.go::TestTheSendFailureSentenceSeparatesARefusalFromAHold` (new): the
  mapping table over a REAL socket timeout and a REAL refused dial, plus the daemon's hold, a daemon refusal,
  an older daemon, and an unreadable answer - each asserts both the words and `nothingWasSent`.
- `internal/clientseat/sendfailure_test.go::TestSendToAHeldFrameSaysTheTextMayBeHeld` (new): end to end
  through `send` against a fake daemon that reports the runner's silence - exit non-zero, stdout empty, stderr
  carries the runner's own words and `may be held`, and never `nothing was sent`.
- `runner::tests::send_to_a_mailbox_that_never_answers_ends_in_the_named_sentence` (new): the REAL
  `send_to_seat` - bind, `replyTo`, write, wait, error - against a mailbox that reads the frame and never
  answers, spending the real `REPLY_TIMEOUT` (8.5 s in the suite, which runs in parallel). It asserts the
  premise the sentence rests on (the frame arrived carrying `replyTo`), the sentence itself, and that the wait
  is the bound rather than a shorter guess. A runner that READ a frame always answers it
  (`serve_mailbox_stream` replies even when `relay_frame` refuses), and a mid-turn text is relayed and
  answered `{"relayed":"prompt"}` at once - so a HELD text is not the silence leg. The silence leg is the
  LOST ANSWER: the frame reached the mailbox and nothing came back, which is exactly why the sentence says
  the text MAY be held and the old `nothing was sent` was the false claim.
- `internal/clientseat/sendfailure_test.go::TestAHeldSendIsAuditedUnconfirmedAndARefusalFailed` (new): the
  audit file a held send writes ends in `delivery_unconfirmed`, a daemon refusal ends in `delivery_failed`,
  and the decision line before either carries no outcome.
- `internal/clientruntime/deadline_test.go` (extended): `CallDeadline > REPLY_TIMEOUT`, the 8 s number named
  because it cannot be read across the language boundary.
- At this change, on `4genthub-min/send-reply-race`: `cargo test --bins` = 153 passed, 0 failed;
  `go test -count=1 ./...` = every package ok, 0 fail.
