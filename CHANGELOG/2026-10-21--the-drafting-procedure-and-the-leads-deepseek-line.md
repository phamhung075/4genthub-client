# The drafting procedure is written down, and the lead's stale DeepSeek line is corrected

Docs only. Two skill files, no code.

`rooms/4genthub-min/architect/system-improvement-analysis` gains **Drafting a fix**: the five steps over the
interfaces that already exist - `4genteam offpeak --quiet` as the gate (exit 1 means peak, stop),
`4genteam skills evidence --json` for the one row, one read-only `deepseek_agent` job on a fixed six-field
brief, `4genteam skills propose --draft` as the only write path (an uncommitted diff in the caller's own
worktree), and the reviewer with the cite. The brief follows verbatim: `skill`, `state`, `fired`, `rule`,
`text`, `job`, with the job's two constraints - change no other rule, return ONLY the new file content.

`rooms/4genthub-min/lead/lead-workflow` said "Path B only: this seat has no `deepseek` MCP entry". That is
false: every seat's `mcp.json` lists the `deepseek` bridge, the lead's included. The line now names the
mounted bridge, both `seat-crew` paths, and the same `offpeak --quiet` gate the drafting step uses, because a
job spends and the gate is the only thing in front of the spend.

No signal text is added to either skill: the census reads both as `no-signal` before and after, so its
counts for them do not move. The decision behind the procedure is
`seats/architect/DECISION-2026-10-21-skill-drafting.md` (slice 1); the widened citation rule it depends on
(slice 0) is go-dev's and is not touched here.
