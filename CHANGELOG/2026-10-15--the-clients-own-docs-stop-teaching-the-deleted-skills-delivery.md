# The client's own docs stop teaching the skills delivery this cut deleted

The cut's fourth commit deleted `clientseat/deliverSkills` and its call in `relaunch`, so the client no
longer delivers a seat's skills and no longer resolves a skills tree. Four documents in this repository
still instructed a reader to rely on exactly that, and one of them was inside the cut itself: the
paragraph at `docs/AGENT-SETUP.md:231-238` at tip `64c4de2` still read "The client also delivers this
tree itself ... the relaunch it performs ... is the step that gives the seat the skills it resolves to",
which is the mechanism commit 4 removed. The sweep that commit 3 ran cannot see it: the paragraph names
no removed symbol, so it is false only RELATIVE to the commit that follows it.

The three other hits are the same class one document over, and each was re-measured against the tip
rather than assumed: `README.md`'s install note told a reader that a seat's restart "refuses until it
finds the tree it delivers to the seat" and prescribed the `--skills-root` flag; `README.md`'s failure
map carried that refusal as a row (`the client's skill tree is not at … (no share/ directory)`). Both
strings belong to `internal/clientskills/tree.go`, which after commit 1 (`rigverb.go`, the only caller,
deleted) is reachable from no path in the module and registers no flag anywhere - `git grep -n
'"skills-root"'` over `*.go` matches only that dead file's own refusal text. `skills/client/install/SKILL.md`'s
failure map carried `seat <seat> has no skills to deliver`, the refusal branch commit 4 deleted.

## Changed

- **`docs/AGENT-SETUP.md`**: the paragraph is now **"The RUNTIME installs this tree, not the client"** -
  `install_seat_skills` (`rust/agenthub-runtime/src/supervisor/mod.rs:258`, called at `:545` on every
  start) clears `<seat dir>/skills` and copies the tree `AGENTHUB_CLIENT_SKILLS` names; the runtime reads
  that name from the state root's `.env` first, else from the daemon's own environment, and a machine
  with neither set has no skills installed and no error. The neighbouring "Where a skill lands, and what
  makes it count" paragraph loses the `agent.yaml` declaration it described
  (`resources.skills[]`, the profile's `uses.skills`): that declaration was the client's own tree install
  and it went with the tree locator. The paragraph's route description (`share/`, the room's, the seat's)
  and the two command lines commit 3 already corrected are untouched.
- **`README.md`**: the install note now names the runtime as the installer and the two places the name is
  read; the failure-map row that prescribed `--skills-root` is replaced by the one symptom the runtime
  actually has (a seat starts with no skills because no tree is configured).
- **`skills/client/install/SKILL.md`**: the `has no skills to deliver` row is deleted from the failure
  map. No path in the client produces that message after commit 4.

## Verified

- `git grep -n "has no skills to deliver"` at the new tip matches `CHANGELOG.md` and
  `CHANGELOG/2026-10-15--the-client-stops-delivering-skills-it-has-no-store-for.md` only - both records
  of what was, which this repository's scanner skips by design.
- The runtime half is read, not paraphrased: `install_seat_skills` is defined at
  `rust/agenthub-runtime/src/supervisor/mod.rs:258`, called from the seat start at `:545`, reads
  `SKILLS_ROOT_ENV` = `AGENTHUB_CLIENT_SKILLS` (`:251`) via `env_file_vars(state_dir)` first and
  `std::env::var` second (`:259-261`), and returns `Ok(())` with no root set (`:263`). There is NO
  "else `skills/` beside the binary" branch: the earlier draft of this paragraph and of the README note
  asserted one, and it was corrected before this commit.
- `AGENTHUB_CLIENT_SKILLS` has no writer in this repository outside the runtime and the dead
  `internal/clientskills` locator: `grep -rn "AGENTHUB_CLIENT_SKILLS" .` returns the two documents in
  this change, the runtime, and that dead package.
- `go build ./...` rc 0 and `go test -count=1 ./...` rc 0 at the new tip.

## Not in this change

- **The orphaned `internal/clientskills` package is deleted in the next commit of this lane**, not here:
  it is code, and this commit is the documentation half the lead asked for by itself.
- **Nothing pushed.** The four patch-ids of commits 1-4 are unchanged by this commit and are re-measured
  in the report.
