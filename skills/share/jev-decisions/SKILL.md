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

Exit `0`: your tier decided, so act on the printed answer and note it in your task. Exit `5`: it escalated, so send the lead the question with the recommended option (the lead repeats the call with `--tier lead`; the lead's escalation goes to the owner). The question format, categories and escalation rules are in `room-decision-request`.

Credentials and spend always go to the owner, with no Jev call. Never decide those yourself.

## 2. The work is finished: `4genteam complete`

Use it instead of marking the task done by hand.

```bash
4genteam complete --task <task-id> --base <sha-before-your-work> \
  --summary "<what changed>" --test "<the test command>"
```

It collects the evidence (diff, commits, the test run), checks it deterministically (a passing test command is required, keep-out paths, diff size), puts the task's acceptance criteria to Jev (a task with no criteria is `uncertain`, so give every task criteria when you create it), posts the evidence and then completes the task. Pass `--test` or the check fails. Exit `0` completed; `5` completed but your test command failed; `6` the gate refused (when `VALIDATION_GATE_ENFORCEMENT=true`): fix what the verdict names and run it again.

The idle-room guard also asks Jev, once per set of open rail items: when every seat is idle and the rail is unchanged since the last verdict it does not ask again, so a guard message to the lead cannot start a loop. Each decision-log line keeps the questions and their options beside the pick, so a decision can be audited.

## Checklist

- [ ] A decision was asked through `decide` before it went to the lead.
- [ ] The task was closed through `complete`, with `--base` and `--test`, and it had acceptance criteria.
- [ ] Nothing about credentials or spend was decided by a seat.
