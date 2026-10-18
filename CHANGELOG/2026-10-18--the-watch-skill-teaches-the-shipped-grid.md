# The watch skill teaches the shipped grid, not the retired python workaround

`skills/client/watch-team/SKILL.md` described the grid the client no longer draws. It said the
owner's layout "is NOT the client default", promised "an empty 6th pane for 5 seats", and carried an
"Owner's grid layout" section telling a seat to run `~/.claude/skills/watch-team/grid-layout.py`
after every `watch` — with the note that the section would drop "once `watch` produces it".

`27eeb28` produces it: `gridPlan` carves the owner's wall (lead tall left, two stacked right, the
rest along the bottom) and creates exactly one pane per seat at any count. A skill that still sent a
seat to a python script would build the layout twice and read a retired tool as the authority.

- The grid bullet now states the shipped behaviour: one pane per live seat in the wall's reading
  order (lead, architect, go-dev, fe-dev, reviewer, then any other seat by name), the default
  geometry, no empty pane, and `--cols N` for any N other than 2 keeping the older equal-columns
  grid. The options table says the same about `--cols`.
- The "Owner's grid layout" section, the `grid-layout.py` command and the "drop this section"
  note are deleted; nothing replaced them, because the client is now the only mechanism.
- True facts from the same uncommitted draft are kept, corrected: the token bar is
  `used/<limit> to compact` with the limit from `COMPACT_LIMIT_TOKENS`, the owner's `.env` held
  75_000 / 125_000 / 200_000 on 2026-10-18 (the live daemon carries those), and the supervisor
  inherits its environment from the `4genteam agent` daemon, which reads `.env` once at its start.
- The stale restart line is replaced: `4genteam compact RIG` spawns nothing and says so (agent is
  the single restart authority); a changed `COMPACT_*` value needs `4genteam stop RIG` then
  `4genteam up RIG`, or `heal` for a supervision that is down. The ladder paragraph names the three
  variables instead of hard-coding 150k / 250k / 300k, which the owner's overrides made false.

No code and no test changed: the layout was already shipped and tested in `27eeb28`.
