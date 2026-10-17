# Nothing but counts leaves the machine to Jev

## Changed

- `internal/clientjev/countsonly.go`: one wholesale guards every request the client posts to TypeSafe
  (owner ruling O9a, 2026-10-16: "No file paths or test names to Jev/TypeSafe: counts only"). A token
  containing `/`, a token ending in a source extension, and a `Test*`/`Benchmark*`/`Example*` identifier
  become `<path>` / `<test>`. The walk is over the ENCODED payload decoded back to `any`, because the
  decide path's option labels arrive as structs and a map-of-strings, and a type switch over the
  caller's values was measured to miss them while shaping the instruction beside them.
- `internal/clientjev/jev.go`: `call` builds the request through `redactPayload`, the single place the
  payload is made, so a second entry point cannot skip the rule.
- The criteria request's `state` is `countsState`: whether the test command ran, its exit code, how many
  tests failed, files changed, lines changed, and the two commit hashes. The evidence body's numstat
  text and test command no longer travel; the judge decides on the same question with counts.
- The local `jev_decisions.log` is unchanged and still records the full question, its options and the
  pick - the shaping is at the transport, not in the caller.

## Tested

- `go test ./...` in `agenthub_client`: all pass.
- `TestTheCriteriaRequestCarriesTheEvidenceAndNothingElse`: criteria naming `./fastmcp/...` and
  `CHANGELOG/` go out with `<path>`, the raw request bytes carry neither, and `state` is the counts map.
- `TestTheWireLosesTheNamesTheLogKeeps` (clientdecide): a question naming `internal/clientjev/jev.go`
  and `TestRedactForWire` reaches the wire with placeholders and the log line keeps both names and the
  pick.
