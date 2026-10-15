# The seat audit listener survives a lagged burst

### Fixed
- `rust/agenthub-runtime/src/hooks/mod.rs` `start_audit_listener`: the loop was `while let Ok(event) = rx.recv().await`, and a broadcast receiver that falls behind returns `Lagged`, which ended the loop for good, so one burst silenced the seat audit until the daemon restarted. It now logs the missed count and keeps writing; only a closed bus ends it.

### Measured, not changed
- Of the seven `SeatEvent` variants only `SessionStarted` is ever emitted (`supervisor/mod.rs:636`): 70 of 70 lines in `~/.config/4genthub/audit/4genthub-min/*-audit.jsonl` over about 12 hours. `PreTurn`, `PostTurn`, `ToolRequested`, `ToolCompleted`, `ContextLimitReached` and `SessionTerminated` are defined and written nowhere.

### Testing
- New `an_event_is_appended_to_its_seats_audit_file` and `a_burst_that_laps_the_receiver_does_not_end_the_audit`; the second fails with the old behaviour (checked by making the lagged arm break). `cargo test --offline`: 101 passed.
