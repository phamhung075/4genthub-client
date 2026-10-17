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
- **Lead**: `escalate_to: "owner"` means the owner decides. Stop the branch that depends on it and raise the ask on the room's rail as an `owner-decision` row addressed to the room's lead:

  ```bash
  4genteam room queue create <room> --to agenthub--<room>--lead --tag owner-decision \
    --summary "<the decision, ≤120 chars>" \
    --body "<the brief: what is open, the options in order, the recommended one (≤600 chars)>"
  ```

  The room has no principal seat, so the lead is the seat that carries an owner ask to the owner. `--to` takes a seat the room declares - its session name, `<seat>@<room>`, or the bare seat key - and the create REFUSES any value that names no seat, listing the room's seats, so a misaddressed ask fails loudly instead of sitting unread. The create prints the `qitem-<…>` id, `4genteam room queue show <room> <id>` reads it back, and the id goes in your report. The owner answers on the dashboard under **Decision Requests**.

The threshold is `JEV_DECISION_THRESHOLD` from `~/.config/4genthub/.env` at every tier (read the value there; this text carries none). **Category** is a free token (`a-z`, `0-9`, `_`, `-`); omit it for `general`. `credentials` and `spend`, and the categories in `DECISION_OWNER_ONLY_CATEGORIES` (for example `omp_cap`), go to the owner whatever the confidence, decided by code with no Jev call. Every step is appended to `~/.config/4genthub/jev_decisions.log`.

## Checklist

- [ ] Questions JSON is valid and the brief is self-contained.
- [ ] You asked your own tier first.
- [ ] Exit 5 was followed one level only.
