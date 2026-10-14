## 2026-10-14 — the `forcecompact` helper is removed

### Changed
- `compact-run` compacts a seat through the runtime daemon (`ForceCompact` on the runtime socket, the same call
  `4genteam room compact` makes) instead of exec-ing the `forcecompact` helper. The helper needed
  `CAP_SYS_PTRACE` and only knew omp; the runner's mailbox serves every runner runtime.
- `internal/clientlifecycle`: `Supervisor.Forcecompact`, the `New` parameter, the `--forcecompact` flag, the
  `4GENTEAM_FORCECOMPACT` variable, `ForcecompactPath` and the `up` step `ensureForcecompact` are gone.
- `README.md`, `docs/AGENT-SETUP.md`: the helper's instructions are removed.

### Removed
- `rust/forcecompact/`.

### Fixed
- `4genteam up` no longer prints "run once: sudo setcap cap_sys_ptrace+ep .../forcecompact" or the
  "forcecompact helper is not on PATH" line.

### Tested
- `go build ./... && go vet ./... && go test ./...`; a live `4genteam room compact` on a running room.
