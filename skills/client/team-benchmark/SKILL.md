---
name: team-benchmark
description: Benchmark a running room over the last 24 hours in one command - cost per seat and per merge, spend split, cache and compaction model, branch waste, where each seat's work goes, and the effect of the last team change (before/after). Use when the owner asks to "benchmark the team", "what does the team cost", "did the change help", "measure 24h", before and after any team change, or once a day to improve the team.
---

# Benchmark the team

One command measures what each seat costs and produces, and whether the last team change helped.
Read-only on the room: it reads the room's turn metrics (`4genteam room metrics`), each seat's
`tool-calls.jsonl` and `git`, never `.env` or a token. All the arithmetic is in the script.

## Run it

```bash
python3 ~/.claude/skills/team-benchmark/scripts/benchmark.py <ROOM> [--hours 24]
```

That is the whole procedure: run it, read its lines (~25), act. **Do not re-derive its numbers with
ad-hoc `jq` or `python`**; a number that is missing belongs in the script, so report it as a gap.

| Flag | Use |
| --- | --- |
| (none) / `--brief` | the short report below; what agents run |
| `--full` | adds tool-call shares per seat, commits per repo, every activity per seat, branch names |
| `--json` | one compact JSON object, for scripts |
| `--change "<text>"` | records a team change (marker only, no measurement) |
| `--repo PATH` | project repo to count when the room keeps no worktree of it under `wt/` (repeatable) |
| `--metrics FILE` | read a saved `room metrics --print` file instead of calling the binary |

Every run appends its full numbers as one line to `<state-root>/benchmarks/<ROOM>.jsonl`; change
markers go in the same file as `{"kind":"change","ts":...,"text":...}`.

Repos: the hub and the client are found from the script's own checkout; a project repo is any other
repo with a git worktree under `<state-root>/rooms/<ROOM>/wt/` (also inside a seat's hub worktree,
e.g. `wt/lead/aa2-rs`). Each repo's log covers its main checkout's HEAD plus each room worktree's
HEAD, so unmerged seat branches count. `--full` keeps the per-seat tool-call shares (`chat` =
queue/send, `board` = `manage_task`, `git`, `write` = edit/write, `test`, `read`, `shell`) and the
per-repo commit lines (non-merge commits, merges, heads, added lines by kind: code, test, docs).

## Read it

Brief output, line by line:

1. **Room line**: spend and spend per day, turns, merges into main and **usd per merge**, team
   commits, **hit ratio** (`cache_read / (cache_read + tokens_in)`), **spend split** (output / miss /
   hit share of the fitted cost) and the fitted prices per 1M tokens.
2. **One line per seat**: turns, usd and its share, usd/turn, output and miss tokens per turn,
   **coord** (queue-poll + queue-write turns / turns), compactions, and the two activities that cost
   most.
3. **Compaction line**: count, median peak, `S` (median context after), init (median first-turn
   context of a session), `C` (median miss tokens on the first turn after a compaction), `g` (mean
   positive per-turn growth, median beside it), `w_r`, then `T_opt` against the current peak with
   the relative cost at the peak, at 250k and at 500k, and the break-even against the cycle length.
4. **Waste line** per project repo: of the branches whose tip was committed in the window, how many
   are scratch: named `gate-*`, `proof-*`, `tmp-*`, `*-rebased`; already in main (no non-merge commit
   whose patch is not in main); or duplicates (their patch-ids are carried by another branch).
5. **Before/after**: the last run before the latest change marker against this run, with deltas,
   plus `WARN` lines when less than 24h have passed or two or more changes fall in between.

What good looks like:

- usd per merge falls while merges per day hold. Turns or spend alone are not the goal.
- Hit ratio above 0.95; below that something rebuilds context (respawns, compaction storms).
- Output share is the largest part of spend on DeepSeek flash; it moves with reasoning level and
  reply length, not with context size.
- Coord share under ~20% per seat; a lead above that relays instead of deciding in batches.
- Compaction cost x1.0-1.1 at the current peak; a trigger far above `T_opt` costs more per turn.
- Waste near 0. A branch already in main or a duplicate is paid work that produced nothing new.
- **`notok`** (in `--full`) mixes real errors with hook blocks, which are the guard working. Classify
  `outcome` and `error` in `tool-calls.jsonl` before sending a seat a correction.
- A seat that never writes (lead, reviewer) is judged on coord share and on whether its gate or
  relay is one row in and one verdict out.

## The cost model

**Prices.** The script fits USD per 1M tokens by least squares of `cost_usd` on `(tokens_in,
cache_read, tokens_out)` over every turn. DeepSeek flash (aa2-rs, 2026-10-28, off-peak): miss 0.15,
hit 0.003, out 0.60. `w_r = hit / miss = 0.02`: one cached token costs 2% of a missed one.

**Compaction.** Per seat, turns sorted by time; a compaction is a turn whose context
(`tokens_in + cache_read`) falls below 0.6x the previous turn while the previous carried more than
40k. After it the seat restarts at `S` (init + summary), and its first turn pays `C` miss tokens to
rebuild the cache; then the context grows by `g` a turn up to the trigger `T`. Average input cost per
turn, in miss-token units:

```
f(T) = w_r * (S + T) / 2  +  C * g / (T - S)
       (cached context, re-sent every turn)   (one rebuild per cycle of (T-S)/g turns)
T_opt = S + sqrt(2 * C * g / w_r)
```

**Why "compact only pays when the saving G exceeds 98% of the rebuild I" is wrong.** That looks at
one request: compacting costs `C` once and saves `w_r * G` on that request. But every later turn
re-sends the context, so the saving `w_r * G` recurs on every turn of the cycle. Break-even is
`C / (w_r * G)` turns (~13 at aa2-rs) against a cycle of `(T-S)/g` turns (~39), so compaction pays
about three times over. Raising the trigger to 250k or 500k makes every turn re-read a larger
context: x1.8 and x3.3 the optimum's input cost at aa2-rs.

**Levers, ranked by measured spend** (largest first):

1. **Waste work**: scratch, duplicate and re-done branches, gate re-runs. Whole turns for nothing.
2. **Coordination turns**: queue-poll and queue-write are ~21% of turns; batch them, poll less.
3. **Output tokens and reasoning level**: output is ~47% of spend; shorter replies, lower effort
   where judgement is not needed (`short-output`).
4. **Summary size**: `S - init` is the compaction summary, paid as context on every turn after it.
5. **Init size**: guides, skills and tool lists loaded at session start, paid on every turn.

Already done, do not count them again: off-peak scheduling (`offpeak-gate`), prompt cache (hit ratio
~0.98), the flash model.

## The change protocol

1. **Baseline**: run the benchmark before the change (it is the "before" row).
2. **Mark**: `benchmark.py <ROOM> --change "<what changed, commit or file>"` right when it lands.
3. **One change per 24h**, unless the owner orders a bundle; two markers between the compared runs
   make the cause not separable, and the report warns.
4. **Rerun after >= 24h**; an earlier run is printed with a `WARN` and is not a verdict.
5. **Keep** the change when usd per merge drops without fewer merges; **otherwise revert it by
   commit** (`git revert <sha>`), mark the revert with `--change`, and note the numbers.

## Worked example: aa2-rs, 2026-10-28, 24h

- Spend $6.14 (director's hand count $6.09) over 7366 turns, 4 seats, all deepseek-flash. Split:
  output 47%, miss 29%, hit 24%; hit ratio 0.976. Fitted prices miss 0.15, hit 0.003, out 0.60.
- usd per merge: $6.09 / 31 merges = $0.20 (director baseline); the script's window gave 27 merges,
  $0.23.
- Lead $2.08 of $6.14 (34%); coord 29% of its turns (queue-write 389, queue-poll 279) against
  code-edit 226 turns ($0.21); measuring-bash 554 turns ($0.51) is its largest activity.
- 162 compactions (director's count 165). Peak ~92k, after `S` ~32k = init 15.9k + summary ~16k,
  `C` 16k miss, `g` 1.5k/turn (median 1.0k). Working room `T - S` ~60k, at the optimum: `T_opt` 81k
  with the mean `g` (the director's hand estimate with a larger `g` gave ~97k), current cost x1.01,
  250k x1.8, 500k x3.3. Break-even 13 turns of a 39-turn cycle: keep the compaction trigger where it
  is; spend lies in output and coordination, not in context.
- Waste: 6 of 12 branches active in the window were already in main (one named `tmp-combine`).

## Daily routine

1. Run the benchmark (brief). Read the before/after block first.
2. If a change is pending its 24h verdict, apply the change protocol's keep/revert rule.
3. Otherwise take the largest lever from the ranking that the numbers show, and send its owner one
   `guide` or `finding` row naming the seat, the metric and its value.
4. Make at most one team change, and mark it with `--change`.

## Checklist

- [ ] One command run; no number re-derived by hand.
- [ ] The run covered at least 24 hours since the last change marker (no `WARN`).
- [ ] At most one change marker between the compared runs.
- [ ] The finding names a seat, a metric and the before/after values.
- [ ] Keep/revert decided on usd per merge with merges held.
- [ ] Hook blocks were not sent to a seat as a fault.

## Commits to this skill

A commit that changes this skill carries a `Skill-Evidence:` trailer naming the measurement or
request behind it, e.g. `Skill-Evidence: none - director request from the founder 2026-10-28, aa2-rs
cost review`.
