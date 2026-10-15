# The seat guard judges the command, not the heredoc text inside it

### Fixed
- `rust/agenthub-runtime/assets/seat-guard.ts`: every bash rule now reads the command through `commandOf`, which drops heredoc bodies. The rules split on newlines, so a body line that began with `tmux` (or held `git push`) refused an unrelated call: fe-dev's `4genteam room queue close ... --note "$(cat <<'EOF' ...)"` was blocked by `tmux-mutate` although it ran no tmux.

### Testing
- 7 commands driven through the real hook with a fake `pi` under node: a heredoc that mentions `tmux kill-server` and `git push` passes, and a bare `tmux kill-server`, `git push`, `docker ps` and a `tmux kill-server` placed after a heredoc are still blocked; `env -u TMUX tmux -L s ...` still passes. `cargo test --offline seat` passes.
- Not covered, as before: a tmux call inside a script fed through a heredoc to an interpreter. The hook reads only the command text, and the heredoc body is no longer part of it.
- Reaches a seat at its next start.
