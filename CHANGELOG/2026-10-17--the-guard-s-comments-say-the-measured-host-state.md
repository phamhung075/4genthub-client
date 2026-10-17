# The guard's comments say the measured host state

## What was wrong

`internal/commpolicy/policy.go`'s comment on `BypassSuspected` claimed, as a 2026-10-15 measurement, that
"`rig` on PATH is `@reference/cli`". That is not this box's measurement: `command -v rig` prints nothing,
`~/.local/bin/rig` does not exist, and no `@reference` scope is installed (the npm tree holds `@openrig/cli`
0.6.10, whose `rig` bin link is absent too). `internal/clientseat/quiet.go`'s comment claimed `rig` is "the
compat shim to `4genteam`" - a third identity for the same name. Two adjacent comments asserting different
boxes, one dated as a measurement it was not: this is the reviewer's required edit on `892f867`
(`GATE-comm-guard-native-spellings-892f867-2026-10-15.md` §3), and the stale package name reached the comment
after the gate, through the legacy-name purge (`338f192`) rewriting `@openrig/cli`.

## Changed

Comments only - no behaviour, no test:
- `internal/commpolicy/policy.go` - the retired `rig` spellings stay "because an `rig` on some other host's
  PATH is not this client", and the host fact is stated as measured (2026-10-15, re-measured 2026-10-17):
  `rig` is not on PATH at all, `~/.local/bin/rig` does not exist, the client is one file
  `~/.local/bin/4genteam`.
- `internal/clientseat/quiet.go` - `rig` is "not this client" (no shim identity), same measured dates.

## Tested

- `go build ./...` rc 0; `go test -count=1 ./...` in the client: 28 packages ok.
- `go test -count=1 -run 'TestBypassSuspected$|TestBypassSuspectedKnownShapeIsNotALicence|TestBypassSuspectedNilKnown'
  ./internal/commpolicy/`: three PASS - the deny/allow tests by name.
- Mutation, shim-only binding (the `case "4genteam"` block deleted from the switch, `case "rig"` kept): the same
  tests fail on all eight native cases plus the shape and nil-known tests - a shim spelling no longer passes.
- Mutation, the allow shape dropped (`known` matched on the program name alone):
  `TestBypassSuspectedKnownShapeIsNotALicence` fails - the licence hole is pinned.

## Not in this change

- No deny/allow table, no behaviour, no wiring: C5 (the guard on the send path) is unchanged and unmoved.
- No pin bump: the hub pin is the lead's.
