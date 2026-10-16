# The state root's writes go owner-only, at the process level

### Fixed

- **One call in each process the tree is built from takes the group's and the world's bits out of everything
  created under the state root**: `clientenv.RestrictUmask()` first thing in `cmd/4genteam/main.go`, and
  `restrict_umask()` first thing in `rust/agenthub-runtime/src/main.rs`. A umask is the right instrument here and
  a mode per write site is not, because the state root holds the seats' own words - a transcript is a durable
  copy of whatever a seat printed - and a transcript is written by the AGENT inside a tmux pane, not by either of
  these processes, so no mode set at a call site could reach it. The umask IS reachable, because it is inherited
  by everything they start: the runtime's tmux client, the server that client creates, each pane's shell, and the
  agent in the pane. Measured on the live `4genthub-min` on 2026-10-16, before this change: the daemon and every
  seat ran with `0022`, so `run/<seat>.state.json`, the nudge logs, `rooms/<room>/logs/<seat>.jsonl`,
  `queue.sqlite`, `spec.json` and `seats/<seat>/mcp.json` were `0644`, 138 of 163 seat transcripts were `0644`
  over `0755` directories, and 13712 of 20937 files under the state root were readable by every local user on the
  box - the same root in which 17 files held the live DeepSeek key and 14 the live Typesafe key.
- **Two writers carry their own mode, and a umask clamps neither.** It only decides the bits a `create` leaves
  open; an explicit `chmod` and a file copy both bypass it, so each was fixed where it is decided:
  - `internal/clientservices/services.go`: the safeguard heartbeat was `Chmod(0o644)` - an explicit mode, so it
    stayed `0644` whatever the umask said. It is `0600` now.
  - `rust/agenthub-runtime/src/supervisor/mod.rs`: `copy_dir` copies the seat's skills with `fs::copy`, which
    carries the SOURCE's permission bits across, so the skills landed with the rig's own modes - measured in the
    first artifact probe: every file of the installed skill set at `0644`, the shipped `find-polluter.sh` at
    `0755`, and this was the ONE part of the state root the umask did not reach. The copy now drops the group's
    and the world's bits and keeps the owner's (`mode & 0o700`), so an asset that ships executable stays
    executable for its owner and nothing more.
- `internal/clientenv/umask_windows.go` is a documented no-op: Windows has no umask, who may read a file there is
  decided by ACLs, and this box cannot state that guarantee for a platform it did not run on. It exists so the
  package still builds for windows.

### Testing

- `rust/agenthub-runtime/src/pty/mod.rs`: `launch_env_tests::a_pane_runs_under_the_umask_the_runtime_took` owns a
  private tmux root, takes the umask, launches a session whose command writes its own `umask` to a file, and
  asserts the pane's shell reports `0077`. That is the load-bearing half: the pane and the agent in it inherit
  the mode, so the transcript an agent writes is created `0600`. **Red first:** with the `umask` call inside
  `restrict_umask` commented out the case fails on
  `a pane's command must create nothing the group or another user can read left: '0022' right: '0077'`, and
  passes restored.
- `rust/agenthub-runtime/src/supervisor/mod.rs`:
  `an_installed_skill_keeps_the_owners_bits_and_drops_the_rest` sets the source's bits explicitly (`0644` and
  `0755`, so the case means the same thing whatever the test process's umask is), installs the skill and asserts
  the installed file is `0600` and the installed script `0700`. At this change: `cargo test` in
  `rust/agenthub-runtime` is **120 passed, 0 failed, 0 ignored**.
- `internal/clientenv/umask_unix_test.go`: `TestRestrictUmaskLandsOnThisProcessAndOnWhatItStarts` asserts the
  process's own umask after the call, and that a child process the client starts reports the same one.
  `internal/clientservices/services_test.go` asserts the heartbeat's mode is `0600` where it was `0644`.
  `go test -count=1 ./internal/clientenv ./internal/clientservices ./cmd/4genteam` = 3 packages ok.
- **The artifact-level A/B, on a real daemon and a real room** (throwaway probe, deleted; the numbers are kept
  because the probe is not). A scratch state root, a scratch socket and a scratch tmux root, one seat, `StartRoom`
  over the IPC socket, run once per binary - the installed pre-change binary and this build. **Before:** the
  pane's own shell reported `umask=0022`, and **74** files and directories under the scratch state root were
  group- or world-readable (`rooms/<room>` and its logs at `0755`, `queue.sqlite`, the audit journal, the seat's
  `mcp.json`, every skill file, and the two files the AGENT itself wrote in the pane, all `0644`). **After:** the
  same flow reports `umask=0077` from inside the pane and **2** entries remain readable - the scratch root
  directory and `daemon.log`, both of which that probe created with its own shell (`mkdir -p`, `> daemon.log`)
  and neither of which the runtime wrote. Every path the runtime and the pane created is `0700` for directories
  and `0600` for files, the shipped-executable skill asset included (`0700`), and the tmux socket is `0660` -
  tmux's own mode, with no "other" bits. A second probe with the runtime's own runner (an `omp` seat's path)
  showed the same for the files the runner writes: `run/` `0700`, `run/<seat>.state.json` `0600`, `logs/` `0700`,
  `logs/<seat>.jsonl` `0600`.
