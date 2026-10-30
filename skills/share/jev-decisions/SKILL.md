---
name: jev-decisions
description: "Use before asking anyone for a decision and before closing a task. Jev, the decision judge, runs on this machine through `4genteam decide` and `4genteam complete`; the server judges nothing."
metadata:
  agenthub:
    stage: factory-approved
    audience: every seat
    sibling_skills:
      - room-decision-request
      - verification-before-completion
signals:
  - command: 4genteam decide
  - command: 4genteam complete
---

# Jev decisions

Jev is the judge that answers a decision or checks a finished task. **It runs on this machine, like your own model.** The server does not ask it, so no MCP tool and no server route reaches it: you call it through two `4genteam` verbs. Both read `TYPESAFE_API_KEY` from `~/.config/4genthub/.env` and append every outcome to `~/.config/4genthub/jev_decisions.log`.

## 1. A decision is open: `4genteam decide`

Run it **before** you message the lead or the owner about a choice (architecture, scope, tradeoff, which of several fixes). Do not guess and do not wait for an answer you could get in seconds.

```bash
4genteam decide --tier seat --summary "<5-8 word subject>" --questions-file q.json
```

Put the context in a file, never in `--detail "..."`: a double quote, a `$` or a newline in the text breaks the shell command, and the text after it runs as a command or lands in the log as garbage. Write it with a quoted heredoc and pass the path:

```bash
cat > detail.txt <<'DETAIL'
<what you measured, the options and your lean; any characters are safe here>
DETAIL
4genteam decide --tier seat --summary "<5-8 word subject>" --detail-file detail.txt --questions-file q.json
```

Exit `0`: your tier decided, so act on the printed answer and note it in your task. Exit `5`: it escalated, and the JSON names the next tier in `escalate_to`. Follow exactly one step:

- `escalate_to: "lead"` (from a seat): send the lead the question with the recommended option, so the answer is one word. The lead repeats the call with `--tier lead`.
- `escalate_to: "owner"` (from the lead, or any tier on an owner-only category): **`decide` has already raised the owner ask** - a rail row to `human@<room>`, tagged `owner-decision`, with the questions recorded for the dashboard's Decision Requests panel. The JSON carries its id in `owner_ask` and stderr says so. Put the id in your report, stop the branch that depends on the answer, and do not raise it again; the answer comes back to the lead as a pending row. Only when stderr says nothing was raised (no room could be named, or the rail refused) raise it yourself with `4genteam room queue create <room> --to human@<room> --tag owner-decision --questions-file q.json --summary ... --body ...`. The room is `--room`, else your session's room, else `$AGENTHUB_ROOM`.
- `escalate_to: "director"`: the room has a director office (`<room dir>/director/`). The founder is not asked by a seat there: `decide` appended one `QUESTION` line to `director/ready.md` (path in `ready`), and the director answers there with an `ANSWER` line; only the director raises an owner ask, for what it cannot decide. Wait for that answer and work only on what does not depend on it. A room whose director has denied `decide` to seats for design choices still takes those as QUESTION lines you write yourself.

The question format, categories and escalation rules are in `room-decision-request`.

### The three triggers - one of these and the call is due

(i) the change picks among two or more defensible mechanisms, interfaces or shapes and the task names none;
(ii) it adds behaviour outside the acceptance criteria;
(iii) it is irreversible or owner-only.

A seat never puts options in front of itself without a `decide` call. Picking one silently is exactly what
the triggers catch, and the room measures it: `~/.config/4genthub/jev_decisions.log` is the record each call
appends, and the assignment row carries the timestamp back.

Credentials and spend always go to the owner, with no Jev call. Never decide those yourself.

## 2. The work is finished: `4genteam complete`

Use it instead of marking the task done by hand.

```bash
4genteam complete --task <task-id> --base <sha-before-your-work> \
  --summary "<what changed>" --test "<the test command>"
```

It collects the evidence (diff, commits, the test run), checks it deterministically (a passing test command is required, keep-out paths, diff size), puts the task's acceptance criteria to Jev (a task with no criteria is `uncertain`, so give every task criteria when you create it), posts the evidence and then completes the task. Pass `--test` or the check fails. Exit `0` completed; `5` completed but your test command failed; `6` the gate refused (when `VALIDATION_GATE_ENFORCEMENT=true`): fix what the verdict names and run it again.

The idle-room guard also asks Jev, once per set of open rail items: when every seat is idle and the rail is unchanged since the last verdict it does not ask again, so a guard message to the lead cannot start a loop. When Jev is unsure, the lead is told to ask the owner - or, in a director room, the guard appends the QUESTION to `director/ready.md` itself and the lead waits for the director's answer. Each decision-log line keeps the questions and their options beside the pick, so a decision can be audited.

## Checklist

- [ ] A decision was asked through `decide` before it went to the lead.
- [ ] The `jev=<ts>` the call printed is on the assignment row or in the report; `none-open <why>` when
      nothing was open.
- [ ] The task was closed through `complete`, with `--base` and `--test`, and it had acceptance criteria.
- [ ] Nothing about credentials or spend was decided by a seat.
- [ ] An escalation past the lead was not raised twice: `owner_ask` (dashboard) or `ready` (director room) names where it went.
