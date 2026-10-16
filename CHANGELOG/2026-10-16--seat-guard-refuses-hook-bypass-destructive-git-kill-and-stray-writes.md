# Seat guard: hook bypass, destructive git, kill by pattern and writes outside the seat's worktree are refused

### Added
- `rust/agenthub-runtime/assets/seat-guard.ts`: six rules, judged on commands like the existing ones (quoted words and heredoc bodies are not commands).
  - `hook-bypass`: `OWNER_DOCS_COMMIT=`, `env -u`/`unset AGENTHUB_SESSION_NAME`, `git commit --no-verify`/`-n`, `git merge --no-verify`, any git statement naming `core.hooksPath`.
  - `git-destructive`: `reset --hard`, `clean -f`, `checkout -- .`, `restore .`, `stash drop|clear`, force push, `add -A|--all|.`, `commit --amend`.
  - `process-kill`: `pkill`, `killall`, `pgrep -f`, `xargs kill`, and `kill` of anything but a literal pid that descends from the guard's process or its parent.
  - `frozen-paths`: a write to `NEXT-GEN*.md`, `GATE-*.md` or `ai_docs/reports-status/`.
  - `protected-paths`: a write to `scripts/git-hooks/`, `.git/hooks/`, `seat-guard.ts`, a seat's `hooks/pre/`, agy's `hooks.json`.
  - `worktree-scope`: a write outside `$AGENTHUB_WORKTREE`, the seat's own dir and the temp dir. A write is an edit tool's path, a bash redirect, `tee`, or the target of `cp`/`mv`/`install`/`rsync`.
- `rust/agenthub-runtime/src/supervisor/mod.rs`: the seat's environment carries `AGENTHUB_WORKTREE`, the canonical directory the seat starts in.
- `rust/agenthub-runtime/tests/seat_guard.rs`: runs the guard through `node seat-guard.ts --agy-hook pre`, one test per rule family, and checks that each refusal is logged `blocked` with its rule.

### Testing
- `cargo test --offline`: all green. With the previous `seat-guard.ts` the four new tests fail and the existing-rules test passes.
- Not covered: a command inside quotes or a script (`sh -c '...'`), `sed -i`/`touch`/`rm` as writes, paths built from variables, symlinks. The guard is a net for a seat's mistakes, not a sandbox. The agy edit-tool names (`write_to_file`, `replace_file_content`) are taken from agy's argument names and not checked against a live agy. Live on a seat at its next start and reseat.
