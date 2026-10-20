# The safeguard frame's `at` is unix millis, the one time format the socket states

Ruling `RULING-2026-10-21-safeguard-frame-time-and-frontend-names.md` (architect seat), queue row
`qitem-20261020223830-c0797e94406c`, hub task `88d1d01a`.

## The defect

`internal/clientsync/agent.go`'s `publishRows` sent the frame's `at` as
`d.Now().UTC().Format(time.RFC3339)`. The hub reads that field through `wsOptUnixMillis`
(`server/httpapp/ws_mount.go:520`, `ws_connector_safeguard.go:267-280`): integer JSON numbers only, and nil
for anything else. So `room_safeguards.last_beat_at` was written as NULL on every row - a record defect,
not an outage, because nothing reads the column yet (`transitionAlerts` compares state and restarts only).

2.2 states one time format for the socket - unix millis, the shape of `ping.t` - and the hub's own socket
test already sent `time.Now().UnixMilli()` (`ws_room_safeguard_test.go:390`). The producer was the outlier,
and its test pinned the frame's key set only, so it could not see the drift.

## What changed

One line of production code: `"at": d.Now().UnixMilli()`. Nothing else moves. The hub's reader is
untouched, no second format is accepted anywhere, and `UTC()` went with the formatter (it has no effect on
a millisecond number).

Not in this change, by the ruling's split: the frontend comment naming the client's seven names
(`agenthub-frontend/src/types/websocket-protocol.ts:272,276`) is fe-dev's half, and the pin bump is the
lead's.

## Verified

- `go test ./internal/clientsync/ -count=1` -> ok.
- `go test ./... -count=1` in `agenthub_client` -> exit 0.
- `cargo test --offline` in `rust/agenthub-runtime` -> 192 + 1 + 20 + 1 passed, 0 failed.
