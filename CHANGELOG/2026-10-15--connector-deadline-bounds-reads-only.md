# Connector: the drain's 1 ms deadline no longer expires the next write

### Fixed
- `internal/clientsync/connector.go` `connectorConn.SetDeadline`: it called `net.Conn.SetDeadline`, which sets the read AND the write deadline. `AgentDaemon.drain` sets 1 ms to poll without blocking, so the write deadline stayed expired and the next ping or events frame failed with `write tcp ...: i/o timeout`. Every connection died about a second after its handshake (33 of 33 reconnects at the 2026-10-15 restart), so the agent never held the cloud socket. It now sets the read deadline only.

### Testing
- `internal/clientsync/connector_test.go` `TestSetDeadlineBoundsReadsOnlyAndLeavesWritesOpen` fails on the old line with `write pipe: i/o timeout` and passes with the fix. `go test ./internal/clientsync` passes.
