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

### Changed

- `.env.sample` states the one-credential, one-file contract: `AGENTHUB_URL` and `AGENTHUB_TOKEN`
  in `~/.config/4genthub/.env`, with a variable already in the environment winning over the file.
  It carries no token value, and every key it lists is one the client reads.
