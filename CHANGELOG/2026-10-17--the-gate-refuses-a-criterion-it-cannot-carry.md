# The gate refuses a criterion its bundle cannot carry, and names what it refused

The completion gate scores acceptance criteria with a judge that receives four facts and nothing else -
`clientevidence.Body`: the base/head shas, the numstat and the test run. Criteria that assert what a
production surface DID therefore cannot be verified from it, and the measured harm was that they came back as
LOW SCORES rather than as unjudgeable claims: 0.29/0.24/0.39 and 0.36/0.32/0.26 on Outcome 1 with every
deterministic check green, 0.46/0.28/0.20/0.19/0.26 on `6f461321` whose criteria span two divergent lines, and
the lead's calibration showing the number tracks the criterion's FORM (the same green work: 0.81/0.69/0.61 as
owner prose, 0.47/0.35/0.78 as denser evidence prose, 0.98/0.96/0.96/0.97/0.97 as mechanical clauses).
`seats/architect/RULING-closing-gate-register-and-unjudgeable.md` rules the second branch.

- `internal/clientcomplete/gate.go`: a REGISTER GATE runs after the deterministic checks and before the judge
  call. The register is what the bundle carries, and the only set of facts a criterion may be about: the
  submitted test command (substring, after whitespace normalisation), this run's own exit code, an exact
  numstat path, and a base/head sha prefix of seven characters or more. A criterion resolving to none of them
  is REFUSED: the verdict is `unjudgeable`, no judge call is made, and the answer names the refused criteria,
  the ones that were judgeable, the register, and the numstat's paths - because R6 corrects a refusal by
  REMOVING the claims the bundle cannot carry. One refused criterion refuses the whole task, even when the
  others would accept. Each fact stands on its own (the ruling's "a criterion resolving to NONE of these is
  refused"), so a criterion naming only the run's exit code or one carried path is judged.
- `internal/clientjev/jev.go`: the fourth verdict, `Unjudgeable`, next to the three the judge itself returns.
  It is never one of the judge's answers - the judge is not asked about a refused criterion - which is why the
  verdict's `Evaluator` is `deterministic`.
- The exit code is unchanged: an enforced `unjudgeable` rides the existing gate refusal (6), with the verdict
  in the JSON on stdout and the criteria named on stderr, exactly as `reject` and `uncertain` do. The bundle
  is NOT widened: a sha256 a judge receives can only be read as text, which is the opinion-with-a-number this
  ruling removes.

### Testing
- `internal/clientcomplete/complete_test.go`, five new cases, all failing on their assertions before the
  change: `TestTheMeasuredRegisterIsJudgedAndAccepted` (the lead's calibration run 3, criteria verbatim: five
  clauses, ACCEPT, and the judge asked exactly those five); `TestOutcomeOnesCriteriaAreUnjudgeableWithNoJudgeCall`
  (task c916e113's three criteria: `unjudgeable`, the refusal names each one and prints the register, and ZERO
  calls to the judge route); `TestACriterionIsJudgedOnlyWhenTheBundleCarriesItsFact` (the pair that makes the
  register a necessary condition: a path the numstat lists is judged, a path it does not list is refused);
  `TestABaseShaPrefixAloneIsJudged` (and the control: a sha this bundle does not name is refused);
  `TestOneRefusedCriterionRefusesTheCompletionEvenWhenTheOthersWouldAccept` (no judge call, both sets named).
- The tests capture the judge's own request body, so a gate that dropped a criterion silently would fail on
  the criteria asked rather than on a count.
- Three existing cases in the same file moved to criteria that carry register facts (`["it works"]` -> "the
  acceptance command `git --version` exits 0"), and `TestCriteriaVerdicts` does the same: their subject is the
  FLOW (evidence stored then completed; the enforced refusal before any write; the shadow log line) and the
  judge's own verdict mapping, none of which changes.
- Smoke, the built `4genteam complete` binary against a stub server, gate ENFORCED: with Outcome 1's two
  behaviour criteria the run exits 6, prints `{"verdict":"unjudgeable",...}` naming both criteria and the
  register, and the stub's judge route is called ZERO times while no other write happens; with the measured
  register's own shape (the acceptance command plus one numstat path) the SAME binary exits 0 after one judge
  call and the evidence + completion writes.
- `go build ./...`, `go vet ./...`, `gofmt -l` and `go test -count=1 ./...` in the client: all clean.

### Not implemented here
- AC2 of `283fb358` (Outcome 1's closure re-run with its verdict recorded) is a step after this lands, and it
  is the lead's: this change only makes the gate answer `unjudgeable` instead of scoring such criteria.
- The parked criteria-vocabulary patch stays parked: this lane changes the gate, not the room's vocabulary.
