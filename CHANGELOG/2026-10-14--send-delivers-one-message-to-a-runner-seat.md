## 2026-10-14 — `send` delivers one message to a runner seat

### Fixed
- `4genteam send` (and `agenthub-runtime send`) pasted the `From:/To:/Sent:/---/body` envelope into the seat's tmux
  pane. A runner seat's pane input is the runner's own line reader, which turned every line into a separate
  prompt, so the agent's first prompt was the `From:` line alone. The SendText IPC now goes through
  `RoomSupervisor::deliver_text`: a seat with a runner (omp, claude-code) gets one `text` frame in its mailbox
  and the runner's own answer (`outcome: "relayed"`); the runtimes with no runner keep the bracketed tmux paste.

### Changed
- `rust/agenthub-runtime/src/supervisor/mod.rs`: `deliver_text`, `seat_runtime`.
- `rust/agenthub-runtime/src/ipc/mod.rs`, `src/main.rs`: SendText and the `send` command call `deliver_text`.
- `internal/clientruntime/sendline_test.go`: the `relayed` outcome reads as a plain "Sent to" line.

### Tested
- Rust: `a_multi_line_message_reaches_a_runner_seat_as_one_text_frame` (86 pass).
- Go: `go build ./... && go vet ./... && go test ./...`.
