# The five seat roles live in the seat tier

A seat's role reached it as a rendered file: the hub's `seatrenderer` turned the seat type into
`guidance/role.md` (plus `agent.yaml`, `startup.files` and a skills list), and the render's file half is
retired - nothing wrote those files into a seat's launch directory, measured on all five seat dirs of
`4genthub-min` (no `agent.yaml`, no `guidance/`, no `role.md`). Ruling
`seats/architect/RULING-seat-context-delivery-model.md` R1/R3: the render is a resolution record, and the
per-seat role ports into the one channel that is already implemented, populated and delivered - the client
skill tree's **seat tier** (`skills/rooms/<room>/<seat>/`), the tier the runtime resolves for that seat
alone and installs under the seat's own `skills/` at every start.

### Added

- **Five seat-role skills, one per seat of `4genthub-min`**, each carrying the seat type's role text, its
  rules and its output format - what `guidance/role.md` used to deliver for that seat:
  `skills/rooms/4genthub-min/<seat>/<seat>-role/SKILL.md` for `lead`, `architect`, `go-dev`, `fe-dev`,
  `reviewer`. The texts come from the hub's `seat-types/{lead,architect,developer,reviewer}.yaml` (the room's
  five seats run four types: `go-dev` and `fe-dev` are both `developer`), read at the seed version the hub
  stamps today, `1.4.12` (`seedmap.go`). Each frontmatter carries `name`, `description` and
  `seat-type-version: 1.4.12`, and the body names the hub file it was ported from, so the seat-facing copy
  says which record it is a copy of.
- **`skills::tests::a_seat_resolves_its_own_role_skill_and_no_other_seats`**, which reads the shipped tree
  through `SkillResolver::resolve_for_seat`: for each seat, its `<seat>-role` skill resolves, at tier
  `Seat`, from `rooms/4genthub-min/<seat>/<seat>-role/SKILL.md`; the frontmatter parses, names the version
  and a description; the body still carries the type's role text, one of its rule names and its output
  format; **and no other seat's role skill is in the set**. Landmarks beside the assertion: the share tier
  still resolves in the same call, `fe-dev`'s own `taste-skill` stays theirs, and `go-dev`'s set does not
  carry it.

### Measured

- **"Cleaned of retired verbs" is vacuous for these four types, and that is checked, not assumed.** The four
  role texts name exactly two commands, `4genteam whoami` and `4genteam send` (both live; `whoami` is
  hidden from `4genteam --help` but answers), and none of the ten entries in `internal/retiredverbs`'
  `RetiredCommands` appears in any of the five texts. The generator that produced them printed every
  backticked command span it emitted.
- **The two `developer` seats share one contract text.** `go-dev` and `fe-dev` run the same seat type, so
  their skills differ only in the seat name and the description's first words. The seat-specific lanes are
  not lost: they are the room's own `guide-go-dev` / `guide-fe-dev` overlay blocks, which land in AGENTS.md,
  and they stay there.
- **A colon in an unquoted frontmatter scalar is a SILENT failure.** The first draft's description read
  `The lead seat's contract: own coordination ...`; `serde_yaml` refused it and
  `SkillResolver::parse_skill_file` returned `Ok((None, content))`, so the skill resolved with no metadata
  and nothing complained - the new test failed with `lead-role carries no frontmatter`, which is how it was
  found. The parser swallows a frontmatter syntax error by design; a skill whose metadata matters is only as
  good as a test that asserts it is `Some`.
- The remaining occurrences of the retired names in this tree are mentions that say they are retired, the
  captured production baseline under `fe-dev/fe-4genthub-driven/baseline/` (recorded page text, not read as
  a claim), and history in `CHANGELOG/` and `CHANGELOG.md`.

### Testing

- `cargo test --offline` in `rust/agenthub-runtime`: **150 unit + 15 `seat_guard` + 1 + 1 = 167 passed, 0
  failed** (149 unit before this change; the one added is the seat-tier test).
- **RED, measured by moving `skills/rooms/4genthub-min/lead/lead-role` away and re-running**: `lead: no
  skill named lead-role; the seat tier holds []` - the failure names the seat and what the tier did hold.
- `go vet ./...` rc 0; `gofmt -l internal/ cmd/` clean; `go test -count=1 ./...` all packages ok, 0 FAIL
  (including `internal/retiredverbs`, `internal/clientskills`).

### What this does not do

- It does not delete the hub's file half: `seatrenderer` still renders `agent.yaml`, `startup.files`, the
  skills list and `guidance/role.md` until its own lane lands. Until then the seat reads this skill AND a
  `role.md` the current render hands the routes - the content is the same text twice, which is the state an
  R1-only landing leaves.
- It does not touch the seat types' `module_refs` (18 of the 24 slugs have no directory anywhere in this
  repo). Those refs are the hub's seed data and its own hygiene lane.
