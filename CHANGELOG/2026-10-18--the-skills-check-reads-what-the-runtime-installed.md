## The skills check reads what the runtime installed: F1-F5, `up`, `doctor` and `reseat`

`ai_docs/core-architecture/seat-skills.md` section 6, ported onto the runtime-install model. The client
delivers nothing (the ruling's 2026-10-17 note), so the check no longer compares against a client delivery:
it compares each seat's INSTALLED tree with the tree the runtime installed from.

### Added

- `internal/clientskills`: the resolver the ruling's layout needs - `share/`, then `rooms/<room>/`, then
  `rooms/<room>/<seat>/`, never `client/`; a directory holding `SKILL.md` is a skill and one without is a
  seat folder; a name two levels both deliver is refused by name. `RootEnv` is `AGENTHUB_CLIENT_SKILLS`,
  the runtime's own variable (`install_seat_skills` in `rust/agenthub-runtime/src/supervisor/mod.rs`), so
  the check and the install cannot resolve different trees. There is no delivery here and no writer.
- `internal/clientlifecycle/skillscheck.go`: the ruling's five codes as one function `up`, `doctor` and
  `seat reseat` share. F1 compares a seat's observed cwd with the worktree
  `clientservices.DerivedWorktrees` names; F2/F3/F4 read `omp skill list --json` with the seat's
  `PI_CODING_AGENT_DIR` against the resolved set and `<roomDir>/seats/<seat>/skills`; F5 hashes the
  installed copy against the tree's. A skill loaded from outside the installed tree (the platform's own
  delivery) is not a finding; one that sits in the installed tree and is not in the set is F2. Every input
  the caller could not obtain is a `note` line naming it, never a fabricated code.
- `clientservices`: the `seats` row and its one failing state `skills_stale`.
- `doctor`: the `seats` row per room, `running` or `skills_stale`, with its lines beside the row, its
  reasons in `--json`, and the failures on stderr when the row is down - the notes print there whether or
  not any row is down.
- `up`: the same check after the room's safeguards are up and before the watch view. Failures refuse the
  verb; the check starts, restarts and signals nothing.
- `seat reseat`: after each restart, the restarted seat's checks, reported as lines and never as a refusal.
  A seat the runtime does not report answers with that fact rather than an empty list.

### Not ported, with the input that is gone

- **F6 guide version.** No rendered guide and no pinned `agent.yaml` exist on this line: the renderer's file
  half is retired and a seat's role is the seat-tier skill.
- **F7 forbid rules.** The client-side policy table was deleted so the hub's render is the only source;
  `4genteam policy render --check` is that comparison and it fetches the render. Porting F7 would restore
  the duplicate table the ruling removed.
- **The claude probe.** It asserted the client's plugin delivery (`seat:`-prefixed skills from
  `<agent dir>/skills-plugin`), which is deleted, and the client still builds no claude argv. A non-omp
  seat gets a note that F2-F4 have no reader, and F5 is still decided.

### Measured on the live room

`doctor 4genthub-min` now names real drift: five seats' cwds are hand-made worktrees (F1, lead and go-dev
included), the installed `seat-worktree` and `short-output` copies carry a pre-change description (F5),
and `fe-dev`'s installed `taste-skill/SKILL.md` declares `name: design-taste-frontend`, so omp loads
`design-taste-frontend` (F2, the name the set does not carry) and never loads `taste-skill` (F3). The
installed copy is byte-identical to the tree's - F5 is silent on it, correctly - so the defect is in the
tree's own frontmatter, not in a stale install. Row line `4genthub-min seats: skills_stale`, 16 reasons
on `--json`, exit 3.

### The two lines a reader acts on

- **F1's refusal names the way out**, because on a rig whose seats run in hand-made worktrees it refuses
  `up` every time and that is the rule working: `... is not its worktree <path>; reseat it
  (\`4genteam seat reseat <room> --seat <seat>\`) or start it so its worktree is derived`.
- **F2 names the FILE**, not only the directory: `skill "<name>" loads from <filePath> in the installed
  tree, but the tree declares no skill by that name` - an installed `SKILL.md` that declares a name
  differing from its directory is the case the line exists for.

### Tests

`go test ./...` green, `gofmt` clean, `go vet` clean. New cases: the resolver's levels and its two
refusals; F1-F5 each; a platform skill is not F2; a runtime with no reader is noted and F5 still decided;
a room-level note is attributed to the room and is not one per seat; `SeatCheckLines` answers for the named
seat only and names a seat the runtime does not report; the row is `skills_stale` and counted as down.
`doctor`'s fixture stubs the seats row the way it already stubs the orphan row, and `reseat`'s fixture
pins the checks after each restart.
