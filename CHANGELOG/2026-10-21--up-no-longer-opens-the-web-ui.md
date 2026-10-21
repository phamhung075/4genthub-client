## `up` no longer opens the web UI

### What was wrong
Every `4genteam up` opened `AGENTHUB_UI_URL` (default `http://localhost:3800`) in a browser, with no way to turn it off: an empty `AGENTHUB_UI_URL` fell back to the default. A respawn runs `up`, so each respawn opened another browser tab.

### Changed
- `internal/clientlifecycle/lifecycle.go`: `up` ends with the watch view and herdr; the `openUI` call is removed. The summary and help say so and name `4genteam ui`, which still opens the UI on demand.
- `skills/client/watch-team/SKILL.md`: the lifecycle line no longer lists the UI.

### Tested
- `TestUpInitialisesEveryServiceInOrder` now wants the sequence without `openUI`; its stub records the call, so the test fails on the old `up` (`order = [... watch openUI herdr]`) and passes on the new one.
- `go vet ./internal/clientlifecycle/`, `gofmt -l internal/` clean, `go test ./...` all ok.
