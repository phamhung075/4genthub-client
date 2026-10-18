## A seat launch names the binary the daemon is running, or refuses

### Fixed

- `rust/agenthub-runtime/src/supervisor/mod.rs` (`launch_executable`, the `Omp`/`ClaudeCode` seat command):
  the pane command was built from `std::env::current_exe()`. When the installed binary is replaced while the
  daemon runs - `scripts/build.sh` does exactly that, with `mv -f`, and prints "restart the daemon to run it" -
  the running process's `/proc/self/exe` reads `…/agenthub-runtime (deleted)`, a string that is not a file.
  Every seat started after that died in the pane within milliseconds and the supervisor reported only `its pane
  is gone`: measured on 4genthub-min 2026-10-18, the daemon started 10:02, the binary was replaced at 10:20,
  and three start attempts (`room reseat`, `room reseat` again, `up --room-only`) failed identically 20-26 ms
  after launch. The pane's whole output, recovered by setting `remain-on-exit` on for one attempt, was
  `bash: -c: line 1: 'env -u TMUX -u TMUX_PANE /home/daihu/.local/bin/agenthub-runtime (deleted) --state-dir
  …'` with exit status 2.
- A launch now uses the resolved path when it is a file, and falls back to `/proc/<daemon pid>/exe` - which
  names the SAME inode after the unlink, and execs (measured: `--help` exits 0) - so a room keeps starting
  seats on the binary its daemon actually runs. When neither path names a file, the start is refused by name,
  with both paths and the remedy (restart the daemon), before a shell is handed a string that cannot exec.
- Why not the installed file: a runner must speak its own daemon's protocol, so the same inode is the
  consistent choice; upgrading is a daemon restart, which stays the documented step.

### Testing

- `cargo test --manifest-path rust/agenthub-runtime/Cargo.toml`: 175 + 1 + 18 + 1 passed. The new case pins
  the three arms - an existing path is used as it is, a stale path yields the `/proc` fallback, and two paths
  that name nothing are a refusal naming both and the remedy.
- Live, in an isolated state root on the room's own server: a daemon started from a copy of the built binary
  launched its seat; the copy was then replaced with `mv -f` (its `/proc/<pid>/exe` read `… (deleted)`); the
  next launch created the pane with `pane_current_command = exe` - i.e. through `/proc/<pid>/exe` - and the
  session survived. The same launch shape with the literal `… (deleted)` path dies at once with status 2.
