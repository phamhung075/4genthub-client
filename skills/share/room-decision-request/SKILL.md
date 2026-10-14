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
  --questions-file ask-questions.json [--category <token>] [--detail "<context>"]
```

It reads `$AGENTHUB_URL` and `$AGENTHUB_TOKEN` (never printed) and prints the JSON response. Exit codes: `0` this tier decided (act on the chosen options), `5` escalated (see `escalate_to`), `1` request failed or refused, `2` usage.

- **Seat**: `escalate_to: "lead"` means send the lead the question with the recommended option, so the answer is one word. The lead repeats the call with `--tier lead`.
- **Lead**: `escalate_to: "owner"` means the owner decides. Stop the branch that depends on it, put the brief and questions in a `4genteam room queue create` row to the room's principal session tagged `owner-decision`, and say so in your report. The owner answers on the dashboard under **Decision Requests**.

The threshold is `JEV_DECISION_THRESHOLD` (default 0.8) at every tier. **Category** is a free token (`a-z`, `0-9`, `_`, `-`); omit it for `general`. Categories the backend lists in `DECISION_OWNER_ONLY_CATEGORIES` (for example `omp_cap`) go to the owner whatever the confidence. Every step is appended to the decision log.

## Checklist

- [ ] Questions JSON is valid and the brief is self-contained.
- [ ] You asked your own tier first.
- [ ] Exit 5 was followed one level only.
