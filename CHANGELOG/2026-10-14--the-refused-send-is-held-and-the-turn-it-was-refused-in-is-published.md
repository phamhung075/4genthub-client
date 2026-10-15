# The refused send is held, and the turn it was refused in is published

A `send` to a seat that was mid-turn printed a delivery and the text was gone. The client's half of that
was cured on its own branch (`623d29a`: only `submitted` earns the `Sent to` line); this is the runtime's
half, where the frame is actually lost.

## What was wrong, measured 2026-10-14 (U3 run 1 and the live room)

Three facts, in one path:

1. **The refused frame was DROPPED.** `4genteam send` to a mid-turn runner seat wrote one `text` frame into
   the seat's mailbox; the runner relayed it as a `prompt` RPC; omp answered
   `"Agent is already processing. Use steer() or followUp()…"`. The runner's mailbox path never recorded
   the prompt it relayed, so the response was not recognised as its own: `decide_resume` saw `turn_active`
   and answered `Resume::Nothing("the turn is still running")`, which mirrors the error and loses the text.
   Nothing was queued, nothing was re-sent, nothing was reported.
2. **Nothing reported the refusal back.** The refusal reached the pane and the log line and no other
   surface. The seat's state file — the channel the daemon's `status` reads — said `idle`.
3. **The turn was never published at its start.** Only failure arms and agent events wrote state, so a
   seat with a turn in flight read as a seat at its prompt. That is the word that made the whole thing
   invisible from outside: `idle` beside a refusal.

## The change

One rule per defect, in `rust/agenthub-runtime/src/`:

- **The relayed prompt is remembered.** `relay_frame`'s `text` arm records the frame in the same `pending`
  slot the pane's own input uses, so its response is matched by id and the runner owns it.
  (`spawn_mailbox`/`serve_mailbox_stream` are given the slot; it is created before them.)
- **A refusal of our own prompt while a turn is live is HELD, not dropped.** `Resume::Hold` joins the
  decision, taken only when the error is classified terminal AND the prompt is ours AND the turn is live —
  a retryable error mid-turn still resumes nothing (the locked rule, unchanged and still pinned). The text
  goes to a one-slot hold beside `pending`, and the state file publishes
  `working` / `held: the agent refused this prompt while its turn was running` while it waits.
- **The held text comes back when the seat is free.** `deliver_held` runs from `turn_end` and from
  `agent_end`/`session_settled`: it takes the slot, re-sends the text as a fresh `prompt` under
  `runner-held-<pid>-<n>`, records it as pending, and publishes
  `working` / `delivering the text this runner held for the next turn`. It is bounded like the resume
  ladder (`MAX_RESUME_ATTEMPTS`); past the bound the seat is `blocked`, named, and nothing more is sent.
  `agent_end` no longer writes `idle` unconditionally: a seat with a prompt in flight or a text still held
  is not at its prompt.
- **Every turn is published at its start.** `turn_start` writes `working` whoever opened the turn — the
  pane's input, the daemon's mailbox, a resume.
- **The runtime's own send receipt follows the client's rule.** `agenthub-runtime send` printed
  `delivered to <session>` for EVERY answer except `unconfirmed` — `relayed` included. `send_receipt` now
  gives `delivered to` to `submitted` alone; `relayed` reads
  `relayed to <session>, submission unconfirmed: …`, and a missing or unknown outcome says the answer
  carries no submission verdict.

## Evidence

- **88 tests pass** (86 before), 0 failed: `cargo +stable test`.
- **Falsified at the unit level.** With the three rules reverted, exactly the three new pins fail and
  nothing else: `a_refused_prompt_is_held_and_delivered_when_the_turn_ends` (`left: Nothing("the turn is
  still running")`, `right: Hold(…)`), `a_text_frame_becomes_the_agents_prompt_on_the_child_channel`
  (`left: None`, `right: Some(("nudge-1", …))`), and `only_a_submission_is_a_delivery`. The locked pin
  `an_error_mid_turn_resumes_nothing` passes in both states, which is the control that the retry ladder
  was not weakened.
- **Falsified end to end.** The real runner binary against a scripted child that is mid-turn when the
  frame arrives and refuses it in omp's own words (`--state-dir` a scratch dir, the mailbox socket driven
  like the daemon does, nothing of the live room touched). With the change:

  ```
  before the send:            state=working activity=working
  the runner's answer:        {"data":{"relayed":"prompt"},…,"ok":true}
  right after the send:       state=working  classification=held: the agent refused this prompt while its turn was running
  after the turn ended:       state=idle
  what the agent received:    …{"id":"send-20351-…","message":"THE TEXT…"} then {"id":"runner-held-20090-1","message":"THE TEXT…"}
  occurrences of the text in the agent's stdin: 2
  ```

  With the same two rules reverted, the same script reads `state=idle` mid-turn and
  `occurrences … : 1` — the text was dropped, which is the defect.

## What this does not do

- **It does not install itself.** The room runs a binary built before `11438a5` (no runner branch at all,
  so every seat is answered by the pane path); the receipt on the live room changes only when a runtime at
  or after this commit is installed and its runners restarted. That is the deployment half, not this
  branch's.
- **The daemon's one-shot reply still cannot carry the verdict.** The runner answers the reply socket the
  moment the frame is written, which is what keeps `send` from blocking for a whole turn; the verdict
  travels on the durable channel instead — the seat's state file, which `status` and `get_room_status`
  already read. A caller that wants the agent's verdict reads the state, and the receipt says
  `submission unconfirmed` until then, which is now true rather than a lie.
- **`4genteam sync messages` still acks a relayed-and-refused message** (`internal/clientsync/messagesverb.go:90`).
  The honest rule there needs the verdict to be readable, which this makes possible, and the ack change is
  its own unit.
