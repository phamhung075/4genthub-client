---
name: improvement-review
description: Use when a skill or tool edit lands that cites a friction - check that it actually lowered the friction it names, with a measurement or an honest "cannot measure".
---

# Improvement review (reviewer)

The loop's closing check: did the change do what it claimed?

## What to check

The edit cites a friction (a report id or a cluster). Compare **before and after** on the figure that friction
carried:

- a missing tool -> it exists, and it is used;
- a slow or failing step -> the same step's cost, measured again;
- a blocking rule -> the block is gone for the case that was blocked.

Reuse the reviewer verdict path already in force for a merge (`seat-worktree`). Do not add a second gate.

## When there is no measurement

Say so plainly: "no measurement was possible; the claim is unchecked". That is a valid verdict, and better
than a pass with nothing behind it. If the friction recurs, that is the evidence.

## Boundaries

- Judge the claim, not the taste: the question is whether the cited friction dropped, not whether the edit is
  elegant.
- Do not create obligations to stay busy (`development-team`): no friction cited, nothing to review.
- One verdict per change, at the merge - not a standing committee.
