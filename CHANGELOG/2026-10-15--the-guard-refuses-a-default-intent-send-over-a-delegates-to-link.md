## The guard refuses a default-intent send over a delegates_to link

### Added

- `internal/clientseat/sendpolicy_test.go`: `TestASendWithNoIntentIsRefusedByADelegatesToLink`. A send that
  names no intent is decided as a `notice`, and a link declared `delegates_to` authorizes only an explicit
  `--intent task` - so such a send is refused, denied in the audit (`allowed: false`, intent `notice`), and not
  delivered. This is the case the C5 gate asked for: the wiring's other lab cases all sent an explicit intent or
  ran against a single `collaborates_with` link, so none of them exercised the mismatch, and the mismatch is the
  shape of traffic a room actually produces - the reviewer's verdict rows go out with no `--intent` at all. The
  live room's `reviewer -> go-dev` and `reviewer -> fe-dev` pairs were declared `delegates_to` while every one of
  those verdicts is a default-intent send, which the declaration therefore refused (found while gating, fixed in
  `scripts/team/4genthub-min/team.json`, main repo).

### Testing

- The case PASSES on the map as designed (`go test -count=1 -run TestASendWithNoIntentIsRefusedByADelegatesToLink
  ./internal/clientseat/`, ok 0.094s) and the client suite is green (`go test -count=1 ./...`, exit 0).
- FALSIFIED, not asserted: with `IntentNotice` temporarily also licensing `KindDelegatesTo` in
  `internal/commpolicy/policy.go`, the case FAILS - `a default-intent send over a delegates_to link exited 0` -
  and the mutated map is reverted in this commit's tree.
