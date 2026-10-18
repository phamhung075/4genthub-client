# A dangling client link is healed, not walked past

Measured 2026-10-18 on `4genthub-min`. The canonical client clone was made afresh at `bcd3110` with no
worktrees of its own, so five seat links - `wt/<seat>/agenthub_client` for `architect`, `fe-dev`,
`go-dev`, `integration`, `lead` - still held a `.git` file naming
`/home/daihu/__projects__/4genthub/agenthub_client/.git/worktrees/agenthub_clientN`, a directory that
no longer exists:

```
$ git -C ~/.config/4genthub/rooms/4genthub-min/wt/architect/agenthub_client status
fatal: not a git repository: .../agenthub_client/.git/worktrees/agenthub_client4
```

Both readers asked only whether `.git` was there: `Check` read every one of those paths as
`Registered`, and `Ensure`'s `placeClientLink` returned before it looked, so `up` reported
`kept` for a seat whose client code no git verb could read.

## Changed

- `internal/clientworktree/worktree.go`
  - `linkIsDangling(dir)` - a `.git` FILE whose `gitdir:` target cannot be stat'd. Any failure counts:
    when the client repository's own `.git` is a FILE (a submodule), the metadata path below it answers
    `ENOTDIR`, which `os.IsNotExist` does not match.
  - `retireDanglingLink(target, out)` - moves the stale working tree to `<target>.obsolete` (a rename,
    never a removal: a build, a local edit or an untracked file in the old checkout survives) and names
    the move on `out`. A retirement already on disk is refused by name; nothing is overwritten.
  - `linkSubmodule` heals a dangling link through the two above, then places the link exactly as for a
    missing one; `placeClientLink` lost its own `.git` early return and delegates the decision.
- `internal/clientworktree/check.go` - `clientLinkPresent` answers false for a dangling link, so the
  path is `Wrong` (git cannot read it) until `up` heals it.

## Tests

- `internal/clientworktree/ensure_test.go`
  - `TestEnsureHealsADanglingClientLink` - real repositories: the stale checkout and its marker file are
    retired beside the link, the link is re-placed at the parent's gitlink on the seat's branch, and a
    second `Ensure` moves nothing.
  - `TestEnsureRefusesToOverwriteARetiredDanglingLink` - an existing `.obsolete` is refused by name and
    the first retirement is byte-identical after the run.
- `internal/clientworktree/check_test.go` - `TestCheckCallsADanglingClientLinkWrong`.

The five live links were repaired through the same operations the client now performs: each stale
working tree renamed to `agenthub_client.obsolete`, then a worktree of the canonical client clone placed
at the seat's path on branch `4genthub-min/<seat>` at the gitlink that seat's own worktree records
(`architect` 56ed713, `fe-dev`/`integration`/`lead` 7034a80, `go-dev` 9f84728). `git status` answers 0 in
all five parents; the only untracked path each carries is the retirement.
