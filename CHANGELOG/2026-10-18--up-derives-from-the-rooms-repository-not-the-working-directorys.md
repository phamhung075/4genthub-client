# `up` derives from the room's repository, not the working directory's

Measured 2026-10-18, same binary, three working directories, one room that was up:

| run from | answer |
|---|---|
| `/home/daihu/__projects__/4genthub` (the project root, the spec's own seat cwd) | six `kept`, five `running`, exit 0 |
| `~/.config/4genthub/rooms/4genthub-min/wt/go-dev-scope` (a client worktree) | refused 6 of 6, exit 3 |
| `~/__projects__/4genthub/agenthub_client` (the client main checkout) | refused 6 of 6, exit 3 |

Every refusal read `<seat path> exists and is not a worktree registered in `git worktree list` of
<CLIENT repo>; nothing is overwritten` — naming a repository the seat paths never belonged to. The
paths are worktrees of the project's own repository; `clientworktree.MainRoot` falls back to
`git rev-parse --show-toplevel` of the working directory when neither `--main` nor
`AGENTHUB_CLIENT_SKILLS` names a checkout, so the refusal named whatever repository `up` was run in.

A refusal must name what the operator can change, and the room's definition already records the
answer: each seat has a `cwd`.

## Changed

- `internal/clientworktree/worktree.go` — new `SeatProjectRoot(cwds []string) (string, bool, error)`:
  asks git for the common directory behind each seat `cwd` and answers the one root they share.
  `ok=false` when no seat directory is readable (a room never derived yet, a definition without
  cwds) — the caller keeps the working directory's answer. A non-nil error when the readable
  directories name more than one repository.
- `internal/clientlifecycle/lifecycle.go` — the worktrees step reads the definition's repository
  through `worktreeSeatRoot` and, when it differs from the working directory's, prints
  `the room's seats live in <root>, not in the working directory's <cwd>; using <root>` and derives
  from it. Two repositories is not guessed: the working directory's answer stands and the
  disagreement is printed, because deriving half a room into a tree the other half does not live in
  is the defect this step exists to prevent.
- `internal/clientlifecycle/worktreesrow.go` — `worktreeSeatRoot` seam, beside the existing
  `worktreeMainRoot` / `worktreeCheck` / `worktreeEnsure` seams.

## Tests

- `internal/clientworktree/seatroot_test.go` — real repositories and real worktrees: a main checkout
  and its linked worktree answer one root; two repositories are refused with both named; missing and
  non-repository directories answer nothing, and a missing path stays missing.
- `internal/clientlifecycle/worktreesstep_test.go` — the step derives from the definition's root and
  prints both roots; with no readable seat directory it keeps the working directory's root and says
  nothing; with two repositories it keeps its answer and prints the disagreement.
- `go vet ./...` clean; `go test -count=1 ./...` 35 packages `ok`.

## Not changed

`doctor`'s `worktrees` row still reads `MainRoot("")` (it is handed seat keys, not the definition's
cwds), so from a wrong working directory it can still report the derived paths as `Wrong`. Its note
names the root it used, so the fact is visible; the row is not this fix's surface.
