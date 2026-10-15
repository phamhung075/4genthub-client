## 2026-10-15

### Changed
- The upload credential is separate from the MCP key. `4genteam agent` and `4genteam sync connector` read `AGENTHUB_CONNECTOR_TOKEN` (scope `sessions:write` only); `AGENTHUB_TOKEN` stays the MCP and API key. A leaked upload token can no longer read or change tasks.
- `internal/clientlifecycle/agent.go`, `internal/clientlifecycle/doctor.go` (`up` names a missing connector token before starting the daemon), `internal/clientsync/connectorverb.go`, `internal/clientsync/redaction.go` (the new name is redacted), `.env.sample`, `README.md`, `docs/AGENT-SETUP.md`.
- No fallback: a machine without `AGENTHUB_CONNECTOR_TOKEN` is refused, not served with `AGENTHUB_TOKEN`.
- Tested: `go test ./...` and the new `TestMissingCloudEnvNamesTheConnectorTokenSeparately`.
