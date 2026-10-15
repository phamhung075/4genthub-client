## The connector token leaves the seat, the pane argv and the tmux server

### Fixed

- **`AGENTHUB_CONNECTOR_TOKEN` left the room through three doors, and this closes all three.** Measured on this
  box on the live `4genthub-min` before the change: `tmux show-environment -g` held the key, every one of the
  five seats' panes carried it in `/proc/<pane_pid>/environ` (5/5), and `ps -eo args` matched it on a live
  `tmux new-session … -s agenthub--4genthub-min--<seat>`. **Nothing a seat does reads that key** - a seat
  addresses its own daemon through `AGENTHUB_URL`/`AGENTHUB_TOKEN` - so it was a credential handed to every seat
  in a room with no consumer.
- `rust/agenthub-runtime/src/main.rs`: the predicate gains `ENV_DENY_NAMES` (exact names) BESIDE
  `ENV_DENY_PREFIXES`, because a prefix cannot name one credential without over-removing: `AGENTHUB_TOKEN` (the
  seat's own MCP credential, named by its `mcp.json`) and `DEEPSEEK_API_KEY` (what its model calls carry) must
  survive. `denied_name_keys()` returns the exact-name keys for the server half; `denied_env_keys()` stays
  PREFIX-ONLY on purpose (next bullet).
- `rust/agenthub-runtime/src/pty/mod.rs`: `pane_env_args()` is the `-e` list with every denied key dropped, so
  the credential cannot ride `tmux new-session -e AGENTHUB_CONNECTOR_TOKEN=<value>` into `ps`; and
  `launch_session` unsets each denied name from the tmux SERVER's global environment
  (`set-environment -g -u`) before it creates the session. That second half is what no client-side scrub
  reaches: a pane takes the server's environment for every key the `-e` list leaves out, and a server that
  already exists keeps whatever it was created with.
- `rust/agenthub-runtime/src/supervisor/mod.rs`: `seat_env()` filters the denied name off the map it returns -
  the one place a seat's environment is decided. This map IS the pane's `-e` list, so a key dropped only
  downstream had already reached the seat's contract.
- **The `env -u` wrapper (already on main, for the prefix class and the two tmux pane variables) is
  deliberately not where the credential is handled.** The wrapper prints the key's NAME on the pane's command
  line for the seat's whole life, and that line is exactly what the owner's measurement greps - a deny list that
  named the credential there would publish it. Prefix keys carry no such cost.

### Testing

- Permanent pins, all deterministic and tmux-free: `the_connector_token_is_denied_by_name_while_a_seats_own_keys_are_not`,
  `the_credential_is_removed_by_the_server_and_never_by_a_wrapper`,
  `the_credential_is_cleaned_by_the_server_and_never_named_on_the_pane_command`,
  `a_denied_credential_never_becomes_a_pane_env_argument`, `a_seats_env_never_carries_the_connector_credential`,
  plus the pre-existing `the_pane_command_unsets_the_tmux_pane_variables_and_the_denied_keys` and
  `each_session_gets_its_own_env_not_the_servers`. The first three pin the two mechanisms APART - the credential
  is cleaned off the server and must never be WRAPPED - because a key routed to the wrong mechanism breaks the
  property the fix is for.
- `cargo test` at the tip: **109 passed, 0 failed**, `TMUX_TMPDIR` pointed at a throwaway directory so the
  room's own server was not the subject. One earlier full run of the same suite failed two pane-spawning
  supervisor tests; both pass in isolation, the same suite is green on re-run, and pristine main is green three
  times out of three - a parallel-tmux race under load, reported rather than papered over.
- **The end-to-end measurement on a real tmux server, deliberately dirtied first** - a throwaway probe, deleted
  before this commit; its numbers are kept here because the probe is not. Server dirtied
  (`server_before=1`), seat env map carrying the credential, one `launch_session`: `server_hits=0` (the server's
  global environment no longer holds it), `pane_hits=0` (the pane's own `/proc/<pid>/environ` holds no such
  key), `argv_hits=0` (neither the value nor the key name is on any command line of that session), and
  `seat_kept=1` (the pane still got its own `AGENTHUB_SEAT`, so nothing was over-removed).
- **What this cannot fix by itself:** the LIVE room keeps what its running runtime gave it. The seats up now
  still carry the key in their environ and on the launching argv until they are relaunched with this build, so
  the acceptance is a measurement after the pin bump and a reseat, not against the tree.
- **NOT fixed, and it is the bigger half:** the same command line carries `AGENTHUB_TOKEN` and
  `DEEPSEEK_API_KEY` in cleartext. Neither can be denied the way the connector token can - a seat's own MCP
  config reads the first, its model calls read the second - so what is wrong is the argv CLASS, not one key in
  it. The proposed shape and its blast radius are in this lane's rail report.
