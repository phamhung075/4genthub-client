## The gone wait's declaration says what each caller actually lost

### What changed

The paragraph in `clientlifecycle/supervisor.go` that declares the removal of `--wait-for-idle 120` now separates the two
shapes, because they did not observe that wait equally - and the difference is a stronger reason for the removal than
the pretence alone:

- FOR THE THREE PLAIN SENDS it was a PRETENCE. They were non-raw, so the old code ran `cmd.Start` +
  `Process.Release` with no `Wait`: a detached CLI that timed out failed where nobody was listening, and this package
  never observed the outcome at all.
- FOR `/compact` it was REDUNDANT. Raw was `cmd.Run`, so that caller DID observe its wait - and its call site already
  gates the send on `s.Idle`, the one place a race with a working seat matters.

So the caller that could still have needed the wait never had it, and the one that had it never needed it. Wording
only: no behaviour changed and no code path moved.

### Verified

The claim is checkable against the commit that removed the wait, `7d07eca` ("the supervisor's sends go to the runtime"):

    git show 7d07eca^:internal/clientlifecycle/supervisor.go
    :333  args := []string{"send", ..., "--wait-for-idle", "120"}
    :339  _ = cmd.Run()                       <- the raw path observed its wait
    :342  if err := cmd.Start(); err == nil {
    :343      _ = cmd.Process.Release()       <- the non-raw path never did

Run for this entry, in this worktree, at `4630995`. `gofmt`, `go build ./...`, `go vet ./internal/clientlifecycle/` and
`go test ./...` are unchanged by a comment; the full suite stays at 24 packages ok.
