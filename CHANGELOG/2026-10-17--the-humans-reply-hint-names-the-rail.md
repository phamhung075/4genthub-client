# The human's reply hint names the rail, not a send that refuses

An owner message arrives in a seat's pane stamped `From: human@kernel`, and the envelope's reply hint said
`4genteam send human@kernel "..."`. Running it answers `the runtime … lists no session "human@kernel", so the
seat key a send is decided by cannot be read for it; nothing was sent` — the one destination the hint named
was one its own verb could not resolve, and a seat spent a turn discovering that.

## Decided

The HINT is wrong, and send's resolution is right. A send addresses a runtime session: a human has none, and
a rail row must name a seat of the room (the daemon resolves a seat key to that seat's session and refuses a
destination that names none). The route that reaches the human is the rail row to the room's lead, the seat
that carries an owner ask to the owner.

## Fixed

- The envelope's reply line is built by `replyLine(label, recipient)`. `human@kernel` now gets `the rail
  carries the reply: 4genteam room queue create <room> --to agenthub--<room>--lead --summary "..."`, with the
  room read from the recipient (a session name `agenthub--<room>--<seat>`, or a seat key's room after the
  `@`; a value of neither shape leaves `<room>` rather than guessing). A blank sender gets no command at all
  — it used to name `4genteam send <unknown sender>`, which could not run either. Every other label keeps
  `4genteam send <label> "..."`, whose refusal names the session it could not resolve.

## Changed

- `clientruntime.HumanSender` (`human@kernel`) is now the one definition of the label the message deliverer
  sends as; it moved from `clientsync.humanSeatSession`, whose own comment said the hint addressed it back.

## Why the room's lead, not the principal

`principal@<room>` names no seat, so a create with it is refused by name — the rail's own record
(`2026-10-17--the-rail-stores-the-session-name-the-nudge-matches.md`) — and the decision ladder's owner step
files to the room's lead. A session name is accepted by a daemon on either side of that change.

## Tested

`internal/clientruntime/sendline_test.go`:
`TestTheReplyHintNamesOnlyRoutesThatReachTheSender` pins the measured defect — the human's hint carries the
rail row to `agenthub--4genthub-min--lead` and no `4genteam send`; a session name, a seat key and a bare seat
key keep the send hint; a blank sender names no command; a recipient of no readable shape leaves `<room>`.
`TestSendEnvelopeNamesSenderRecipientAndReply` still pins the exact envelope for a seat-key sender, and
`clientsync`'s delivery test reads the same `clientruntime.HumanSender`. `go test ./...` and `go vet
./internal/...` green; `go build ./cmd/4genteam` green.
