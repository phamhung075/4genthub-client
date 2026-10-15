# Legacy client names purged; routes at `/api/v2/`

### Changed
- Every server route drops the old client sub-prefix and is called at `/api/v2/...`; the module-store tests' path parsing shifted one segment (`internal/clientteam/{fake,importproject,publish}_test.go`).
- The feedback layer sent by `clientfeedback` is `client`.
- `rust/agenthub-runtime`: the env deny list is `["RIGGED_"]` only; docs and tests follow.
- The bridge idempotency prefix is `bridge:`; `4genteam --help` header reads "the local 4genthub client"; retired-verbs list drops the daemon entry; docs, skills and changelogs lose every mention of the old client.

### Testing
- `go vet ./... && go test ./...` and `cargo test --offline` (103 passed) pass.
