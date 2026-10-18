## The seat-worktree skill says how a client tip becomes reachable

### Changed

- `skills/share/seat-worktree/SKILL.md`, new section "Making a CLIENT tip reachable before naming its sha":
  a worktree's `agenthub_client` is a separate clone (`.git` points at
  `~/__projects__/4genthub/.git/worktrees/<worktree>/modules/agenthub_client`, its own objects and origin),
  so a commit made there is not an object in `~/__projects__/4genthub/agenthub_client` and the lead's
  `cat-file -t` / `log --all` there correctly reports it missing. The section carries the fix - a local
  `git fetch <worktree module gitdir> <branch>:refs/heads/<branch>` into the canonical repo, which writes
  refs and objects only - and the proof commands to run in the repo the lead reads.
- Measured 2026-10-18: the three client tips of the day (`8e521ed`, `4c51e56`, `b47ba91`) read as missing
  until that fetch, and the lead gated on a checkout that could not contain them.

### Testing

- Docs and skill text only; no code path changed. The commands were run as written: the fetch reported
  `* [new branch] 4genthub-min/queue-transition-actor -> 4genthub-min/queue-transition-actor`, and
  `git -C ~/__projects__/4genthub/agenthub_client cat-file -t 8e521ed` answered `commit`.
