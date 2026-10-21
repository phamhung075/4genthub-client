# The build refuses a differing live .env

### Added

- `scripts/build.sh` refuses, before it builds anything, when the checkout's `.env` and
  `~/.config/4genthub/.env` both exist and differ (`cmp -s`): it prints the two paths, leaves the live
  file untouched and exits 1. The file was replaced unconditionally before, so a stale checkout copy
  silently undid a token rotation while the build reported success.
- `scripts/build.sh --force` installs the checkout's copy anyway, and says so on stderr. This is the
  deliberate case; nothing else reaches the install line.
- Identical files install as before, a checkout without a `.env` still leaves the live file as it is,
  and a first install still creates it from `.env.sample` (mode 600). The content of either file is
  never printed.

### Why

Owner ruling 2026-10-21 on task `61fbacb5`: `build.sh` installed the checkout's `.env` over the live
one, and a checkout that had gone stale would swap a working credential for an old value. The ruling's
first half needed no change — `bae2c86`'s status-bridge auto-start on every cloud verb is kept as
intended behaviour — and `build.sh` is the only code this change touches.
