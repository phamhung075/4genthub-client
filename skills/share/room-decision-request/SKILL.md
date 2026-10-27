---
name: room-decision-request
description: "Use when a room lead or agent needs an owner/human decision. Structures the brief and options, asks the decision ladder with `4genteam decide` and escalates one tier at a time to the owner dashboard."
metadata:
  agenthub:
    stage: factory-approved
    audience: room leads and agents
    sibling_skills:
      - human-in-the-loop
      - messaging-the-human
      - queue-handoff
signals:
  - command: 4genteam decide
  - command: room queue create
---

# Room Decision Request

Use this when a seat or lead reaches a decision gate (architecture choice, scope change, tradeoff, credential or permission). Ask the lowest tier that can decide with `4genteam decide`; escalate exactly one level only when it says so.

## 1. Write the questions

A JSON array, 1 to 4 questions, 2 to 4 options each, labels under 75 characters, at most one `"recommended": true` per question:

```json
[{"id": "decision_choice", "question": "Which option should the team execute?",
  "options": [{"id": "opt_a", "label": "Option A (Recommended) - why", "recommended": true},
              {"id": "opt_b", "label": "Option B - alternative"}]}]
```

## 2. Ask your tier

```bash
4genteam decide --tier seat --summary "<5-8 word subject>" \
  --questions-file ask-questions.json [--category <token>] [--detail-file <context.txt>]
```

Jev answers on your own machine (it needs `TYPESAFE_API_KEY` in `~/.config/4genthub/.env`); the server is not called and `$AGENTHUB_URL` is not needed. It prints the JSON outcome. Exit codes: `0` this tier decided (act on the chosen options), `5` escalated (see `escalate_to`), `1` the Jev log could not be written, `2` usage or a missing key.

- **Seat**: `escalate_to: "lead"` means send the lead the question with the recommended option, so the answer is one word. The lead repeats the call with `--tier lead`.
- **Lead**: `escalate_to: "owner"` means the owner decides. Stop the branch that depends on it and raise the ask on the room's rail as an `owner-decision` row addressed to the room's human, `human@<room>`:

  ```bash
  4genteam room queue create <room> --to human@<room> --tag owner-decision \
    --summary "<the decision, ≤120 chars>" \
    --body "<the brief: what is open, the options in order, the recommended one (≤600 chars)>"
  ```

  `human@<room>` is the owner's inbox: the bridge posts every open row addressed to it to the dashboard notification inbox, which keeps it for the owner until they next open the dashboard. Never address an owner ask to a seat (yourself included): a seat's row never reaches the owner. The create prints the `qitem-<…>` id, `4genteam room queue show <room> <id>` reads it back, and the id goes in your report.

## 3. Wait for the owner's answer

Enforced in code, not by convention:

- **Only the owner's answer closes an `owner-decision` row.** The rail refuses a close (or `--state done`) of one by any seat. You may add a `--note` to it when the ask changes; you may not close, supersede or "no-follow-on" it.
- **Do not re-ask the owner's question at lead tier.** `4genteam decide --tier lead` with a summary that rewords one escalated to the owner in the last 7 days does not call Jev: it exits `5` with `escalate_to: "owner"` and names the open escalation. A genuinely different question takes `--distinct-from-owner-ask "<why it differs>"`, and the reason is written to the Jev log, where the owner reads it.
- Until the answer arrives, work only on what does not depend on it.

The owner answers by closing the row as the room's human, handing the answer to the lead in the same step:

```bash
AGENTHUB_SESSION_NAME=human@<room> 4genteam room queue close <room> <qitem-id> \
  --reason handed_off_to --target agenthub--<room>--lead --note "<the answer: chosen option and any condition>"
```

The close leaves a new pending row for the lead carrying the answer in its note, and the lead is woken by it. Act on that row and close it as usual.

**Owner validation of a step is an owner decision.** When a step needs the owner's approval before it runs or before it counts as done - for example each `run_stepN.bat` that the aa2-rs owner runs on Windows and approves - raise it as one `owner-decision` row to `human@<room>` per step, with the step, the command the owner runs, and what the owner should check. The step is not done until the owner's answer closes that row.

The dashboard's **Decision Requests** panel is not fed by this client yet: nothing mirrors a row's structured questions to it, so do not tell the owner to look there. The inbox notification and the row are the path.

The threshold is `JEV_DECISION_THRESHOLD` from `~/.config/4genthub/.env` at every tier (read the value there; this text carries none). **Category** is a free token (`a-z`, `0-9`, `_`, `-`); omit it for `general`. `credentials` and `spend`, and the categories in `DECISION_OWNER_ONLY_CATEGORIES` (for example `omp_cap`), go to the owner whatever the confidence, decided by code with no Jev call. Every step is appended to `~/.config/4genthub/jev_decisions.log`.

## Checklist

- [ ] Questions JSON is valid and the brief is self-contained.
- [ ] You asked your own tier first.
- [ ] Exit 5 was followed one level only.
- [ ] An owner ask went to `human@<room>`, tagged `owner-decision`, and its id is in your report.
- [ ] You did not close it, and you did not re-ask its subject at lead tier.
