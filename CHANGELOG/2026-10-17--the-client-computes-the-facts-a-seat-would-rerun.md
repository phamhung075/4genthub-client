# The client computes the facts a seat would re-run

## Added

- `internal/clientfacts`: `4genteam facts` prints ONE JSON block (`"schema":"4genteam.facts.v1"`) of the facts a seat otherwise assembles from bash chains, with a fixed key set per source:
  - `repo`: root, branch, detached, head, head_short, dirty count and paths, and the base (`main` by default) with base_sha, base_state, ahead_of_base and behind_base - the "N behind main" fact a lead's git hook warns about.
  - `pins`: the gitlink the pinning repo holds for `agenthub_client` at `--pins-ref` (main), against this HEAD: state same/ahead/behind/diverged, ahead and behind counts. The pinning repo is `--pins-repo` or the PARENT of this repo's main worktree, verified to be a git worktree whose tree carries the 160000 gitlink; a plain file at that name is refused.
  - `queue`: the room rail counted from the daemon's own answer (rows, pending, in_progress, blocked, handed_off, done, other), plus the active rows addressed to this seat's session (mine_pending, mine_in_progress, mine_blocked) and the newest active one by id and summary. It deliberately sends no `RequesterSession`, so the daemon's five-minute "no change since" limit cannot refuse a read.
  - `offpeak`: the schedule package's own verdict (peak/off-peak, peak flag, stamp, next boundary, holiday).
  - `ci`: the newest `gh run list` run for the branch with its head sha, status, conclusion, workflow, run id and url.
  - `task`: a task's acceptance record via `GET /api/v2/tasks/{id}` (status, priority, criteria, criteria_count); `--url` defaults to `AGENTHUB_URL`, the bearer token to `AGENTHUB_TOKEN`.
- `internal/clientfacts/facts_test.go`: a fixture repo pins the whole block: the exact top-level and per-source key sets, branch `work` one ahead and one behind `main`, two dirty paths in order, pin same/ahead/behind/diverged (real git, the counting direction measured before it was pinned), discovery of the parent pinning repo, base missing, queue counting with the newest active row, the task GET path/auth and its HTTP refusal, `gh` parsing and its failure, and the offpeak schedule under a pinned Friday clock. A non-repo directory is reported `unavailable` with a reason, never as a zero.
- `cmd/4genteam/main.go`: registers `facts`; `internal/clientfacts` is a dispatcher append like every other package.

## Measured

- Ranking, from the room's seat transcripts on 2026-10-17 (20,536 bash calls): git state 6,600+ (status 1,507, log 2,491, show 1,572, diff 1,065, rev-parse 687, merge-base 601, branch 423), the rail 5,316 (list/show 2,954), pin-vs-worktree compares 340, `go test` 1,561, `go build` 446, JSONL/jq reads 771, offpeak 96, task acceptance reads 90, `gh` CI 1. The verb covers the first five groups plus CI and the task record; `gh` is the one fact the room measures least, and it prints unavailable when no run exists rather than a guessed state.
- Live smoke at 2026-10-17T13:52Z in this worktree: repo branch `4genthub-min/comm-guard-unaudited-routes` at ee4f6d3, one ahead of main d708d31; pins state `ahead` (pin d708d31, head ee4f6d3, 1/0) discovered at `/home/daihu/__projects__/4genthub`; queue 1,408 rows (9 pending, 2 in progress, 2 blocked, 212 handed-off, 1,183 done), 2 pending rows to this seat, newest `qitem-20261017135025`; offpeak `off-peak` at 13:52Z; CI unavailable ("gh reports no workflow run for this branch"); task 2ad9dc23 read with its 4 criteria.

## Tested

- `go test ./internal/clientfacts`: ok. `go test ./...`: 33 packages ok, 0 failures. `go vet ./internal/clientfacts ./cmd/4genteam`: ok.
- RED first was not used: the change is a new verb with no prior behaviour to fail; the tests pin the field names and values on a fixture repo instead, which is this task's acceptance. The one live defect the smoke caught - `--url` not defaulting to `AGENTHUB_URL` - was fixed and is pinned by the block test's captured url/token.
