A gate close carries the verdict, and the client refuses one that does not

Measured 2026-10-18 (review 3 line 2): gate rows were claimed and closed with the bare word `closed` -
the body still holding the request, no PASS/FAIL anywhere on the row - five rows between 21:14 and
21:33. The tests had run; the rail, which is the loop's proof surface, carried none of them.

`gate` is now a closure reason (the daemon's set and the client's mirror), and the reason says what the
close was instead of recording a verdict as `no-follow-on`. `4genteam room queue close --reason gate`
refuses without a note, naming the missing verdict: the note must carry PASS/FAIL and the commands that
produced it. `room queue update --state done --closure-reason gate` is the same close by another verb
and is refused on the same rule, so the requirement cannot be stepped around. The refusal is decided
before the request, like the verb's other local checks.

The reviewer's skill says the same thing where it belongs: the gate row's close is the record, and
`queue-row-schema` lists the new reason with its note requirement. Proven: `go test ./internal/clientroom/...`
(gate close refused non-zero naming the verdict; the same close with the note rides it) and
`./internal/clientruntime/...` are green, `go test ./...` exits 0, `cargo test --offline` passes (184 + 1 +
19 + 1), and the built verb refuses `--reason gate` with exit 3 before it touches a socket while the same
close with a note reaches the daemon.
