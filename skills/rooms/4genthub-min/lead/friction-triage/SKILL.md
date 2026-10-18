---
name: friction-triage
description: Use when friction reports are waiting - dedupe them, rank them by cost and frequency, name one fix owner and one size, and close the loop back to the reporter.
---

# Friction triage (lead)

The funnel's decision point. Reports arrive through the shipped feedback channel (grouped by `layer`) and as
items tagged `friction`.

## Steps

1. **Read** the channel grouped by `layer`, and the open `friction` items.
2. **Dedupe on layer + symptom**, never on the reporter: three seats missing the same tool is one item with
   frequency 3.
3. **Rank by cost x frequency**, in the four currencies the report carries: time, tokens, blocked work, risk.
   A one-off cosmetic is dropped without ceremony.
4. **Name one fix owner and one size** - never a committee:
   - client tool (layer `runtime` / `client`) -> go-dev; UI -> fe-dev;
   - a design question -> architect (a decision note);
   - a skill or doc edit -> the seat that owns it;
   - a rule change -> the owner (see `autonomy-charter`);
   - drop.
5. **Open one board task** for a real fix, carrying the report's id.
6. **Close the loop** to the reporting seat - the reporter hears the outcome, or the loop dies.

## Reconciling with the existing ladders

A cluster that is really a lost outcome is a `refocusing` signal, not a tool task. One that needs a human
decision is a `human-in-the-loop` **human gate** and lands as a durable attention item, never a chat line. The
oversight pod's intervention ladder (orchestrator-ping -> refocus -> human escalation) is the fallback when
triage cannot settle it - and a minimum room has no oversight pod: on this room the lead **is** the ladder's
first rung.

## Boundaries

- Triage does not fix. It names the owner and the size, then gets out of the way.
- Do not create obligations for their own sake; a report with no actionable fix is dropped, and said to be.
