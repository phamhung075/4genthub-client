## ps names the room it could not see, instead of an empty list with nothing to read

### What changed

`ps <room> --json` now says on stderr why it printed an empty list when the room matched nothing while tmux sessions
DID exist: `no seats for rig <room> on the server this process would use (N session(s) there, none named <room>-*)`.
An empty list and a verb that cannot see the room read identically on stdout, and that identity is the difference
F-A was reported over - a live room whose inventory came back empty with nothing to distinguish it from a blind verb.

The silence cases are pinned with the reason, because the reason belongs to exactly one case: no room was named, the
server held no sessions at all (`Run` already names that), or the room's seats are right there in the list.

### Fail-before / pass-after, measured

With a fake `tmux` on `PATH` reporting one session for another room, `ps leg-solo --nodes --json`:

- before this change: stdout `[]`, **stderr empty** - the ambiguity.
- after: stdout `[]`, stderr names the rig, the server and the session count.

Against this rig's own live room the verb is unchanged: five seats, empty stderr.

### The state-root half stays owed, and the reason is a runtime one

The measurement that killed the state-root knob (`TMUX_TMPDIR` does not redirect tmux here) was reproduced
independently: `env -u TMUX TMUX_TMPDIR=/tmp/fa-probe-tmux tmux new-session -d -s fa-probe-only` created the session on
the HOST DEFAULT server, and the probe directory stayed empty. What this change adds is WHY the client cannot map a
state root to a socket: `rust/agenthub-runtime/src/pty/mod.rs:19` starts plain `tmux` with no `-S` and relies only on
`own_tmux_root` setting `TMUX_TMPDIR`, so the private server the runtime's own comment promises is not created on this
box - every room's panes land on the host default server, including a room started under its own `--state-dir`. A
client-side map to `<root>/tmux/tmux-<uid>/default` would therefore point at a socket that never exists. The owed half
is a Rust change that names the socket outright; the design choice (runtime `-S`, or an explicit `ps --tmux-socket`) is
with the lead rather than decided here.
