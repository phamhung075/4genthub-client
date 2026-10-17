# The prior client's prefix stays denied, one name after the rename

`338f192` purged the prior client's name from the runtime's deny list and left `RIGGED_` alone. The purge's
own subject already said what the list is for - "`RIGGED_` is denied because the client still reads
`RIGGED_SESSION_NAME` as `legacySessionEnv`" - and the reason does not stop at one name: `OPENRIG_*` is what
the prior client's variables addressed its daemon with, and nine of them were measured in an agent's own
environ before `e635e79`. Removing the name from this client's docs and routes does not remove it from the
environments that still export it, and an agent holding `OPENRIG_URL`/`OPENRIG_HOME`/the activity-hook token
can address a rig that is not its room - the exact failure the list exists to stop.

`ENV_DENY_PREFIXES` is now `["OPENRIG_", "RIGGED_"]`: the list is by prefix because the addresses change
names over time and the rename left an alias, which is the doc comment's own argument.

Measured, not assumed: the U3 gate's A4(vi) leg poisons a stub launch with `OPENRIG_HOME`, `OPENRIG_URL` and
`OPENRIG_ACTIVITY_HOOK_TOKEN`, and on `ca8f5f5` the agent the runner spawned carried all three (3 of 3);
with this change it carries none while its own `AGENTHUB_*` identity and `PI_CODING_AGENT_DIR` survive. On
this box tonight no OpenRig daemon runs and `~/.openrig` holds no state, so the flag was latent, not live -
the gate's contract is what it broke, and the contract is the design predicate (§6: no orchestrator's
addressing in any room pid's environ).

### Testing
- `rust/agenthub-runtime/src/main.rs`, `the_deny_list_covers_every_prefix_that_addresses_a_rig`: the denied
  half now names `OPENRIG_URL` and `OPENRIG_ACTIVITY_HOOK_TOKEN` beside `RIGGED_SESSION_NAME`; the kept half
  (`AGENTHUB_ROOM`, `AGENTHUB_SEAT`, `AGENTHUB_SESSION_NAME`, `HOME`, `PATH`, `XDG_RUNTIME_DIR`) is unchanged,
  so the boundary test still fails a blanket wipe.
- `the_credential_is_removed_by_the_server_and_never_by_a_wrapper` still passes: `OPENRIG_` joins the WRAPPED
  class (prefix, unset on the pane's argv), not the exact-name class.
- `cargo test --bin agenthub-runtime the_deny_list` and `... the_credential_is_removed`: 1 passed each.
- U3 A4(vi) with `HARNESS_NO_SCRUB`-shaped poison: 0 OPENRIG_* in the agent's own env dump, 12 AGENTHUB_*
  and 1 PI_CODING_AGENT_DIR intact (the leg's other two assertions).
