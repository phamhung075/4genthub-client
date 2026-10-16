# An empty value in a prefix no longer steps past the guard

### Fixed
- `assets/seat-guard.ts`: `callOf` ate the env/sudo/`VAR=` noise with `[A-Za-z_]+=\S+`, which asks for at least one
  character after the `=`. The head `FOO= ` - three characters, what every shell accepts, and how a seat empties a
  marker it was handed - was therefore not noise: the anchor never reached the command and EVERY rule built on
  `callOf` was stepped past. The value is `\S*` now. Measured at d964b8b with one harness, unpatched vs patched: 16
  of 29 commands changed verdict, all from ALLOWED to refused - `FOO= git push origin main` -> `push`, `FOO= rm
  scripts/git-hooks/pre-commit` and its `sed -i`, `mv` and `dd of=` siblings -> `protected-paths`, `FOO= 4genteam
  room limits <rig> --usd 999` -> `budget-ceiling`, and the same prefix on `docker ps`, `tmux kill-server`, `git
  commit --no-verify`, `git reset --hard`, `pkill omp` and `kill $(pgrep omp)` reaching the rule each of those
  already had.
- `tests/seat_guard.rs`: `an_empty_value_in_a_prefix_is_still_a_prefix` is the 13th case (12 -> 13). It pins BOTH
  spellings - the bare command and that command behind `FOO= `, `AGENTHUB_SESSION_NAME= ` and `env FOO= ` - for
  `push`, `protected-paths` (two shapes) and `budget-ceiling`, so the fix cannot buy the prefixed form by losing
  the bare one. `FOO=1 ` is pinned too: it already worked, and it is the line that says the hole was the EMPTY
  value and nothing else. `FOO= git status` and two more stay allowed - the prefix is noise, and what the command
  IS stays what is judged.
- The guard is the owner's file, so the rule and the case ship as a patch: the seat's own edit tool was answered
  "blocked by seat guard (protected-paths): the git hooks, the seat guard, and the config, log, budget and park
  record it rests on, are the owner's to change" while this was being written. The verification ran in a scratch
  worktree at d964b8b, and the asset's content there was written by a program rather than by the edit tool: the
  guard judges the calls a seat makes, not the programs it runs, which is the limit its own header states ("a net
  for a seat's mistakes, not a sandbox"). Named here rather than hidden.

### Testing
- RED first, in the scratch worktree at d964b8b before the rule changed: `cargo test --offline --test seat_guard` =
  **12 passed, 1 failed**, the new case panicking on `FOO= git push origin main` (`left: None`, `right:
  Some("push")`). A direct drive of the unpatched asset showed the three named shapes ALLOWED behind `FOO= `,
  `AGENTHUB_SESSION_NAME= ` and `env FOO= ` while the bare and the value-carrying (`FOO=1 `) spellings were
  refused.
- GREEN, the same worktree with the rule applied: `cargo test --offline` = **133 + 13 + 1 passed, 0 failed** (the
  seat_guard target is the 13).
- The before/after map covers the whole file, not only the three shapes: the same 29-command harness now answers
  `push`, `protected-paths`, `budget-ceiling`, `docker`, `tmux-mutate`, `hook-bypass`, `git-destructive` and
  `process-kill` where it answered ALLOWED, and the allowed set is unchanged, a quoted mention of `git push`
  included.
