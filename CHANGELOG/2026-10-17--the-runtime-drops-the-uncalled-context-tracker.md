# The runtime drops the uncalled ContextTracker and assemble_context

## Removed

- `rust/agenthub-runtime/src/context/mod.rs` and `mod context;` in `main.rs`. The module defined
  `ContextTracker` (150k/250k/300k token thresholds), its `estimate_tokens`/`assess_threshold` pair, and
  `assemble_context`, a four-tier string builder - and nothing called any of it: `grep -rn` over `src/` and
  `tests/` found only the module's own `#[cfg(test)]` cases, and the three `DEFAULT_*` constants plus
  `CompactionAction` were referenced nowhere outside the file. The compaction policy actually in use lives in
  the supervisor, and the resume brief is the context builder (task 4/8), so this is a deletion, not a
  migration: there is no second consumer to keep alive.

## Fixed

- `rust/agenthub-runtime/src/supervisor/mod.rs`: the multi-line delivery test's scratch directory name shrinks
  from `deliver-one-frame` to `deliver`, so the reply socket it binds
  (`…/rooms/deliver/run/dev-deliver-<pid>.reply-<pid>-4.sock`) fits under `SUN_LEN` on this box's `/tmp`. The
  fixture, not the product, was over the limit (109 bytes against the 108-byte `sun_path`), and the failure
  blocked the acceptance command for any change that runs the suite.

## Verified

- `cargo test --offline` exits 0: 171 passed, 0 failed.
