---
name: room-decision-request
description: "Use when a room lead or agent needs an owner/human decision. Structures the brief and options, asks the decision ladder with `4genteam decide`, escalates one tier at a time; past the lead, decide raises the owner ask for the dashboard's Decision Requests panel, or in a room with a director office appends the question to director/ready.md for the director."
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
- **Lead** (or any tier on an owner-only category): past the lead, `decide` raises the escalation itself, once. Where it goes depends on the room:
  - **A room with a director office** (`<room dir>/director/`, the same declaration the git guard reads): `escalate_to: "director"`. One line is appended to `director/ready.md` (`ready` in the JSON names the file):

    ```
    - <4genteam clock --short> | QUESTION | <asking seat or tier> (Jev escalation) | <summary>; options: <id: label, ...>; asker's pick: <id or none>
    ```

    The director answers with an `ANSWER (director)` line in the same file; nothing goes to the dashboard. Only the director raises an owner ask, for what it cannot decide; the founder gets only that. Section 3 does not apply to these.
  - **Any other room**: `escalate_to: "owner"`. `decide` creates the owner ask on the room's rail - tagged `owner-decision`, addressed to `human@<room>`, its questions recorded for the bridge - and prints the id in `owner_ask`. Put it in your report and stop the branch that depends on it. Do not create a second one.

  The room is `--room`, else your session's room (`$AGENTHUB_SESSION_NAME`), else `$AGENTHUB_ROOM`; the Jev log entry records it. Only when stderr says nothing was raised (no room named, the rail or the file refused the write) raise it by hand - in a director room as the QUESTION line above, elsewhere as:

  ```bash
  4genteam room queue create <room> --to human@<room> --tag owner-decision \
    --questions-file ask-questions.json \
    --summary "<the decision, ≤120 chars>" \
    --body "<the brief: what is open, the options in order, the recommended one (≤600 chars)>"
  ```

  The create refuses an `owner-decision` row addressed anywhere but `human@<room>` or without `--questions-file`, and refuses `--questions-file` on any other row. It prints the `qitem-<…>` id and records the questions beside the room.

## 3. The owner answers on the dashboard (rooms without a director)

The bridge (`4genteam bridge run`) carries the ask both ways:

- it posts a notification to the owner's inbox and mirrors the ask, with its questions, to the dashboard's **Decision Requests** panel for this room;
- once the owner has clicked an option for every question, it closes the ask as `human@<room>`, `handed_off_to` the seat that asked, with the chosen options in the note. The asking seat is woken by a new pending row carrying the answer ("Owner's answer (dashboard): <question> -> <option> ..."). Act on it, then close that row as usual;
- if the ask is closed on the rail first, the panel shows it as superseded.

The answer is applied once: a second pull of the same answer is acknowledged, not applied again. The bridge asks the hub for answers at most once a minute, and only for a room with an ask open.

The owner can also answer on the rail, which is the path when the bridge is not running:

```bash
AGENTHUB_SESSION_NAME=human@<room> 4genteam room queue close <room> <qitem-id> \
  --reason handed_off_to --target agenthub--<room>--lead --note "<the answer: chosen option and any condition>"
```

## 4. Wait for the answer

Enforced in code, not by convention:

- **Only the owner's answer closes an owner ask.** The rail refuses a close (or `--state done`) of an `owner-decision` row addressed to `human@<room>` by any seat. You may add a `--note` to it when the ask changes; you may not close, supersede or "no-follow-on" it. The answer row handed to you is yours to close.
- **Do not re-ask the owner's (or the director's) question at lead tier.** `4genteam decide --tier lead` with a summary that rewords one escalated to the owner or the director in the last 7 days does not call Jev and raises nothing: it exits `5` with `escalate_to` naming the holder and the open escalation. A genuinely different question takes `--distinct-from-owner-ask "<why it differs>"`, and the reason is written to the Jev log, where the owner reads it.
- Until the answer arrives, work only on what does not depend on it.

**Room-tier idle verdicts follow the same routing.** When the idle guard's Jev call is unsure whether an idle room carries on, the lead is told to ask the owner; in a director room the guard appends the QUESTION to `director/ready.md` itself and the lead waits for the director's answer.

**Owner validation of a step is an owner decision.** When a step needs the owner's approval before it runs or before it counts as done - for example each `run_stepN.bat` that the aa2-rs owner runs on Windows and approves - raise it as one owner ask per step, with the step, the command the owner runs and what to check in the body, and a question such as "Step N passed?" with options approve / reject. The step is not done until the owner's answer arrives.

The threshold is `JEV_DECISION_THRESHOLD` from `~/.config/4genthub/.env` at every tier (read the value there; this text carries none). **Category** is a free token (`a-z`, `0-9`, `_`, `-`); omit it for `general`. `credentials` and `spend`, and the categories in `DECISION_OWNER_ONLY_CATEGORIES` (for example `omp_cap`), go to the owner whatever the confidence, decided by code with no Jev call. Every step is appended to `~/.config/4genthub/jev_decisions.log`.

## Checklist

- [ ] Questions JSON is valid and the brief is self-contained.
- [ ] You asked your own tier first.
- [ ] Exit 5 was followed one level only.
- [ ] The escalation past the lead went once: `owner_ask` (a `human@<room>` row, tag `owner-decision`) or, in a director room, `ready` (a QUESTION line in `director/ready.md`), and it is in your report.
- [ ] You did not close it, and you did not re-ask its subject at lead tier.
