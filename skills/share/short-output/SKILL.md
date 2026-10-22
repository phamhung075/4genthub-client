---
name: short-output
description: "Keep a seat's thinking and replies short to save output tokens. Use on every turn: think in pointer lines, answer result-first, never paste large output. Complements token-economy, which covers documents and messages between seats."
---

# Short Output

Output tokens are the expensive ones. Spend them on the next action, not on narration.

## Thinking

- Think in **pointer lines**: `goal -> file:line -> next step`. One line per fact or step.
- Do not write out a complete chain of reasoning, restate the task, or list options you will not pursue. Pick one and go.
- Think longer only when a wrong move is hard to undo (delete, migrate, push, config of a running service).

## Reply

1. **Result first**: done / blocked / verdict, in the first line.
2. Then at most **5 short lines**: what changed (paths), what was verified (command), what is open.
3. No preamble, recap, thanks, or closing offer. No restating the question.
4. Evidence stays in a file or a command the reader can run; the reply carries the path and a one-line result.

## Tool output

- Read the part you need: `grep -c`, `grep -m`, `head -n`, `sed -n 'A,Bp'`, `--json | jq` on one field. Never `cat` a large file or log.
- Do not echo output back into the reply. Quote only the line that decides the next step.

## Work first

Build, fix and improve. Ideas and findings go through the queue as one short row. The commit message is the change record (owner ruling 2026-10-21): what was wrong, what changed, how it was tested. No docs-only commit unless the owner asks.

## Never shorten

Negations (`not`, `never`, `no`, `only`), numbers and units, paths, commands, error text, owner-approval status, blockers, and the exact scope of an irreversible action. A dropped negation costs more than every token saved.

See `token-economy` for documents, MCP tasks and messages between seats.
