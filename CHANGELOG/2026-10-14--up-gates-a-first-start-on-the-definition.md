## 2026-10-14 — `up` gates a first start on the team definition

### Fixed
- A first `4genteam up` has no node records, so the peak gate proceeded with "the gate did NOT run" and a
  DeepSeek seat could start at the peak rate. `up` already holds the definition it is about to start, so the gate
  now decides on the definition's own seats when the room has no records. A definition that declares a
  `deepseek/...` model is refused at peak before the room is touched. `compact` and the restore verbs keep the
  records-only reading and its "did NOT run" line, since they hold no definition.

### Changed
- `internal/clientlifecycle/lifecycle.go`: `peakGate` takes the declared nodes; `PeakGate` is the records-only wrapper.
- `internal/clientlifecycle/offpeak_gate_test.go`: a refusal test for a first start with a DeepSeek seat; the
  no-records `up` test now asserts the gate decided.

### Tested
- `go build ./... && go vet ./... && go test ./...`.
