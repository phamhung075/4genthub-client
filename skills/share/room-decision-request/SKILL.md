---
name: room-decision-request
description: "Use when a room lead or agent needs an owner/human decision. Structures the brief and options, creates the queue item with --destination human-operator@external, mirrors to cloud/web dashboard, and blocks the working row until resolved."
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

Use this skill when an autonomous team lead (or seat) reaches a genuine decision gate that requires owner/human input (architecture choice, scope change, feature tradeoff, external credential/permission grant).

## Overview & Architecture

When a room needs an owner decision:
1. **Author the brief & questions**: Prepare a concise Markdown brief (`<name>-body.md`) and a structured JSON questions file (`<name>-questions.json`).
2. **Create the decision queue item**: Use `rig queue create` targeting `--destination human-operator@external`.
3. **Automatic Cloud & Jev AI Evaluation**: 4genthub routes the ask to Jev AI (`jev-1.13.0` via TypeSafe AI System One). If confidence is $\ge 0.8$, Jev auto-answers immediately; if $< 0.8$, it appears in the web dashboard under **Decision Requests**.
4. **Owner Interaction**: For decisions with confidence $< 0.8$, the owner reviews the request in the web dashboard sidebar under **Decision Requests** (with visual pulse animation and badges), clicks the option, and validates.
5. **Client Drain & Unpark**: The client supervisor automatically pulls recorded answers (`GET /api/v2/openrig/decisions/answers`), unparks the local queue item (`pending` → `done`, `closureReason: no-follow-on`), and notifies the seat.

## Step-by-Step Instructions

### 1. Author the Question Files

Create the brief file (e.g. `ask-brief.md`):
```markdown
### Decision Needed: <Clear Short Title>

**Context**: <2-3 sentences on what has been done and what requires a decision>.
**Tradeoff / Impact**: <What happens if option A vs option B is chosen>.
**Recommended Action**: <Which option is recommended and why>.
```

Create the questions file (e.g. `ask-questions.json`):
```json
[
  {
    "id": "decision_choice",
    "question": "Which option should the team execute?",
    "options": [
      {
        "id": "opt_a",
        "label": "Option A (Recommended) — description",
        "recommended": true
      },
      {
        "id": "opt_b",
        "label": "Option B — alternative description"
      }
    ]
  }
]
```

Rules:
- 1 to 4 questions per request.
- 2 to 4 options per question.
- Labels under 75 characters.
- Mark at most one option as `"recommended": true`.

### 2. Post the Decision Request

Run `rig queue create` with `--human-intent decision`:

```bash
rig queue create \
  --destination human-operator@external \
  --human-intent decision \
  --priority high \
  --summary "<Short 5-8 word subject>" \
  --body-file ask-brief.md \
  --human-questions-file ask-questions.json \
  --verify \
  --json
```

Output gives you the new `qitem_id` (e.g., `qitem-20261012...`).

### 3. Block Your Working Row on the Decision

Block your current active task row on the newly created decision `qitem_id`:

```bash
rig queue block <my-current-qitem-id> --on <decision-qitem-id> --reason "Waiting for owner decision"
```

Do **not** block on the human address; block on the specific `decision-qitem-id`.

### 4. Resuming After Resolution

When Jev AI auto-selects (confidence $\ge 0.8$) or the owner validates their choice on the dashboard:
1. The client supervisor pulls the recorded answer from 4genthub.
2. The decision queue item transitions to `done` (`closureReason: "no-follow-on"`).
3. The reply row is emitted to your seat containing `humanAnswers`:
   ```json
   {
     "decision_choice": "opt_a"
   }
   ```
4. Your blocked task row is automatically unblocked and ready to resume.

## The Decision Ladder: Seat, Lead, Owner

Most questions never need the owner. Ask the lowest tier that can decide, and escalate exactly one level only when it cannot.

1. **Seat**: before sending anything to the lead, ask Jev. `POST /api/v2/openrig/decisions/evaluate` with `tier: "seat"`, a `category`, `summary`, `detail` and the same `questions` array as above (mark your recommended option). If the response says `decided: true`, apply `answers` and keep working. Nobody is told.
2. **Lead**: if the response says `escalate_to: "lead"`, send the question to the lead with the `recommended` option, so the answer is one word. The lead repeats the call with `tier: "lead"`.
3. **Owner**: if the lead's response says `escalate_to: "owner"`, create the owner request in the steps above. Ask the owner only for a very important decision.

The threshold is `JEV_DECISION_THRESHOLD` (default 0.8) at every tier. Every step is appended to the decision log with its tier, category, confidence and outcome.

**Category** is a free token (`a-z`, `0-9`, `_`, `-`) that names what the question is about; leave it out for `general`. Some categories reach the owner whatever Jev's confidence, because confidence measures how concentrated an answer is, not whether the action is safe. The built-in set is `credentials` and `spend`. A project adds its own with `DECISION_OWNER_ONLY_CATEGORIES` (comma-separated), for example `omp_cap,deploy-prod`. Name your question's category after the case, and never pick `general` to avoid the owner.

## Verification Checklist

- [ ] Brief is self-contained with context and tradeoffs.
- [ ] Questions JSON contains valid IDs and labels.
- [ ] `rig queue create` targets `--destination human-operator@external`.
- [ ] Working row is blocked on the created `qitem_id`.
- [ ] Once answered, inspect the reply row and execute the chosen branch.
