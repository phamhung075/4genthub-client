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
  refuses a policy that names another seat instead of handing a seat the wrong links.
- `4genteam sync respawn` is implemented in Go (`41335a4`, `internal/clientsync`): it launches a
  fresh occupant only for a seat that holds the dead reading for `--after-seconds` (default 30s),
  and it decides on the seat's own state rather than on `rig seat launch`'s exit code, because a
  launch can start the occupant and still exit non-zero. A live or an unknown seat is refused
  without launching anything.
- `4genteam feedback` is implemented in Go (`e0a9b98`, `internal/clientfeedback`): the friction
  channel's second door is native now - the flags are parsed in-process, the body is marshalled by
  `encoding/json` and sent in one POST to `/api/v2/openrig/feedback`, so no `.sh` file, no `python3`
  and no `curl` is needed. The layer is validated before any dial, the identity defaults come from
  `rig whoami --json` (read lazily, so a fully-specified call needs no rig at all), and
  `$AGENTHUB_TOKEN` is sent as a bearer token that is never printed. Deviations from the script,
  named rather than hidden: the URL default is `$AGENTHUB_URL`, this client's one server variable,
  instead of the script's MCP/public chain; any 2xx counts as stored; and a missing credential or URL
  is refused before dialing at exit 2.
- `4genteam sync rig` is implemented in Go (`4214f18`, `internal/clientsync`): it fetches the room's
  rigspec, pulls every listed seat under the same pinning rules as `sync pull`, then builds the rig
  root in a staging directory and swaps it in, so a file the operator placed there while it
  materializes survives and is named on stderr. Two of the Python's own-team defaults are gone: the
  per-room `.env` link now points at the client's ONE env file and is made for any member whose model
  is written `provider/model`, and the per-seat policy table is a seam a governed rig supplies instead
  of a compiled seat-role table.
- `4genteam sync bundle` and `4genteam sync switch` are implemented in Go (`3b5d6ec`): bundle builds
  `<room>-<seat>-<hash8>.rigbundle` from the LOCKED pin with the Python's exact `rig bundle create`
  arguments (the source label is this tree's own), warns about the seats a rig root leaves unpinned,
  and exits 2 when there is no pinned snapshot; switch records the new occupant in the cloud first,
  applies a model change with `rig seat set-model` (or stops and relaunches fresh with
  `--apply restart`), prints the manual steps for a runtime change because OpenRig cannot swap one in
  place, and exits 0 with `applied=none` when rig is missing or the seat is absent from `rig ps` -
  the cloud change is the authority, and only the local half is outstanding.
- Every verb answers `--help` (`5cb9206`): one decision in `internal/clientcmd` replaced the per-verb
  flag loops and closed the last five verbs that treated `--help` as an unknown flag - `bridge run`,
  `bridge once`, `sync pull`, `sync status` and `sync messages` - and the one name that still refuses,
  `seatcheck`, answers help too. `sync watch` is dropped from the `sync` group: Python's argparse
  registers no such verb under sync, so the name is gone rather than advertised, and the group's usage
  now describes only what this build implements.
- `skills/`: this repository's skills tree, in four kinds — `share/<skill>` for every seat of every
  room, `rooms/<room>/<skill>` for one room, `rooms/<room>/<seat>/<skill>` for one seat, and
  `client/<skill>` for an LLM running the client. A directory holding `SKILL.md` is a skill; a
  directory without one is a seat folder. Thirteen skills come across from the main repository's
  `.agents/skills` (its `.claude/skills` entry is a symlink to it), de-privatised and corrected for the
  Go client; `README.md` and `docs/AGENT-SETUP.md` describe the tree and how a skill reaches a seat.
- `internal/clientskills`: the client delivers the skills it owns. The tree is found at `--skills-root`,
  else `$OPENRIG_CLIENT_SKILLS`, else `skills/` beside the binary; a seat receives `share/`, then its
  room's room-level skills, then its own seat-level skills — so a more specific copy replaces a shared
  name — and never another room's, another seat's or `client/`'s. Installing copies each skill whole
  into the seat's own skills path (`<agent dir>/skills/<id>/`, the directory its agent spec already
  points at) and declares the ids in that spec's `resources.skills` and in every profile's
  `uses.skills`, because OpenRig projects only the skills a profile selects.

### Changed

- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md`: the `--seat` row is the exhaustive set now —
  `feed` and `inputs` take it repeatably as a filter, and `input` takes it once as the seat to send
  to, where it is required — so the row no longer names a set one verb short.
- `docs/AGENT-SETUP.md`: the install-target passage names the profile half too — the same render that
  fills `resources.skills` also lists each id in the default profile's `uses.skills`, so a pool entry
  no profile uses is inert exactly as a file no spec declares is.
- `docs/WATCH.md` and `skills/client/watch-team/SKILL.md` (both tables, so the mirrors stay mirrors):
  the options gain `--seat` — `feed` and `inputs` only, repeatable — and `--cols` is stated as `grid`
  and `watch` only, matching the help text the code prints; `--state-root` is confirmed at the tip.
- `docs/AGENT-SETUP.md` names the install target for a published skill: the per-agent skills
  directory the seat spec's `resources.skills[].path` points at, with the module's `id` declared in
  that spec, because a copy alone is inert — the runtime loads what the spec declares.
- `skills/client/watch-team-grid/SKILL.md` is folded into `skills/client/watch-team/SKILL.md`, which
  survives, in the client kind's generic voice. Every lesson of the grid playbook is in it: the
  per-pane invocation and header, the `grid: <n> seats in <cols> columns` line, the checks to run
  when the view stays silent, the `--cwd` fact, the compaction ladder with the supervisor restart,
  the `rig-continue.sh` hook, the zero-size-file restore trap and the backtick warning. The
  `watch-team-grid` directory is gone, and the skill's own options table carries `--state-root` as
  `docs/WATCH.md` now does.
- `docs/WATCH.md` names `--state-root` in its options table: where the session logs are read from
  is the flag, else `$OPENRIG_STATE_ROOT`, else `~/.openrig/state/omp`.
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
