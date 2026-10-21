# The guard stream reads both producers, and a `guard:` signal can finally fire

The reviewer's S2 verdict passed the windows with one major finding, latent: **GUARD matched no real
producer** - the spool writes `rule: "docs_only"` while every skill a person writes declares `guard:
docs-only`, so the signal was a counter nobody could ever fill.

Two defects, both measured 2026-10-21:

1. **The separator.** The guard has two producers and they spell one rule two ways. The commit wrapper
   (`scripts/git-hooks/git_commit_capture.py`, one `record_refusal` call) writes `docs_only`; the seat guard
   writes `docs-only` and ten other hyphenated kinds into each seat's tool log. The census now folds `_` to
   `-` on every guard rule, so a signal written from the guard's own vocabulary matches both.
2. **The second producer was never read.** `refusals.jsonl` holds only refused COMMITS - the shared checkout
   has no such file at all, and the guard's real volume is the blocked tool calls: 757 lines across this
   room's seats, ruled `git-imports-a-tree` 197, `env-secret` 94, `protected-paths` 85, `worktree-scope` 81,
   `hook-bypass` 76, and five more. Those lines are now guard facts too, read from
   `<state root>/rooms/<room>/seats/<seat>/tool-calls.jsonl` (the path the TOOL stream already reads, and the
   one the runtime's `PI_CODING_AGENT_DIR` names - `seats/<room>/` would have been empty).

The stream stays ONE stream, because a `guard:` signal names a rule and a rule fires wherever the guard
enforces it. It is `unread` only when **no producer file exists** - both are append-on-event files, so an
absent spool and an absent tool log are measured silence, not a gap, and a present-but-empty log is the same.

Two minors from the S1 verdict, both in the record path:

- a queue lookup that FAILED (a refused call, a broken pipe) read as `broken-record`. Only the daemon's own
  `unknown qitem_id` is checked-and-absent now; every other answer is unchecked with its reason, because an id
  nobody could look up is not evidence that a decision is wrong;
- `record.json` accepted a second JSON value after the first. One value per file now: a second value is a
  decision written twice or two pasted together, and reading the first while dropping the rest is how a
  decision goes silently missing.

Verification: `go test ./...` in `agenthub_client` - every package ok, including three new cases (the spool's
`docs_only` matches `docs-only` and keeps the spool id as the ref; a blocked tool line is a fact while an `ok`
line is not; the stream is unread only when no producer file exists) and the two minor fixes. Live over a
throwaway tree whose one `trusted` skill declared `guard: protected-paths` and windowed from an older commit:
`84` occurrences and verdict `demote`, refs `guard:seat architect 2026-10-16T17:07:22.412Z…` - against `0`
and `hold` before this change.
