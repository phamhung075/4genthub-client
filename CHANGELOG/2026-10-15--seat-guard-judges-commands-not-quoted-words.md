# Seat guard: the push, docker and tmux rules judge what is run, not the words in a quoted note

### Fixed
- `rust/agenthub-runtime/assets/seat-guard.ts`: the `push` and `docker` rules matched the text anywhere in the command, so a queue row whose `--body "..."` mentioned `git push` (the reviewer's guard-check report, twice) or an `echo` string with the same words was refused. The three command rules now share `statements()` (heredoc bodies removed, quoted text blanked, split at `&&`, `||`, `;`, newline and pipe) and `callOf()` (the statement's command, after env/sudo/VAR= prefixes, is the named one). `git push`, `git -C x push`, `docker ps` and bare `tmux kill-server` are still refused.
- Not covered, as before: a command inside quotes (`sh -c 'git push'`) or inside a script. The guard is a net for a seat's mistakes, not a sandbox.

### Testing
- Fed 10 payloads through `node seat-guard.ts --agy-hook pre`: real push, `cd x && git push`, `git -C /a push`, `docker ps`, `tmux kill-server` and `env -u TMUX tmux kill-server` are denied; `git status | head`, a queue create with `git push` in `--body`, an `echo "git push ..."` and `python3 -c` are allowed.
