A declared link that licenses another intent is not "no link"

`4genteam send` decides an intent the sender did not name as a notice, and a link kind licenses
particular intents: `escalates_to` licenses escalation and report, `delegates_to` licenses task, and
`collaborates_with` licenses question and notice. When the pair declared a link of another kind, the
decision said `no link` anyway - the one sentence that is false - and it was read as "the edge is not
admitted": the architect's review recorded fe-dev -> lead refused as `no link` along a declared
`escalates_to` edge, with the report falling back to a row note (review 3 line 5, 2026-10-18).

`commpolicy.Decide` now separates the two states. It reads the pair's declarations across every kind
when no allowed kind matched, and refuses with `intent not licensed` when at least one exists;
`no link` is kept for a pair that declares nothing, which is the state that asks for a declaration to
be added. `WouldLicense` and the licence hint are unchanged, so the refusal still names the kind that
would license the intent and what the declaration does license instead.

Proven red-first: with the reason word forced back to `no link`, `TestDecide`, the kind matrix and
both send cases fail; with it, `go test ./internal/commpolicy/... ./internal/clientseat/...` is green,
alongside `go test ./...` and `cargo test --offline` in the runtime crate.
