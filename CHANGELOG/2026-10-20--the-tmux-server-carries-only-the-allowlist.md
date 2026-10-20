The tmux server carries the allowlist, and a seat's session env is scrubbed

The tmux server is the one process of a root that outlives every seat, and a variable it holds is a
variable `tmux show-environment -g` prints to any pane of any room on the root AND one every pane
created afterwards inherits into `/proc/<pid>/environ`. `ensure_server` started it through
`tmux_command()`, which removes only the deny list, so it took everything else the daemon held.
Measured here 2026-10-19 on a scratch socket: `DEEPSEEK_API_KEY` and the real `AGENTHUB_TOKEN` on the
creating client's environment landed on the server's global environment, and the session's own `-e`
pairs stayed readable through `show-environment -t <session>`.

Two mechanisms, per `RULING-sec02-tmux-global-env-2026-10-19.md` (option A):

- `ENV_SERVER_ALLOWLIST` in `main.rs`, beside the deny lists: the host plumbing a server may carry
  (`PATH`, `HOME`, `PWD`, `USER`, `LOGNAME`, `SHELL`, `TERM`, `TMPDIR`, `XDG_CONFIG_HOME`,
  `XDG_RUNTIME_DIR`, `LANG`, `TMUX_TMPDIR`) plus the `LC_` family by prefix. `tmux_bootstrap_command`
  clears the environment and sets only those, so a credential added to `.env` tomorrow is out without a
  code change. `PWD` is a member because tmux itself writes it into the global environment from the
  creating client's directory (measured: a client started with `env_clear()` still leaves `PWD` there).
- `clean_server_env` runs on every launch and takes every key outside the allowlist off the global
  environment of a server that already exists - the bootstrap alone cannot fix a server an older
  runtime started. It replaces the exact-name `denied_name_keys()` loop, which is deleted: it was dead
  once the boundary became an allowlist.
- `scrub_session_env` runs after the pane's `PaneRecord` exists and unset every pair `-e` wrote out of
  the SESSION environment. The pane keeps what the kernel gave it at `exec`, so `mcp.json`'s
  `${AGENTHUB_TOKEN}` and the model client's `DEEPSEEK_API_KEY` are still read. A refused scrub is
  logged, never fatal: the seat is already running.

Both halves read one list - `pane_env_pairs` - so a key `-e` writes and the scrub does not name cannot
happen.

Proven: `cargo test --offline` in `rust/agenthub-runtime` - 191 + 19 + 1 + 1 passed, 0 failed. The new
`pty::launch_env_tests` drive real tmux servers: `a_server_started_here_inherits_only_the_allowlist`
starts one on a scratch root and reads `show-environment -g`; the dirty-server case seeds a credential
on an existing server, launches a seat, and asserts the global environment is cleaned while the pane
still holds its own value; `a_denied_credential_never_becomes_a_pane_env_argument` and the credential
tests pin that the `env -u` wrapper never names it.
