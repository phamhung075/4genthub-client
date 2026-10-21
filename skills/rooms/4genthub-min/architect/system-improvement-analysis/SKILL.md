---
name: system-improvement-analysis
description: Use when the architect takes a friction cluster and turns it into a ruling, a tool change or a skill edit with a stated size and builder.
---

# System improvement analysis (architect)

Triage names a cluster; this turns it into a decision the team can execute.

## Inputs

A friction cluster - a deduped group of reports, or one line from the ecosystem review - with the evidence the
reports carry.

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

## Drafting a fix

A census row that names a failure is drafted by the seat its `refs` name, off-peak.

1. `4genteam offpeak --quiet` - exit 1 means stop; the draft waits for the window.
2. `4genteam skills evidence <room> --json`, and take the one row: its level, state, window, the fired
   signal with its occurrences and refs, and the skill's own sentence that signal governs.
3. One `deepseek_agent` job, read-only, with the brief's six fields (skill, state, fired, rule, text, job).
   It returns only the new file content, and changes no rule the fired signal did not name.
4. Save it, then `4genteam skills propose <room> --skill <name> --change fix --from <refs> --draft <file>`.
   The proposal is an uncommitted diff in your own worktree; hand it to `reviewer` with the cite.
5. No draft, no edit: a fix with no cited refusal is not the loop's business.

### The brief

One text block, the six fields in this order, pasted as the `deepseek_agent` job:

```
skill: <level>/<name> at <path>
state: <state> since <window sha>
fired: <signal> x<occurrences> (<refs, up to 10>)
rule: <the skill's own sentence this signal governs, quoted with its line>
text: <the skill's current SKILL.md, or the section named by the rule>
job:  propose the smallest edit to `text` that would prevent `fired`; keep the skill's voice;
      change no other rule; return ONLY the new file content
```

## Boundaries

- The architect rules; the development seat builds. Do not write the fix, or the code.
- Do not widen the cluster: fix what the reports measured, and name what is out of scope.
