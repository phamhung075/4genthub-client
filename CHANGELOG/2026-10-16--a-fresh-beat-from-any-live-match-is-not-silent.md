## 2026-10-16

### Fixed
- `internal/clientservices/services.go` `verifyRow`: a heartbeat whose pid is any live process matching the safeguard's pattern now reads running. It was compared with the first match only, so a second or overlapping `bridge run` made a fresh beat read `silent` at age 0s.
- `internal/clientservices/services_test.go`: added the case of two live matches with the beat from the second.

### Tested
- `go test ./internal/clientservices/`.
