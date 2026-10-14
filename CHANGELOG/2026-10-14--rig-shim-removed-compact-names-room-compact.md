# rig-shim removed; compact names the runtime's own verb

### Removed

- `4genteam rig-shim` (`internal/clientruntime/command.go`): it installed a `rig` symlink to the client for the
  retired OpenRig CLI. Nothing else in the client reads argv[0] `rig`.

### Changed

- `4genteam compact RIG` without a running rigd also names `4genteam room compact RIG`
  (`internal/clientlifecycle/lifecycle.go`).

### Testing

`go build ./... && go vet ./... && go test ./...` green. Red proof: the two new assertions fail against the
`git archive` of the previous commit.

### Kept on purpose

`rigd`, `bridge`, `doctor`, `status`, `log`, `stop`, `compact-run` are the client's own cloud connector and
safeguard supervisor, not OpenRig; none reads `~/.openrig` or calls the `rig` CLI. The restore brief already
sends through `clientruntime.DeliverToSession(session, text)`, so the 2-vs-3 positional `send` bug does not exist
at this HEAD.
