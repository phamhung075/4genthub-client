## A malformed skill frontmatter is a named failure, and two live skills were malformed

### The defect

`SkillResolver::parse_skill_file` parsed frontmatter with `serde_yaml::from_str(..).ok()`, so a
frontmatter that was PRESENT but did not parse became `metadata: None`: the skill still resolved, at
its tier, with its body - losing its name, description and `seat-type-version` silently. Measured by a
reviewer's gate: an unquoted `: ` inside one seat-role `description:` made serde_yaml refuse the block,
and the only failing assertion was that test's own "carries no frontmatter" check.

### Changed

- `rust/agenthub-runtime/src/skills/mod.rs`: a delimited frontmatter block that fails to parse is now an
  `Err` naming the skill file and the yaml problem (`<path>: frontmatter does not parse: <e>`).
  `resolve_for_seat` and `collect_skills_in_dir` return `Result<Vec<ResolvedSkill>, String>` and
  propagate it, so a malformed skill can no longer be dropped from a seat's set either - the previous
  `if let Ok(..)` would have skipped it after the parse change. A seat start therefore REFUSES with the
  file named (`install_seat_skills` in `supervisor/mod.rs` propagates with `?`).
- A file with NO frontmatter stays legal and resolves with no metadata, as before.
- The strictness immediately found two live malformed skills, both refused by serde_yaml for the same
  reason (an unquoted colon+space inside `description:`): `skills/share/short-output/SKILL.md` and
  `skills/rooms/4genthub-min/fe-dev/fe-4genthub-driven/SKILL.md`. Both descriptions are now quoted, and
  a YAML sweep of every `skills/**/SKILL.md` reports none malformed.

### Tests

- `rust/agenthub-runtime/src/skills/mod.rs`: `a_frontmatter_that_does_not_parse_is_named_and_never_dropped`
  feeds the resolver a `description:` carrying an unquoted colon+space and requires an `Err` naming
  `SKILL.md` and the parse problem; `a_file_without_frontmatter_still_resolves` keeps the legal half.
  The seat-role-tier case keeps asserting a valid block parses with its name, description and
  `seat-type-version`.
- `supervisor/mod.rs` `scratch()` now writes an isolated `AGENTHUB_CLIENT_SKILLS` into the test state
  root's `.env`: the process environment was the documented fallback, so the suite was reading the
  HOST's tree (the main checkout) and a malformed skill there failed every start the suite performs.
- Run: `cargo test` in `rust/agenthub-runtime` at this HEAD (after merging `main` `cc2fd1c` into this
  branch) = **193 passed, 0 failed** - 173 unit + `held_send` 1 + `seat_guard` 18 + `umask_at_the_entry`
  1; the `skills::` filter alone is 4 passed.
- THE EARLIER "one pre-existing SUN_LEN failure" NOTE WAS WRONG and is corrected here: this branch was 8
  commits behind `main`, and `4cd3ee5` renamed the fixture from `scratch("deliver-one-frame")` to
  `scratch("deliver")`, so the reply-socket path had been short enough on `main` all along. The unit
  count reads 173, not the branch's pre-merge 176, because that same commit deleted
  `rust/agenthub-runtime/src/context/mod.rs` and its 3 tests; nothing was skipped or lost.
