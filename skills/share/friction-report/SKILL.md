---
name: friction-report
description: Use when a seat hits friction - a missing tool, a constraint, a slow or failing step, or a rule that blocks good work. File one report through the existing feedback channel and continue; the room tracks that same text as an item tagged `finding` with its layer in the body - the daemon's tag list is closed and has no `friction` tag, so never create one. Never a silent workaround.
signals:
  - command: submit_feedback
---

# Friction report

When something blocks good work, report it once and keep moving. The channel already exists; this skill
defines what to say, not how to send it.

## When

Any of: a tool you needed was missing; a constraint slowed or blocked a step; a step failed or was slow; a
rule blocked work that would otherwise be right. Do not work around it silently - an unrecorded workaround
hides the fix.

## What to do (one report, then continue)

1. Send the report through the shipped channel:
   `4genteam feedback --layer <layer> --text "<body>"`, or the `submit_feedback` MCP tool when the runtime
   has no CLI. **Do not invent a second channel, a form, or a new route.** The `layer` vocabulary is closed:
   `runtime | client | cloud | seat-context | workspace | other` - pick the layer where it actually broke.
2. Make it trackable: the room's view is the same text as one item tagged `finding`, carrying the `layer` you
   chose in the body (the queue row or board item your room uses). `finding` is on the daemon's closed tag list
   (`internal/clientruntime/queue.go`, `QueueTags`); `friction` is not, and the daemon refuses a create that
   carries a tag off that list - so create the `finding` item and never invent a tag. One report, two views -
   the channel for the owner's reading, the tagged item for tracking. It is the room's way of tracking the
   report, not a second obligation.
3. Continue the work. If the rule is genuinely wrong, say so in the report; do not break it silently.

## The body (fixed skeleton, inside --text)

```
tried:     <what you were doing>
blocked:   <what stopped or slowed it>
cost:      <time, tokens, blocked work, or risk>
fix:       <your suggested fix, if you have one>
severity:  <blocking | costly | annoying>
```

Keep it to these five lines. Everything else is the reader's inference; leave it out.

## Boundaries

- Seat-to-seat transport is owned by `comm-guard-skill`; the durable-item vocabulary by `queue-handoff`.
- For a reader's format, `token-economy`.
- A report is not an escalation of the work itself. If the item you are on cannot proceed, that is a
  `queue-handoff` `blocked_on` / `escalation`; file the report *and* park the item.
- Do not report the same friction twice; if it is already tracked, add a line to the existing item.
