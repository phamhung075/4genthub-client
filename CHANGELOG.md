# Changelog

Notable changes to this repository, in the [Keep a Changelog](https://keepachangelog.com/)
format. Versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- The Go client becomes this repository's own module, and its binary is `4genteam` (13ccbdc).
- `CHANGELOG.md`, this file.
- `docs/WATCH.md` and `skills/watch-team/SKILL.md`: how to watch a running room — the seat grid,
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

### Changed

- `.env.sample` states the one-credential, one-file contract: `AGENTHUB_URL` and `AGENTHUB_TOKEN`
  in `~/.config/4genthub/.env`, with a variable already in the environment winning over the file.
  It carries no token value, and every key it lists is one the client reads.
