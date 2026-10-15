# The send receipt claims only what the runtime proved

`4genteam send` printed `Sent to <session>` and exited 0 for text the agent never received, whenever the
target seat was mid-turn. This is the client's half of the two found in the live room at 20:01:5xZ and in the
U3 harness: the text was refused by the agent (`Agent is already processing. Use steer() or followUp() …`) and
dropped, while the receipt named the target, the state file said `idle`, and the transcript, pane, queue and
logs held nothing. The record of the finding is
`CHANGELOG/2026-10-14--send-claims-a-submission-the-runner-refused.md` in the product repo (`4genthub`, not
this client repo); the queue-instead-of-drop, the
refusal reported back and mid-turn activity published are the runtime's half.

## What was wrong

`RuntimeSendLine` read every answer except one as a delivery:

    if json.Unmarshal(answer, &doc) == nil && doc.Outcome == "unconfirmed" { …unconfirmed line… }
    return fmt.Sprintf("Sent to %s\n", session)      // everything else, including relayed

The runtime has two delivery paths and two vocabularies:

| path | document | means |
|---|---|---|
| a seat with no runner (pane paste) | `{"delivered":…,"outcome":"submitted"}` | the pane changed after Enter |
| the same, unanswered | `{"delivered":…,"outcome":"unconfirmed"}` | the pane did not change |
| a runner seat (the whole live room) | `{"delivered":…,"outcome":"relayed","runner":{"relayed":"prompt"}}` | the runner wrote the frame into the agent's channel |

`relayed` is not a delivery: the agent's own answer (which may be the refusal above) is mirrored to the seat's
pane and never reported back to the caller. And the old default line claimed the most exactly where nothing
was known — a **missing** outcome and **text that is not JSON** both printed `Sent to`.

## The change

One rule, now stated in the function: **only the outcome that proves a submission earns the Sent line.** Every
other answer takes an unconfirmed branch naming the reason that is actually known —

- `submitted` → `Sent to <session>` (unchanged);
- `unconfirmed` → `Pasted to <session>, submission unconfirmed: the pane did not change after Enter…`
  (unchanged, word for word);
- `relayed` → `Relayed to <session>, submission unconfirmed: the seat's runner wrote the frame into the
  agent's channel and the agent's own answer is not reported back, so the text is not claimed as delivered`;
- no outcome → `Submission unconfirmed for <session>: the runtime's answer carries no submission verdict…`;
- an outcome this build does not know → the same, quoting the outcome;
- an unreadable answer → the same, saying the answer could not be read as a verdict.

Exit codes are unchanged (0 with an honest line, as the pane-unconfirmed branch already was): the text may
well have arrived — a runner relays an accepted prompt — so a non-zero would report delivery failure for every
successful send to a runner seat.

`TestRuntimeSendLineReadsSubmittedAndMissingOutcomeAsSent` pinned the defect as the contract; it is replaced by
`TestOnlyASubmissionEarnsTheSentLine`, which requires the Sent line for `submitted` and refuses it for
`relayed`, a missing outcome, an unknown outcome and an unreadable answer. Three fixtures in
`clientseat/send_transport_test.go` answered a document shape the runtime does not produce (a bare
`{"delivered":…}`, and one empty `{}`) and now carry the real answer.

## WHICH HALF CLOSES THE LIVE ROOM, MEASURED

`outcome: relayed` is reachable only from `RoomSupervisor::deliver_text`'s runner branch, added by
`11438a5` ("deliver send as one text frame to a runner seat", 2026-10-14 21:11:16 +0200). The binary the room
runs — `~/.local/bin/agenthub-runtime`, sha256 `c1272c6c…`, built 21:09:11 +0200, and `/proc/<pid>/exe` for
every live runtime process at that minute (six then; the count moves with the room, and what that check
establishes is that every one of them predates it) — **predates it**: its strings contain no `send-{}-{}`, the
id format string that exists only at `rust/agenthub-runtime/src/supervisor/mod.rs:881`. So the daemon serving
the room today answers the pane path for every seat, `submitted` included, **including when the agent refused
the prompt** — which is why
the harness log carries `{"delivered":"agenthub--c2-lab--dev","outcome":"submitted"}` beside that seat's runner
printing `gave up: the agent refused the turn`. This client change closes the receipt on a runner-aware
runtime (any install at or after `11438a5`); on the older daemon the same case still reads `Sent to`, and that
word is the runtime's to fix — the runner must report the refusal back so the daemon answers something other
than a submission.

## Evidence

- `TestOnlyASubmissionEarnsTheSentLine`, **falsified rather than read**: with the old verdict restored the test
  fails on all four non-proving answers (`got "Sent to s\n", want …`), and `transport.go` was then restored
  byte-identically (md5 compared).
- The built binary, end to end over a real socket, against a stub answering as the daemon does:
  `relayed` → `Relayed to 4genthub-min-go-dev@4genthub-min, submission unconfirmed: …` exit 0;
  `submitted` → `Sent to 4genthub-min-go-dev@4genthub-min` exit 0.
- `go build ./...` 0, `go vet ./...` 0, `go test ./...` 25 packages ok, 0 FAIL.

## One more surface of the same root cause, NOT fixed here

`4genteam sync messages` acks a pulled message to the cloud as soon as `RuntimeSendText` returns no error
(`internal/clientsync/messagesverb.go:90`, `sendToSession`), and its own usage text promises the ack happens
"ONLY after the text landed". A relayed-and-refused message is acked the same way, and an ack suppresses
re-delivery on the next pass, so that path loses the message *and* records it as delivered. It is deliberately
untouched: the honest rule there is the same one, and it cannot be written until the runner reports the agent's
verdict back (the runtime half) — otherwise every message to a runner seat would stay unacked forever.
