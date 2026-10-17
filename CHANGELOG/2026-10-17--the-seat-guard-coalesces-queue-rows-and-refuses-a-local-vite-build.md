# The seat guard coalesces queue rows and refuses a local vite build

## Added

- `rust/agenthub-runtime/assets/seat-guard.ts`: two rules. A seat's second pending queue row to the same destination and tag within 10 minutes is refused, and `vite build`, `npm run build` and `npx vite` are refused for a seat (local frontend builds are not allowed).
- `rust/agenthub-runtime/tests/seat_guard.rs`: tests for both rules.

## Tested

- `cargo test --offline` in `rust/agenthub-runtime`: all pass (seat_guard 18). Reviewer PASS on tasks 1a099da6 and 5ac4a259.
