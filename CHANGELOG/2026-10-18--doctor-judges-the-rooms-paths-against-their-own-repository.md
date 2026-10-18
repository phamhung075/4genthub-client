# `doctor` judges the room's paths against their own repository

Follow-up to `4fd6c67`, which gave the worktrees STEP the room's own root. The diagnostic surface still
read `clientworktree.MainRoot("")`, so the row was judged against whatever repository the command ran
in — the same defect one layer up.

Measured 2026-10-18, `4genteam doctor 4genthub-min` from a client worktree
(`~/.config/4genthub/rooms/4genthub-min/wt/go-dev-scope`):

- before: `worktrees_wrong`, six reasons — `wt/{lead,go-dev,fe-dev,reviewer,architect,integration} is
  not the seat worktree this client derives`, and `45 foreign worktree(s)` counted in the client repo;
- after: `worktrees_wrong`, TWO reasons — `wt/fe-dev` and `wt/architect`. Those two are true: neither
  path carries `agenthub_client/.git`, so the client link is missing there (the other four do). From the
  project root the lane binary prints the same two, so the row no longer depends on the working
  directory.

## Changed

- `internal/clientlifecycle/worktreesrow.go` — the row resolves the root from the derived paths it is
  about (`clientservices.DerivedWorktrees` → `worktreeSeatRoot`, the seam `up` uses) and falls back to
  `worktreeMainRoot("")` only when no derived path answers, which is a room never started. A derived
  path that spans two repositories produces the row's note naming both roots rather than a verdict from
  one of them; a row that could not look reports no verdict, as its contract says.

## Tests

`internal/clientlifecycle/worktreesrow_test.go`, three cases: the row checks against the paths' own
repository (and the resolver receives the room's derived paths); no path answering keeps the working
directory's root; paths spanning two repositories produce the note with both roots and no Check call.
`go vet ./...` clean; `go test -count=1 ./...` 35 packages `ok`.

## Found, not fixed here

`wt/fe-dev` and `wt/architect` have no `agenthub_client` link, so the row is `worktrees_wrong` on a room
that is otherwise up. `up` deliberately keeps a registered path and does not repair its links
(`4fd6c67`'s rule: only the provider-key link is written), so those two paths will read Wrong until the
links are placed or the definition stops deriving them — the operator's call, not this row's.
