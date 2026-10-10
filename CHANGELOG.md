# Changelog

Notable changes to this repository, in the [Keep a Changelog](https://keepachangelog.com/)
format. Versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- The Go client becomes this repository's own module, and its binary is `4genteam` (13ccbdc).
- `CHANGELOG.md`, this file.
- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md`: how to watch a running room — the seat grid,
  the lead window, the input panes, the option list and the rules. Generic, with placeholders, so
  it works in any project; the skill is the same procedure packaged for a project's skills path.
- `docs/AGENT-SETUP.md`: setting a machine up for the 4genthub MCP, OpenRig and herdr, then the
  procedure an agent follows to build a room and its seats. The README now links it, the watch doc
  and the watch skill.
- `4genteam team apply`, `import-project`, `publish-skills` and `drift-check` are implemented in Go
  (`f7a64c7`, `internal/clientteam`), with a Python-compatible JSON emitter because the cloud
  compares module content byte for byte. `--team`, `--inventory` and the library root are required
  rather than defaulted.
- The lifecycle and per-seat verbs are implemented in Go (`2307e2b`): `up`, `compact`,
  `compact-run`, `stop`, `status`, `log`, `ui`, `seat status|sync|watch` and `policy show|apply`,
  plus the one env file (`internal/clientenv`) and the seat-log reader the supervisor and the watch
  view need. Every verb that takes a rig requires it, the seat-role table is data from the team
  file, and the log directory is the XDG state location instead of the checkout.
- The `team` and `watch` verb groups are registered in the binary's dispatcher (`b311b86`).
- `4genteam watch`, `grid`, `feed`, `inputs` and `input` are implemented in Go (`5fd1216`,
  `internal/clientwatch`), with `docs/WATCH.md` as the contract: `--rig` is required, the state root
  is `--state-root`, else `$OPENRIG_STATE_ROOT`, else the machine's own `~/.openrig/state/omp`, and a
  machine without herdr or without a readable state root exits non-zero with one message. The input
  panes send with `rig send`, never a raw tmux attach.
- The supervisor's witnessed-compaction path is tested (`internal/clientlifecycle`): a new
  compaction record sends the seat the resume text, and an unwitnessed one is reported and retried
  rather than assumed.
- `4genteam sync install-checker` is implemented in Go (`internal/clientsync`): it builds
  `cmd/seatcheck` with the pinned store compiled in, installs it at `<out>/bin/seatcheck`, links it
  where seats resolve it with a relative symlink, and refuses when the NAME still does not resolve
  rather than leaving behind a binary nothing can call.
- `4genteam sync offline-install` is implemented in Go (`internal/clientsync`): it reads a
  materialized bundle's `rig.yaml` and copies each member's pinned `policy.json`/`pinned.json` into
  `<home>/.openrig/agenthub-seats/<room>/<member>/`, which is the one place `seatcheck` looks, and
  refuses a policy that names another seat instead of handing a seat the wrong links. `respawn` is
  the remaining verb Python's argparse registers that this build still refuses by name with exit 3.

### Changed

- `.env.sample` states the one-credential, one-file contract: `AGENTHUB_URL` and `AGENTHUB_TOKEN`
  in `~/.config/4genthub/.env`, with a variable already in the environment winning over the file.
  It carries no token value, and every key it lists is one the client reads.
- `skills/watch-team/SKILL.md` moves to `skills/client/watch-team/SKILL.md` unchanged, byte for byte
  (the same sha256), as the first skill of the tree's client kind. `README.md` and the two entries
  above now name the new path.

### Fixed

- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md`: the three `inputs` examples pass the `--rig` the
  verb requires.
- `4genteam watch`, `grid` and `feed` now resolve the state root exactly as the doc comment and
  `docs/WATCH.md` promised: `--state-root`, else `$OPENRIG_STATE_ROOT`, else the machine's own
  `~/.openrig/state/omp` (`clientenv.DefaultStateRoot()`). The third clause was documented but
  missing, so the feed refused on a machine where the doc says it reads. The usage text now states the
  home-relative default (`~/.openrig/state/omp`) rather than only the variable name - the resolution
  stays absolute - and a root that is named but not there is refused by its path whichever clause
  named it.
- `4genteam sync --help` prints the verb group's usage and exits 0; it used to be read as an
  unknown verb and exited 2 (`1e23ca2`).
- `4genteam bridge install-service` renders the client's OWN environment file
  (`clientenv.DefaultPath()`, `~/.config/4genthub/.env`) instead of a Python-era name nothing ever
  wrote, so the installed systemd unit and the running bridge read the same file by construction.
