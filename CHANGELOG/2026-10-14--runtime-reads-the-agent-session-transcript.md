## Runtime: transcript path and per-session tmux env

### Fixed

- `supervisor::transcript_path` is the newest `*.jsonl` in `<room>/seats/<seat>/sessions` and is refused when there is none.
  The dead `logs/<seat>.jsonl` creation is removed.
- `pty`: each tmux session gets its own env (`-e`), not the tmux server's.

### Tested
`cargo test` in `rust/agenthub-runtime`: `the_transcript_reads_the_newest_agent_session_and_refuses_when_there_is_none`,
`launch_env_tests::each_session_gets_its_own_env_not_the_servers`.
