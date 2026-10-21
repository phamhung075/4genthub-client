---
name: system-improvement-analysis
description: Use when the architect takes a friction cluster and turns it into a ruling, a tool change or a skill edit with a stated size and builder.
signals:
  - command: 4genteam skills evidence
---

# System improvement analysis (architect)

Triage names a cluster; this turns it into a decision the team can execute.

## Inputs

A friction cluster - a deduped group of reports, or one line from the ecosystem review - with the evidence the
reports carry, and the census line for the skills the cluster touches
(`4genteam skills evidence 4genthub-min --json`).

## Output: one decision note, the architect's usual shape

```
Decision:   <one line>
Context:    <the friction and its measured cost>
Options:    <at least two realistic ones, with their costs>
Recommend:  <one, and what would change it>
Interfaces: <who owns the data; inputs, outputs, errors>
Risks:      <what could go wrong, how it would show>
Handoff:    <the builder and the first step>
```

A cluster that needs no ruling gets the smaller verdict instead: the tool edit or the skill edit, with the file
it lands in. Say which it is.

## Size and builder

State one size - ruling, tool edit, skill edit, doc fix, or drop - and one builder. If the answer is "the
architect later", say that too.

## Boundaries

- The architect rules; the development seat builds. Do not write the fix, or the code.
- Do not widen the cluster: fix what the reports measured, and name what is out of scope.
