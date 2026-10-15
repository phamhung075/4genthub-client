## ps never answers `null` where a list is documented, and the state-root knob it still owes

### What changed

`ps --json` prints an empty LIST when it has nothing to report, never `null`, and says on stderr which server it looked
at. `null` was the architect's live repro (F-A): `AGENTHUB_STATE_ROOT=… 4genteam ps leg-solo --nodes --json` printed
`null` while the room was alive and `room status` named its session, so the documented way to learn a session name
returned something that is not a list. The fix is at the JSON boundary itself, where `null` was produced.

### What was tried and measured FAILING, kept so the next unit does not repeat it

The obvious half of F-A - derive the tmux server from the state dir, `<state dir>/tmux`, mirroring the runtime's own
`tmux_root_for`, and set `TMUX_TMPDIR` for the tmux child with `TMUX` removed - **does not redirect tmux on this box**.
Measured directly, not reasoned:

    $ env -u TMUX TMUX_TMPDIR=/tmp/definitely-no-room/tmux tmux list-sessions
    4genthub-min-architect@4genthub-min: 1 windows (created Wed Oct 14 01:34:04 2026)
    …

and the same through the built client: `ps --nodes --json --state-dir /tmp/definitely-no-room` and
`AGENTHUB_STATE_ROOT=/tmp/definitely-no-room ps --nodes --json` both still listed this fleet, with no stderr line.
`$TMUX` was empty in this shell, so the pane variable was not the cause; tmux simply does not follow `TMUX_TMPDIR`
here. The reverted change is not in this commit: shipping a knob whose effect is nil would be worse than shipping none.

The state-root half therefore needs the SOCKET named outright (`tmux -S`, or the runtime's own socket path), not the
tmpdir - that is the owed work, and it is why F-A is released rather than claimed.

### Verified

`gofmt` clean, `go build ./...` rc=0, `go vet ./internal/clientps/` rc=0, `go test ./... -count=1` → 24 packages ok,
0 failing. New test `TestAnEmptyInventoryIsAnEmptyListAndNotNull` pins the JSON boundary; and the built binary prints
`[]` plus `no tmux sessions on the server this process would use` when tmux reports none - measured with a fake `tmux`
on PATH that prints `no server running` and exits 1, which is the shape tmux itself uses.
