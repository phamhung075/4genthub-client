---
name: team-benchmark
description: Benchmark a running room over the last 24 hours - how much of each seat's work is chat, board, git, writing or testing, what the commits produced (code, test, docs, changelog), and the change against the last run. Use when the owner asks to "benchmark the team", "check the team works not chats", "measure 24h", or once a day to improve the team.
---

# Benchmark the team

Measures whether the seats work or talk. Read-only: it reads each seat's `tool-calls.jsonl` and
`git log`, never `.env` or a token.

## Run it

From the hub root:

```bash
python3 agenthub_client/skills/client/team-benchmark/scripts/benchmark.py <ROOM> [--hours 24]
```

It prints one line per seat (share of calls: `chat` = queue/send, `board` = `manage_task`, `git`,
`write` = edit/write, `test`, `read`, `shell`) and one line per repo (commits and added lines by
kind). Each run appends one JSON line to `<state-root>/benchmarks/<ROOM>.jsonl`; the next run prints
the previous chat and write share beside the new one.

## Read it

- **A seat that never writes** (lead, reviewer) is judged on chat share and on whether its gate or
  relay can be one row in and one verdict out.
- **`notok`** mixes real errors with hook blocks, which are the guard working. Classify the
  `outcome` and `error` fields in `tool-calls.jsonl` before sending a seat a correction.

## Daily routine

1. Run the benchmark; take care that the principal's own queue rows are counted in the seats' chat.
2. Compare with the previous line. A fix is proven only when the metric it targets moves across a
   full 24 hours after the change, not when the first 2 hours look better.
3. Take the one largest overhead and send its owner a `guide` or `finding` row naming the number.
4. Do not change two things between runs; the comparison then names no cause.

## Checklist

- [ ] The run covered at least 24 hours since the last change to seat workflow or skills.
- [ ] The finding names a seat, a metric and the before/after values.
- [ ] Hook blocks were not sent to a seat as a fault.
