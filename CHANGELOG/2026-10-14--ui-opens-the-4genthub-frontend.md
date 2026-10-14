## `ui` opens the 4genthub web UI, and no verb needs the rig tool

### What was wrong
`4genteam ui`, and the UI step of `up`, ran `rig ui open`, which opens OpenRig's own web UI. It was the last verb whose `NeedsRig` was true, so a machine without OpenRig could not run it.

### Changed
- `internal/clientlifecycle/lifecycle.go`: `openUIImpl(stderr)` opens `AGENTHUB_UI_URL` (default `http://localhost:3800`, the frontend this repository serves in development) with the first opener on the machine: `wslview`, `xdg-open` or `open`. A failure is one line of stderr naming the URL and the reason. The opener is a seam (`openURL`). `uiCommand.NeedsRig()` is false; `openUI` no longer takes a rig.
- `README.md`: the `ui` row says the 4genthub web UI.
- `cmd/4genteam/main_test.go`: no verb is governed by the platform matrix now. The matrix half of `TestUsageAndVersionAreTheOnlyFreeCommands` asserts that with no rig on PATH `up` still reaches its own argument check; the refusal loop in `TestHelpIsAnsweredBeforeThePlatformMatrix` (its only entry was `ui`) is removed.

### Tested
- `internal/clientlifecycle/ui_test.go` (new): the configured URL is opened; the default is used when none is set; a failing opener is one stderr line.
- `go vet ./...` and `go test ./...` from a clean `git archive`.
