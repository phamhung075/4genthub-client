# The activity timeline keeps the gate's unjudgeable verdict

`4genteam activity --problems` is the operator's view of what was refused, and `problemOutcomes` is the set it
keeps. The register gate added a fourth verdict - `unjudgeable`, the enforced gate refusing a criterion its
bundle carries no fact about - and the set named only `reject`, so a task whose completion the gate refused was
INVISIBLE to the one verb an operator uses to ask why a task did not close. The reviewer's PASS on the register
lane recorded it as a finding out of scope with a row of its own (`qitem-20261017013018`), and this lane is
that row's answer.

- `internal/clientactivity/activity.go`: `unjudgeable` joins `problemOutcomes`, for the same reason `reject` is
  there - both are the enforced gate refusing to close the task, so a hunt for a task that would not close has
  to find either - and the `--problems` line of the usage says "rejected and unjudgeable gates".
- `internal/clientactivity/activity_test.go`: one new case,
  `TestProblemsKeepsAnUnjudgeableGateAndDropsAnAcceptingOne`. Its three rows are serialised from
  `clientjev.LogEntry`, the writer's own type, so the fixture cannot drift from what `4genteam complete`
  appends to the machine-wide log; it asserts both refusing verdicts are kept, the acceptance is not, and the
  kept row renders as one line naming the verdict and the task.
- `internal/clientcomplete/complete_test.go`: the reviewer's nit on the register lane - `containsInteger`'s
  precision was unpinned - answered with `TestTheRegisterFindsAWholeNumberAndNotASubstring`: `0` is not found
  inside `10` or inside `0.98`, while the trailing dot of "exit code 0." still counts as this run's exit code.

### Testing
- Red first: the new activity case run over the branch's own `activity.go` through `-overlay` keeps only the
  rejecting row (`got [{... Outcome:reject What:gate Detail:task t-2}]`); green in the working tree.
- `go build ./...`, `go vet`, `gofmt -l` and the two packages' tests clean.
- Real binary, real run: `4genteam activity --help` from the pre-change build says "rejected gates", from this
  one "rejected and unjudgeable gates". The CLI path from argv to the filter is unchanged by this diff (no new
  flag), and the filter itself is what the new case exercises; the timeline was NOT re-run end to end against a
  freshly written `unjudgeable` row because the state root cannot be redirected (it resolves from the passwd
  home) and the machine-wide jev log is the room's own record, which a smoke must not write a synthetic row to.

### Raised, not decided here
- `uncertain` stays out of `--problems`, as before this change. That is defensible for the verdict's "the task
  has no acceptance criteria" branch, and questionable for its other branches (a missing key, a Jev error, an
  unanswered criterion are failures), but it is an older choice and this lane answers the reviewer's finding
  only. If `--problems` should mean "anything that stopped a close", the same one-line change adds it.
