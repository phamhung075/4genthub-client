# The seat tool hook keeps a blocked command whole

### Changed
- `rust/agenthub-runtime/assets/seat-guard.ts`: a blocked call's `target` in `tool-calls.jsonl` is kept to 2000 characters instead of 240, so a block can be judged (rule right or false positive) by reading the command it refused. Other calls keep the 240-character target.

### Testing
- `cargo test --offline the_seat_` passes.
