## 2026-10-17 - The queue row body is four fixed lines: facts, rule, decision, next

Task `afe7f042` (Thinking system 3/8) asked for the row body schema every `queue create` follows. The daemon
already types a row - `SUMMARY_MAX_CHARS = 120`, `BODY_MAX_CHARS = 600`, three priorities, `high` requires
`prod-break`, and 14 allowed tags after `status`, `ack`, `checkpoint` and `report` were retired today - but
nothing validates the body's shape, so every body is free prose a destination must read whole.

### Ruled

- The body is exactly four lines, in order: `facts: `, `rule: `, `decision: `, `next: `; one occurrence of
  each key, single-line values, none empty.
- Per-field value caps: facts 280, rule 110, decision 100, next 78. Prefixes and newlines cost 32 chars, so
  `32 + 568 = 600` - a body at every cap is exactly the existing cap, and no conforming row is refused.
- All four fields are required for all 14 allowed tags; the tag changes what the fields say, never which
  exist. A create carries at least one allowed tag.
- `facts:` values are `key=value` pairs joined by `; `, the keys being `4genteam facts` field paths verbatim
  (task `2ad9dc23` owns that vocabulary); 3/8 fixes the container and its limits only, so the two cannot
  drift.
- Summaries keep the 120 cap and begin with the tag's act word (`GATE PASS task ...`, `ASSIGN task ...`);
  that half is convention, unvalidated.
- Six refusals, each naming the rule and the field: missing field, out-of-order/unexpected line, duplicate
  key, empty value, per-field cap, empty tag list. The body is create-only (`queue update` takes no
  `--body`), so the rule governs what may be ADDED; rows written earlier keep their bodies verbatim and no
  compat path exists.

The full ruling - field semantics per tag, the six refusals, options weighed, and one worked example per
allowed tag - is `seats/architect/RULING-queue-row-schema.md` (seat record, not tracked here). Its
`queue-schema-check.py` measures the 14 examples: every summary <= 120, every body <= 600 and inside its
per-field caps, every tag present, and the cap arithmetic exact. Red-proved by breaking a copy - a 126-char
`decision`, an unknown `status` tag, a missing `reseat` example: exit 1.

### Not in this entry

- No code: 4/8 (`cd47f19c`, now depending on this task) enforces the schema in
  `rust/agenthub-runtime/src/queue/mod.rs` and `internal/clientruntime/queue.go`, red tests first.
- The seat-facing skill line (how to write a row) belongs to 7/8; the rail receives it then.
