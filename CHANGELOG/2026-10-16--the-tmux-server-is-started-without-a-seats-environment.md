# The tmux server is started by a client that carries no seat's environment

### Fixed

- **The room's tmux server published the seat's credentials on its own command line, and this closes it.**
  A tmux server is created by whichever client reaches it first, and the server KEEPS THAT CLIENT'S ARGV for as
  long as it runs: measured on the live `4genthub-min` on 2026-10-16, `/proc/17057/cmdline` (mode `-r--r--r--`,
  world-readable) held `DEEPSEEK_API_KEY=`, `TYPESAFE_API_KEY=` and `AGENTHUB_TOKEN=`, and it held them because
  the first seat's `new-session` had created that server. The same process's environment is `-r--------` and owned
  by the same uid every seat runs as, so a seat could already read those three keys; what the argv added is the
  CROSS-USER half - `cmdline` is readable by any local user, `environ` only by this runtime's own uid. What is
  wrong is the argv, not the key - see
  `2026-10-15--the-connector-token-leaves-the-seat-the-argv-and-the-tmux-server.md`, which named it as the bigger
  remaining half).
- `rust/agenthub-runtime/src/pty/mod.rs`: `launch_session` now calls a new `ensure_server()` FIRST, which runs
  `tmux start-server ; set -g exit-empty off` through `tmux_command()` when no server answers for this root. The
  creating client's argv is that command, so the server's argv is `tmux / start-server / ; / set / -g /
  exit-empty / off` and every seat's `-e KEY=VALUE` pairs live only in that seat's own client, which is gone
  within the launch. The `-e` list itself, `pane_env_args` and the deny filter are untouched: `-e` is still what
  gives a seat its own identity instead of the server's, and the deny list still drops the credential class out
  of it.
- **`exit-empty off` is what makes the bootstrap hold, and its consequence is a decision rather than a side
  effect.** Under tmux's default an empty server exits as soon as its last client disconnects, so a
  `start-server`-only bootstrap leaves nothing behind and the next session-creating client starts the real server
  from its own argv (measured here: a bare `start-server` on a fresh root had no server a moment later). The probe
  `ensure_server` makes is sound for the mirror reason - the commands it probes with cannot start the server they
  are asking about. On a fresh root both `has-session` and `list-sessions` answer `error connecting to <socket> (No
  such file or directory)`, exit 1 and leave no server behind (measured 2026-10-16 here and independently by the
  reviewer), so the pre-fix `new-session` was simply the creating client, with no earlier creator to blame. Off,
  the empty server waits for the seats. **A stop now leaves an idle server behind instead of none.** The root is
  derived from the state root and shared by every room under it, so no per-room stop may kill it; a server started
  before this change keeps its own `exit-empty` (`on`, verified on the live room's pid 17057), so it still exits
  with its last session and the next launch bootstraps over it. One readout moves with the lifetime: `installchecker`'s
  `seatPath()` takes the PATH a new seat inherits from tmux's global environment whenever a server answers
  (`internal/clientsync/installcheckerverb.go`), so after a stop it now reports `tmux global PATH` where it used to
  report `shell PATH`. That is the tree's own truth rather than the check going wrong - a seat joining the idle
  server really does inherit its global PATH, and a seat's own `-e PATH` still wins for its panes - but it is a
  consequence of the lifetime decision, so it is named here.
- **A server that is already up is left exactly as it is** - not killed, not restarted, its `exit-empty` not
  touched. Whether an older server's argv is clean cannot be read from the launch path, a live server may be
  carrying live seats, and the room's own stop is what ends it: killing it here would be the room-wide destroyer
  this module already refuses to be.

### Testing

- `rust/agenthub-runtime/src/pty/mod.rs`: added
  `launch_env_tests::the_server_is_not_the_process_that_carries_the_seats_environment`. It owns a private tmux
  root, launches one session with `DEEPSEEK_API_KEY` in its env map, finds the root's own server through
  `/proc/<pid>/comm` + `TMUX_TMPDIR` in its environment, and asserts that neither the seat's value nor any
  `(TOKEN|KEY|SECRET|PASSWORD)=` pair is on the SERVER's command line - then asserts the pane still received the
  value, because what moved is which process's argv carries it. **Red first:** with the `ensure_server()` call
  removed the case fails on `the seat's value is on the server's command line, where ps reads it:
  DEEPSEEK_API_KEY=sk-the-owner-would-grep-this`; with it, `cargo test` is **118 passed, 0 failed**.
- `pty::test_servers::own` now returns a `Server` guard instead of a `(MutexGuard, PathBuf)` pair, and its `Drop`
  sets `TMUX_TMPDIR` back to this test's root, runs `tmux kill-server`, removes the root and restores the
  variable. `exit-empty off` means a scratch server no longer exits with its last session, so without this every
  pane test would leave a live server and its directory per run (116 such directories were already lying in
  `/tmp` from earlier runs); the five call sites were updated, and a full suite run leaves none.
- **The end-to-end measurement, on a real daemon and a real room, with a scratch state root and a scratch
  `.env`** (throwaway, deleted; the numbers are kept because the probe is not). One seat, `StartRoom` over the
  IPC socket: **before** (the binary built without `ensure_server`) the server's argv was the `new-session`
  client's - `-e AGENTHUB_URL=… -e TYPESAFE_API_KEY=… -e AGENTHUB_TOKEN=… -e DEEPSEEK_API_KEY=…`, i.e.
  **3 credential-shaped args and 11 `=` pairs**; **after**, the same flow gives `tmux start-server ; set -g
  exit-empty off`, **0 credential-shaped args, 0 `=` pairs**, while the pane's own `/proc/<pid>/environ` still
  carried all four keys. A second seat joining the same room moved nothing (server pid unchanged, still 0), both
  panes kept all four keys, and a `StopRoom` left the server up with the same clean argv - the lifetime
  consequence above, observed rather than assumed.
- **What this cannot do from here:** the live room's running server keeps the argv it was created with. It has
  `exit-empty on`, so a full room stop ends it and the next `up` on this build bootstraps a clean one; until that
  restart the acceptance measurement named in the rail row reads 3, not 0.
