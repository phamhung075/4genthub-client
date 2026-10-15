## The refusal names the kind that would license it

### Fixed

- `internal/clientseat/sendpolicy.go` + `internal/commpolicy/policy.go`: a refused send now names the link kind
  that WOULD have licensed it, and the intent the pair's own declaration licenses instead. The refusal used to
  read `(no link)` for a pair that does declare a link, whenever only the intent was unlicensed - and that is not
  merely under-informing, it taught the wrong fix. `reviewer -> go-dev` and `reviewer -> fe-dev` were declared
  `delegates_to`, while every verdict row the reviewer sends carries no intent at all and is therefore decided as
  the default notice; the room read `no link` as "declare a link" and changed the DECLARATION, when the
  declaration licensed an explicit `--intent task` the whole time. The lead ruled the message must name the kind
  that would license the send (qitem-20261015055946-cb7e92b5d2b2, 2026-10-15).
- `commpolicy.Decision` carries `WouldLicense []LinkKind`, set on the no-link refusal only, so the remedy travels
  with the decision that produced it rather than being looked up again by a caller keeping its own copy of the
  table. `commpolicy.LicensedIntents(kind)` is its inverse, in `Intent` declaration order rather than a map's.
  Both read `allowedKinds`, so the hint cannot drift from the rule that refused the send.

### Testing

- `internal/clientseat/sendpolicy_test.go`: `TestASendWithNoIntentIsRefusedByADelegatesToLink` pins the whole
  remedy - `no link`, `collaborates_with` (the kind that would license a notice) and `task (delegates_to)` (the
  intent the declaration does license) - and `TestASendNoLinkAuthorizesIsRefusedAndNotDelivered` pins the
  pair-declares-nothing half. Both FAIL on the words that came before them, measured: with `sendpolicy.go` reverted
  and only the tests in place, stderr was
  `refused by 4genthub-min's communication policy: go-dev may not send a notice to reviewer (no link)` and the two
  cases failed on the absent `collaborates_with` / `declares no link to` / `task (delegates_to)`.
- `internal/commpolicy/policy_test.go`: the `TestDecide` matrix carries `wantLicense` per case, so an explicit
  deny, a self message, an unknown intent and an allowed send are pinned to carry NO remedy (naming a kind there
  would point at a declaration that is not the obstacle); `TestLicensedIntents` pins the inverse table, including
  that `spawned_by` and `can_observe` license no send.
- MEASURED: the four shapes the changed path can produce, read off the verb's own stderr with a throwaway case -
  `a collaborates_with link would license it, and go-dev -> reviewer declares task (delegates_to)`;
  `... and go-dev declares no link to reviewer`; `... declares can_observe, which licenses no send`; and an
  explicit `--intent task` over the same `delegates_to` link still exits 0.
- `go test ./internal/commpolicy/ ./internal/clientseat/ -count=1` ok; `go test ./... -count=1` ok (28 packages);
  `gofmt` clean on all four files.

### Not changed, and named rather than left silent

- `cmd/seatcheck/main.go` prints `denied: <reason>` for its own send path, whose policy comes from a seat's
  `policy.json` rather than the room's links. It teaches the same lesson with the same words. It is deliberately
  NOT touched here: the lead's order names `sendpolicy.go`'s refusal, and the checker's deny line is a second
  surface with a second source that should get the same treatment as its own decision, not as a rider on this one.
