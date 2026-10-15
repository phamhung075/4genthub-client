# An agy seat runs under the same guard and tool-call log as an omp seat

### Added
- `rust/agenthub-runtime/assets/seat-guard.ts`: run as `node seat-guard.ts --agy-hook pre|post`, it is agy's tool hook. It reads agy's payload on stdin, maps `run_command` to bash and `view_file` to read, applies the same rules (env-secret, docker, tmux-mutate, push), answers `{"decision":"deny"|"allow"}` before a tool and `{}` after one, and appends the same line to `<seat dir>/tool-calls.jsonl`, so `4genteam activity` shows an agy seat. omp never passes the flag and loads the default export as before.
- `rust/agenthub-runtime/src/supervisor/mod.rs` `install_agy_guard_hook`: starting an agy seat adds a `seat-guard` entry to agy's global `~/.gemini/config/hooks.json` and touches nothing else in the file (another tool's `herdr` hook stays as it is). A second install is a no-op, and a file that is not a JSON object is refused, not overwritten.

### Why the entry answers for sessions that are not seats
- agy reads that file for every session on the machine, and an answer with no `decision` is a refusal (measured: with `{}` both `ls` and `view_file` were denied by the hook). The command therefore runs the guard only when `AGENTHUB_SEAT` is set; any other session gets `{"decision":"ask"}` before a tool, which is agy's own default (measured: `ls`, allowed by the user's saved rule, and a workspace `view_file` both ran with that answer), and `{}` after.

### Testing
- `cargo test --offline` passes (105), including a test that the entry joins an existing file, leaves the other hook intact, is idempotent and refuses a non-object file. The hook was driven by hand with four payloads (a push, an `ls`, a `.env` read, a failed call with a token in it): the two refusals were denied and logged with their rule, the token was redacted. A scratch agy run measured the `ask` and `{}` answers above.
