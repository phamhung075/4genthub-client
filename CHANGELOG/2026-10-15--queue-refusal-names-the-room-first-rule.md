# `room queue`: a qitem id in the ROOM place says the room comes first

### Fixed
- `internal/clientroom/queue.go` `queueArgumentRefusal`: `4genteam room queue show qitem-...` (the old `rig queue show <id>` habit; every seat did it at 19:29Z) answered only "a QITEM_ID is required". It now says the argument is a queue item id and that the ROOM comes first (the seat's room is `$AGENTHUB_ROOM`).

### Testing
- `go test ./internal/clientroom` passes, with a new case for the message and one for the plain refusal.
