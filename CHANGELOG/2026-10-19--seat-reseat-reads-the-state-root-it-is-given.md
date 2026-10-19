# `seat reseat` reads the state root it is given

## Changed

- `internal/clientseat/reseat.go`: `runReseat` takes `--state-root DIR` through
  `clientenv.StateRootFromFlags` and resolves it through `clientenv.ResolveStateRoot` - the same two
  calls `activity`, `doctor`, `ps` and the rest use - instead of calling `ResolveStateRoot("")` after a
  FlagSet that knew only `--seat` and `--force`. The flag, else the machine's own root; the retired
  `--state-dir` spelling is refused by the shared extractor before the rig is even parsed. Usage gains
  `[--state-root DIR]`. No new error path: the refusal is the same `StateRootMessage` every verb prints.
- `defaultReseatDeps` is now reached through the package-level `reseatDepsFor` seam, so the suite reads
  the root the verb actually hands the quiet waiter and the seat doctor - a flag accepted by the parser
  but dropped before the deps would still be the old bug.
- The rest of the `ResolveStateRoot("")` / `DefaultStateRoot()` set in `internal/`, classified rather
  than changed (the last of the flag-taking verbs was this one):
  - `clientlifecycle/lifecycle.go:945` (`compact`) - no `--state-root` is parsed; IGNORES the flag.
  - `clientlifecycle/stuck.go:971` and `overlap.go:716` - the `agent` supervisor's background scans
    (`scanStuck`, `scanOverlaps`), not a CLI read; the verbs themselves parse the flag (`Run`).
  - `clientlifecycle/skillscheck.go:566` - a fallback inside `seatSkillInputs` for a caller that passes
    `""`; both call sites pass a resolved root.
  - `clientservices/worktrees.go:106` - `RoomDir`, the one resolution `up`, the worktrees row and the
    `worktree` verbs use; the `worktree` verb parses only `--main`, so it IGNORES the flag.
  - `clientenv/tmux.go:44` - `TmuxArgv` for `sync install-checker`, which parses only `--go-dir/--out`.
  - `seatpark/park.go:36` - a library seam (`Refuse`), not a verb; no verb reads it from arguments.
  - `clientenv/env.go:146` - inside `ResolveStateRoot` itself, the default branch.

## Tested

- `go test ./internal/clientseat/ -run Reseat -count=1` -> ok. The new test
  (`TestReseatReadsTheStateRootFlagInsteadOfTheMachineDefault`) asserts the root handed to the deps is
  the flag's directory, that the retired spelling is refused with the one message and builds no deps,
  and that usage lists the flag. Against a788477 it fails: the FlagSet answers
  "flag provided but not defined: -state-root" (proved with an `-overlay` probe on the base revision).
- `go vet ./...` -> clean; `go test ./...` -> ok (whole module) on branch
  `4genthub-min/port-03-reseat-state-root`.
- Smoke, built `./cmd/4genteam`: `seat reseat --help` lists `[--state-root DIR]`; `seat reseat nosuchrig
  --seat lead --state-root /tmp/none` passes parsing and fails later on the room read (exit 3), where the
  same call at a788477 was exit 2 "flag provided but not defined"; `--state-dir` exits 2 naming
  `--state-root DIR`.
