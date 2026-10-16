# Delete the unused Rust PolicyEngine

### Changed
- Removed `rust/agenthub-runtime/src/rules/mod.rs` (`PolicyEngine`) and `mod rules;` in `src/main.rs`. Nothing called it (`grep PolicyEngine` found only its own file and tests); the live guard is `assets/seat-guard.ts`.
- Removed `PermissionPolicy` from `src/types.rs` (its only user was the engine) and the `regex` dependency from `Cargo.toml`/`Cargo.lock`.
- `scripts/team/4genthub-min/policy-*.json` is untouched; the owner decides on it.

### Testing
- `cargo test --offline` green (125 unit, 5 guard, 1 umask); `go vet ./...` and `go test ./...` clean.
