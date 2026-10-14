## `send` reports whether the text was submitted, not only that tmux accepted it

### What was wrong
The runtime's `SendText` pasted the text, pressed Enter and answered `{"delivered": session}` whatever the pane did. A seat whose TUI was not reading when the paste landed kept the text staged in its input box, and the sender was told it was delivered. OpenRig's daemon reported an `outcome`; the native path did not.

### Changed
- `rust/agenthub-runtime/src/pty/mod.rs`: `send_text` returns `SendOutcome` (`submitted` / `unconfirmed`). After the paste it captures the pane, presses Enter, and captures again. An unchanged pane gets one more Enter; a pane still unchanged, or one that cannot be read, is `unconfirmed`. `submitted` is an observation that the pane changed, not a receipt from the agent (a spinner also changes the pane).
- `ipc/mod.rs`: the `SendText` answer is `{"delivered": session, "outcome": ...}`. `main.rs`: the `send` subcommand prints `pasted to <session>, submission unconfirmed` for the unconfirmed case.
- `internal/clientruntime/transport.go`: `RuntimeSendLine` prints `Pasted to <session>, submission unconfirmed ...` instead of `Sent to` for an unconfirmed answer; `send` and `DeliverToSession` use it. `--json` already prints the runtime's answer, so it carries `outcome`. The exit code stays 0, as OpenRig's rendered-unconfirmed outcome did.

### Tested
- `cargo test --offline`: 74 passed, 6 new (one Enter on a changed pane, retry then submit, never-moving pane unconfirmed after two presses, unreadable pane unconfirmed, failed Enter is an error, kebab-case serialization).
- `go vet ./...`, `go test ./...`: pass; `sendline_test.go` is new.
- Not verified: the check against a live pane. No tmux server was available; the U3 acceptance run on `cutover-lab` is where it gets measured.
