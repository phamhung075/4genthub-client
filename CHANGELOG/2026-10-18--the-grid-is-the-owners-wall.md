# The grid is the owner's wall: lead tall left, two stacked right, the rest along the bottom

The default `4genteam grid` / `4genteam watch` layout is now the shape the owner reads a room in:
the lead in one tall pane on the left, the next two seats stacked in the column to its right, every
remaining seat side by side along the bottom. Five seats no longer produce a sixth empty pane; the
plan creates exactly one pane per seat at any count.

- `internal/clientwatch/grid.go`: `gridPlan` is the layout as a pure function - the cuts (parent pane
  index, direction, the share the existing pane keeps) and the pane each seat is placed in - and
  `runGrid` performs them in order. The geometry is the prototype's: the root is cut once at two
  thirds so the bottom band spans the wall, then that band is split into equal seats, then the top
  band's right column is split in half. `--cols N` for any N other than the default 2 keeps the
  older equal-columns grid, which is what the flag was asked for.
- `orderSeats` reads the wall in the room's own order of work: lead, architect, go-dev, fe-dev,
  reviewer, then any other seat by name. The tmux sessions arrive alphabetically, which puts the lead
  fourth; the layout is what made that order visible.
- `docs/WATCH.md` and the `--cols` help state the default layout and what the flag keeps.

Tests: `grid_test.go` pins the owner's five-seat geometry (four cuts and the seat-to-pane slots), the
invariant that every count 1..12 creates exactly one pane per seat with no pane reused, the reading
order, and a run through the exec seam that asserts each seat's pane and the cut order.
