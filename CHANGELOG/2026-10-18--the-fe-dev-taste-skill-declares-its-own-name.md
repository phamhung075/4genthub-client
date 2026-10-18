## `/fe-dev/taste-skill` declares its own name, so the loader and the directory agree

Found by the skills check on the live room: `omp skill list` loaded `design-taste-frontend` from
`seats/fe-dev/skills/taste-skill/SKILL.md`, so the check reported F2 (a name the tree does not carry) and
F3 (`taste-skill`, the name it does carry, never loaded). Nothing was stale: the installed copy was
byte-identical to the tree's.

### Changed

- `skills/rooms/4genthub-min/fe-dev/taste-skill/SKILL.md` line 2: `name: design-taste-frontend` becomes
  `name: taste-skill`. The directory has always been `taste-skill`, and
  `fe-4genthub-driven/SKILL.md` points at `../taste-skill/SKILL.md` by that name; no other file in the
  tree references `design-taste-frontend`. The loader's name, the directory and the prose now agree.

### Verified

The cause, not the symptom, is what changed:

- `omp skill list --json` with `PI_CODING_AGENT_DIR` over a fresh install of the fixed set prints
  `taste-skill` from `taste-skill/SKILL.md` - the loader's name now equals the directory's, which is what
  F2 and F3 differ on.
- `4genteam doctor 4genthub-min` with `AGENTHUB_CLIENT_SKILLS` at this checkout's `skills/` still prints
  fe-dev's F2 and F3, and that is correct: `seats/fe-dev/skills/taste-skill/SKILL.md` is still the
  pre-fix copy, so the runtime loads the old declared name. A skill change reaches a running seat at its
  next launch (ruling section 6), so the two lines clear at a `seat reseat` and not before. It also now
  reports F5 for `taste-skill`, which is the same fact stated as the filesystem sees it.
- The remaining lines for the room are the true ones: F1 for seats running in hand-made cwds and F5 for
  installed copies that predate the tree.

The check itself is unchanged: the tree was wrong, and it is fixed where it was wrong.
