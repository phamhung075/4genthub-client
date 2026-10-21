# A proposal cites any ref the census prints, not only a queue row

`4genteam skills propose --from` accepted only a `qitem-` id, and the only signal a row could carry was
`row-tag:`. With the signals this room declares, that made a whole class of demotions unproposable: a skill
demoted on a `guard: docs-only` refusal, a `jev:` kind or a refused `send:` had evidence no row carries, so the
drafting loop could not close on it.

`--from` now takes a queue row **or one of the census's own refs** - a refusal id from the guard's spool, a
`seat <seat> <ts>` blocked tool line, a `jev:<ts> <tier>` decision line, a `<from>-><to>` refused send. The refs
are not a second vocabulary: they are the `ref` field of the facts `skills evidence` prints, resolved through the
census's own loader (`LoadEvidence`), so a ref the report shows is a ref this verb can always resolve.

- `internal/clientskills/propose.go`: `citeRows` becomes `resolveCites`, which decides each value as a row or a
  fact and refuses anything else by name; `carriesSignal` pairs a declared signal with the cite of ITS OWN stream
  and the signal's exact value - the same pairing `Occurrences` makes when it demotes a skill, so a cite cannot
  reach a rule the census would never fire. A queue row still carries only `row-tag:`, unchanged.
- The evidence load is lazy and behind a seam (`proposeEvidence`): a proposal citing only rows never opens a
  stream, and a test hands over facts instead of writing four stream files.
- A row-cited proposal's record is unchanged; a ref-cited one stores the ref as its evidence line, so the gate
  reads what the census prints.

Tests (`internal/clientskills/propose_test.go`): `TestProposeCitesTheCensusRefsItPrints` is the decision's five
cases - a `guard:` skill on a refusal id and on a seat line, `jev:` on a decision line, `send:` on an audit line,
`row-tag:` on a row - plus the one that keeps the widening from being a loophole: a `guard:` skill citing a JEV
ref is refused. The pre-existing refusal still holds unchanged: a `guard:` skill citing a queue row carries
nothing. `TestProposeRefusesAValueThatIsNeitherARowNorACensusRef` replaces the test that pinned the old
row-only rule, since the rule itself is what this change repeals.

Measured: `go test -count=1 ./internal/clientskills/` passes. A live proposal in `4genthub-min` is not yet
possible - all 38 of its skills declare no signal (`skills evidence --json`: 38 no-signal), so no cite can match
until the signal blocks land; that is the sibling row, not this one.
