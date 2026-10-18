## The owner route sends from the node's own terminal, and the text goes verbatim

Batch 3 of the supervisor-port table, `9b47d8b 90845c2 410b193 85ce8dc` on `01a2837`. Two of the four are
carried; the other two are superseded by the client main has become, and the reason is written here rather than
the code resurrected.

### Added

- `4genteam send --via-owner` (`internal/clientseat/ownersend.go`) is ruling 4.6's method C. The message goes to
  a private file (0700 directory, 0600 file), ONE short command carrying only file paths is typed into the room's
  owner node, and the result that command wrote is read back and shown. The client writes the owner's name
  nowhere: the signature is the one the node's terminal already carries. Only paths pass through the node's
  command line, so no message text reaches its history and no length limit applies.
- The typed command runs **this client's own `4genteam send`**, not the predecessor's `rig send`: the closed port
  retired that program, and `internal/retiredverbs` failed the build for the literal until it was corrected.
- A node that never answers is NAMED and its files are LEFT — it may still be running the command, so removing
  the message could send nothing and removing the result would throw away the only copy. The refusal says where
  the result will land and never repeats the message text.

### Changed, deliberately

- **`4genteam send` no longer writes a sender into the text.** It prepended `From NAME:` (default
  `owner session`), a CLAIM inside the message, where the ruling requires the sender to come from the
  environment of the shell that ran the send. `--from` is gone with it; a caller that passes it now gets
  `unknown flag --from for this verb; nothing was sent`.
- The help names `--via-owner`, and the RIG SEAT paragraph says the text is sent as typed, with OpenRig's own
  `<unknown sender>` fallback when the environment carries none.

### Superseded, replacement named

- `9b47d8b`'s layout half (`internal/clientsync/rigverb.go`: the owner member appended to `launch_spec`, the
  seat path skipping it) — main has no layout builder at all: `sync rig` is gone, `up` materializes the room
  from the server's spec (`roomSpecFor`), so the client has nothing to append to. The owner node is a room
  member the server's render must carry.
- `9b47d8b`'s `prune` half (`clientservices.IsOwnerNode` read by `staleSeats`) — `RoomSeatsOutsideTeam` and
  `staleSeats` are on main from `4e2a173`, but the owner-node predicate would have exactly one caller and no
  producer on this side; the node's absence is the server's to prevent.
- `410b193` and `85ce8dc`'s `clientpolicy` deny entries (`*OPENRIG_SESSION_NAME=*`, `tmux send-keys*`,
  `4genteam send --via-owner*`) — `internal/clientpolicy` on main RENDERS NOTHING: it states in its own package
  comment that a second table is a second source of truth and that the deny list lives in the hub's seat render.
  Re-adding the table would restore exactly the drift that was retired.
- **Named gap, not fixed here:** the hub render (seedmap 1.4.15) allows `Bash(4genteam send:*)` by name, so the
  `--via-owner` route this change adds is reachable by a seat. The deny row for it belongs in the hub's render,
  with the same scope `85ce8dc` documented (every role; the owner node has no role and no policy). Reported to
  the lead rather than worked around in the client.

### Testing

- `internal/clientseat`: the typed command carries only the paths — never the message text, never a sender
  name — and the result is shown; the private files are gone once it is; a node that never answers is named and
  its files are left; a node that cannot be typed into sends nothing; the text reaches the runtime VERBATIM with
  no added sender (a fake runtime records the `SendText` frame); and `--via-owner` reaches the node route rather
  than being accepted and ignored.
- `gofmt -l internal/ cmd/` empty, `go vet ./...` rc 0, `go test -count=1 ./...` 34 packages ok.
- Smoke, the built binary: `send --via-owner no-such-room-xyz lead "smoke text"` → exit 3, `the message was not
  sent: tmux send-keys -t no-such-room-xyz-owner@no-such-room-xyz failed: can't find pane`; `send --from boss …`
  → exit 2, `unknown flag --from for this verb; nothing was sent`; `send --help` names `--via-owner`.
- NOT RUN: a send out of a real owner node (no room on this box carries one), so the node half is proven against
  a fake terminal whose shell writes the result file.
