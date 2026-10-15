# An agy seat is launched with its model

### Fixed
- `rust/agenthub-runtime/src/supervisor/mod.rs` `agy_command`: an agy seat's pane ran bare `agy`, so the seat's `model` in the room spec was never passed and agy used the owner's default model. With that default out of quota the reviewer took every message into its conversation and never answered (its transcript held user steps and no model step, and its tool-call log stopped at 20:29Z). The pane now runs `agy --model '<model>'`, quoted for the shell line.

### Testing
- `an_agy_seat_is_launched_with_its_model` (plain, quote-bearing, blank and absent model); `cargo test --offline` passes.
