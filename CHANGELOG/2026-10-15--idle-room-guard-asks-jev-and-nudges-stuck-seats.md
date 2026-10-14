# Idle-room guard: Jev decides when the whole room is idle, stuck seats are nudged

### Added
- `4genteam idle-run --rig RIG [--lead SEAT] [--quiet S] [--stall S] [--every S] [--once]`
  (`internal/clientlifecycle/idleguard.go`), a supervised safeguard `roomguard` started by `rigd` next
  to the compaction supervisor and reported by `doctor` (`internal/clientservices/services.go`, `rigd.go`).
  - When every seat has been idle for `--quiet` seconds (default 300), it gives Jev the open rail items
    and each seat's idle time, asks continue or stop once per quiet, logs the decision in the Jev log
    (category `idle-room`), and messages the lead. Below the Jev threshold, or on an error, the lead is
    told not to start new work and to ask the owner.
  - A seat that is not idle but logs nothing for `--stall` seconds (default 900) is interrupted and told to
    continue, up to three times; then the lead is told once.
  - Every automatic message passes the off-peak gate, like the compaction resume.
- A labelled rule in `watch` and `feed` when the next tool call is a different kind from the last
  (`internal/clientwatch/view.go`, `feed.go`).

### Tested
- `go test ./...`; new cases in `idleguard_test.go` and `view_test.go`; the safeguard-list tests now include `roomguard`.
- Not run against a live room: the tmux server was down when the build finished.
