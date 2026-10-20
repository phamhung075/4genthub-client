# Test Suite Changelog

Track test suite changes, fixes, and improvements for the client.

## 2026-10-19 - the watch feed has a timing gate (branch 4genthub-min/perf-06-feed-timing)

- `internal/clientwatch/feed_timing_test.go` (new): `TestFeedWritesEachLineWithinOnePoll` starts
  `runFeed` against a temp state root and a fake tmux, waits for the startup replay, then appends
  one distinguishable event line to the seat's session log and requires the rendered marker at the
  writer within 2 x `feedPollInterval`, carried by a `Write` of its own. A buffered writer in front
  of `out` holds the line until its buffer fills or the process ends, so the bound fails; GREEN in
  1.0 s on 2026-10-19.

## 2026-10-20 - the tmux server's environment is an allowlist (branch 4genthub-min/sec02-tmux-env)

- `rust/agenthub-runtime/src/pty/mod.rs` (changed): five tests added to `launch_env_tests`, one per
  mechanism the ruling names, and they drive real tmux servers on a root of the test's own:
  - `a_server_started_here_inherits_only_the_allowlist` starts a server and reads
    `show-environment -g`: every key it carries must pass `server_env_allowed`, and
    `DEEPSEEK_API_KEY` set in this process must not be one of them.
  - `a_launch_cleans_the_servers_environment_and_the_seats_session_copy` seeds a credential on an
    existing server (`HOST_LEGACY_TOKEN`), launches a seat, and requires the global environment
    cleaned, the session's own copy free of denied and seat keys, while the pane still reads its
    `-e` value; skipped only where `/proc` is absent, since the pane half IS that measurement.
  - `the_server_allowlist_admits_plumbing_and_refuses_a_seat_s_keys` is the predicate's unit test:
    host plumbing and the `LC_` family pass, `AGENTHUB_TOKEN`, `DEEPSEEK_API_KEY`,
    `TYPESAFE_API_KEY`, `AGENTHUB_CONNECTOR_TOKEN`, `AGENTHUB_URL`, `COMPACT_THRESHOLD` and
    `HERDR_STATE_ROOT` fail.
  - `only_the_set_form_of_show_environment_names_a_key_to_clean` pins the parser: `KEY=value` names
    a key, `-KEY` does not, and `unset_global_args` emits one `set-environment -g -u` per key.
  - `the_cleanup_names_the_key_and_not_a_pair` pins that tmux refuses `NAME=value` to
    `set-environment` and takes the two-argument form, which the cleanup depends on.
- `rust/agenthub-runtime/src/main.rs` (changed): `the_credential_is_removed_by_the_server_and_never_by_a_wrapper`
  drops its `denied_name_keys()` expectation with the function; the same guarantee is now read from
  the allowlist and from the pty tests above.
- GREEN on 2026-10-20 after the rebase onto `c21e702`: `cargo test --offline` in
  `rust/agenthub-runtime` - 192 + 1 + 20 + 1 passed, 0 failed; `launch_env_tests` 12 passed,
  the five above among them, in 0.3 s.
