## `--via-owner` refuses a seat, and the refusal is the client verb

### Changed

- `internal/clientseat/ownersend.go`: `viaOwnerRefusal` reads the caller's own sender through the same
  resolution the envelope uses (`clientguard.SessionFromEnv`, one identity source) and returns a refusal
  unless it is `<rig>-owner@<rig>`. The message names both names and says what this is - attribution, not
  authentication.
- `internal/clientseat/reseat.go`: the check runs at the `--via-owner` dispatch, before a message file is
  written or a key is typed; the help says only the owner node may ask for the route.
- `internal/clientcmd/command.go`: `ExitRefused = 5`, named rather than borrowed, because a caller reading
  the code must tell "not allowed" from "wrong arguments" (2) and "not available here" (3).

### Tests

- `TestSendViaOwnerRefusesASeat` (new): a seat's sender, another room's owner node, and an empty environment
  each exit the named refusal with the rule on one line, and the node is never touched.
- `TestSendViaOwnerFlagReachesTheNodeRoute` now runs as the node's own shell
  (`OPENRIG_SESSION_NAME=demo-owner@demo`), which is the only environment the route accepts; its assertions
  are unchanged.

Measured: `OPENRIG_SESSION_NAME=demo-go-dev@demo 4genteam send --via-owner demo lead hello` prints one line
and exits 5; the same line with `demo-owner@demo` reaches the node (tmux's own "can't find pane" is the
answer, exit 3), which is the pre-change behaviour.

### Not authentication

A process that sets the variable forges through plain send regardless. This closes the documented route and
a prompt-injected seat, not a hostile local process; `ownersend.go`'s trust sentence stands.

At this change: `gofmt` clean, `go vet` clean, `go test ./...` passes (34 packages).
