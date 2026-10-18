## A runnerless seat's freshness comes from its own tool log

### Fixed

- `agenthub_client/rust/agenthub-runtime/src/supervisor/mod.rs` (`last_tool_call_at`, `seat_facts`):
  `last_active` was the state document's `updated_at`, and a runtime without a runner (agy) writes that
  document once at launch and never again. The owner's meter therefore read a seat as silent for 28 hours
  while it was closing gate rows: measured on 4genthub-min's `reviewer` on 2026-10-18, document stamp
  `08:02:52Z` against a last logged tool call at `08:49:22Z`.
- The seat's own `tool-calls.jsonl` - which the guard appends one line per tool call for every runtime -
  is now the second source of its freshness stamp, and the newer of the two wins. Only the stamp moves:
  the activity word stays `Unknown` for a runtime that publishes none, so nothing new authorises a reseat.
- The read is the file's 8 KiB tail (the log is append-only and was 1.4 MB after ten days), the fragment
  a mid-file window starts on is dropped, and an absent or unreadable log leaves the stamp exactly as the
  document gave it.

### Testing

- `cargo test --manifest-path agenthub_client/rust/agenthub-runtime/Cargo.toml supervisor::tests`:
  61 passed. The new case writes a launch document plus a >8 KiB tool log and pins both directions - the
  tool call moves the stamp forward, a newer document is not dragged back - and that `activity` stays
  `unknown`.
